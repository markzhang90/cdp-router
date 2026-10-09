package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	adminv1 "github.com/GitMarkZhang/cdp-router/api/admin/v1"
	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/GitMarkZhang/cdp-router/internal/data"
	"github.com/GitMarkZhang/cdp-router/internal/httpapi"
	"github.com/GitMarkZhang/cdp-router/internal/service"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestOptimizedTaskQueriesThroughHTTPWithRealPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cfg, err := loadLocalE2EConfig(t)
	if err != nil {
		t.Fatal(err)
	}
	if err := data.EnsureDatabase(ctx, cfg.Postgres); err != nil {
		t.Fatal(err)
	}
	db, err := data.Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := data.Migrate(ctx, db, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	cfg.Redis.Enabled = true
	redis, cleanup := openRealRedisForTest(t, ctx, cfg)
	defer cleanup()
	defer redis.Close()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`insert into cdp_tasks (batch_no,task_name,provider_code,status,raw_request,result_payload,result_imported_at,biz_date)
 select 'opt-'||n,case when n=45 then '' else 'task-'||n end,'vendor_a','RESULT_IMPORTED','{}',
 jsonb_build_object('retained',repeat('result',10000)),now(),current_date from generate_series(1,45) n`)
	var taskID int64
	if err := db.QueryRow(ctx, `select id from cdp_tasks where batch_no='opt-1'`).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	exec(`insert into cdp_task_items(task_id,item_id,platform_code,seq,question,status,result_status,raw_item)
 select $1,'item-'||n,'local',1,'question-'||n,2,
 case when n=3 then 'PENDING' else 'IMPORTED' end,'{}' from generate_series(1,4) n`, taskID)
	exec(`insert into cdp_task_item_results(task_id,task_item_id,batch_no,item_id,seq,answer_text,cleaned_payload,parse_status,result_file_id)
 select ti.task_id,ti.id,'opt-1',ti.item_id,r.seq,repeat('answer',10000),'{}',r.status,1
 from cdp_task_items ti join (values ('item-1',1,'SUCCESS'),('item-1',3,'ERROR'),('item-2',1,'ERROR'),('item-4',1,'SUCCESS')) r(item_id,seq,status)
 on ti.item_id=r.item_id where ti.task_id=$1`, taskID)
	repo := data.NewTaskRepo(db)
	uc := biz.NewTaskUsecase(repo, nil, nil, nil, nil, nil, biz.TaskUsecaseOptions{})
	server := httptest.NewServer(httpapi.New(service.NewTaskService(uc, nil, data.NewPlatformUserRepo(db), ""), service.NewAdminService(cfg.Admin, redis, data.NewAdminRepo(db), uc, cfg.Provider)).Handler())
	defer server.Close()
	auth := adminLogin(t, server.URL, cfg)
	get := func(path string, dst proto.Message) {
		t.Helper()
		resp := adminGet(t, server.URL+path, auth)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: status=%d body=%s", path, resp.StatusCode, body)
		}
		if err := protojson.Unmarshal(body, dst); err != nil {
			t.Fatal(err)
		}
	}
	for page := 1; page <= 3; page++ {
		var list adminv1.ListTasksResponse
		get(fmt.Sprintf("/api/admin/v1/tasks?page=%d&pageSize=20", page), &list)
		if list.Data.Total != 45 {
			t.Fatalf("total=%d", list.Data.Total)
		}
		for i, task := range list.Data.Tasks {
			want := fmt.Sprintf("opt-%d", 45-(page-1)*20-i)
			if task.BatchNo != want {
				t.Fatalf("stable page %d row %d: got %s want %s", page, i, task.BatchNo, want)
			}
		}
	}
	for _, tc := range []struct {
		pattern string
		count   int32
	}{{"%", 45}, {"task", 44}, {"_", 44}, {"missing", 0}} {
		var list adminv1.ListTasksResponse
		get("/api/admin/v1/tasks?taskName="+url.QueryEscape(tc.pattern), &list)
		if list.Data.Total != tc.count {
			t.Fatalf("pattern %q total=%d want %d", tc.pattern, list.Data.Total, tc.count)
		}
	}
	expected := []struct {
		item   string
		seq    int32
		status string
	}{{"item-1", 3, "ERROR"}, {"item-2", 1, "ERROR"}, {"item-1", 2, "PENDING"}, {"item-3", 1, "PENDING"}, {"item-1", 1, "IMPORTED"}, {"item-4", 1, "IMPORTED"}}
	for page := 1; page <= 4; page++ {
		var detail adminv1.GetTaskDetailResponse
		get(fmt.Sprintf("/api/admin/v1/tasks/%d?itemPage=%d&itemPageSize=2", taskID, page), &detail)
		if detail.Data.ItemTotal != 6 {
			t.Fatalf("expanded total=%d", detail.Data.ItemTotal)
		}
		if detail.Data.Task.ResultSuccessCount != 2 || detail.Data.Task.ResultFailedCount != 1 || detail.Data.Task.ResultMissingCount != 1 {
			t.Fatalf("item-level summary=%v", detail.Data.Task)
		}
		wantLen := 2
		if page == 4 {
			wantLen = 0
		}
		if len(detail.Data.Items) != wantLen {
			t.Fatalf("page %d len=%d", page, len(detail.Data.Items))
		}
		for i, item := range detail.Data.Items {
			want := expected[(page-1)*2+i]
			if item.ItemId != want.item || item.Seq != want.seq || item.ResultStatus != want.status {
				t.Fatalf("page %d item=%v want=%+v", page, item, want)
			}
		}
	}
	task, err := repo.GetTask(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(task.ResultPayload) != 0 {
		t.Fatal("normal read loaded large result")
	}
	payload, err := repo.GetTaskResultPayload(ctx, taskID)
	if err != nil || len(payload) < 60000 {
		t.Fatalf("retained payload len=%d err=%v", len(payload), err)
	}
	// Readiness remains incomplete, and an unchanged second pass must not write a tuple.
	_, ready, err := repo.CompleteResultImport(ctx, taskID, true)
	if err != nil || ready {
		t.Fatalf("incomplete ready=%v err=%v", ready, err)
	}
	var before, after string
	if err := db.QueryRow(ctx, `select xmin::text from cdp_tasks where id=$1`, taskID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	_, ready, err = repo.CompleteResultImport(ctx, taskID, true)
	if err != nil || ready {
		t.Fatalf("repeat ready=%v err=%v", ready, err)
	}
	if err := db.QueryRow(ctx, `select xmin::text from cdp_tasks where id=$1`, taskID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("unchanged readiness rewrote task")
	}
}
