package data

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/GitMarkZhang/cdp-router/internal/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func openQueryOptimizationTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "..", "configs", "cdp-router.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Postgres.Host != "127.0.0.1" && cfg.Postgres.Host != "localhost" && cfg.Postgres.Host != "::1" {
		t.Fatal("query tests require loopback PostgreSQL")
	}
	cfg.Postgres.Database = fmt.Sprintf("cdp_query_test_%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := EnsureDatabase(ctx, cfg.Postgres); err != nil {
		t.Fatal(err)
	}
	db, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		database := cfg.Postgres.Database
		cfg.Postgres.Database = "postgres"
		admin, err := Open(ctx, cfg.Postgres)
		if err != nil {
			t.Error(err)
			return
		}
		defer admin.Close()
		if _, err := admin.Exec(ctx, "drop database "+pgx.Identifier{database}.Sanitize()+" with (force)"); err != nil {
			t.Error(err)
		}
	})
	if err := Migrate(ctx, db, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestCompleteResultImportPreservesReadinessAndSkipsUnchangedWrites(t *testing.T) {
	db := openQueryOptimizationTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repo := NewTaskRepo(db)
	cases := []struct {
		name            string
		status          biz.TaskStatus
		notify          bool
		response        string
		itemStatus      string
		results         []string
		wantStatus      biz.TaskStatus
		ready           bool
		success, failed int
	}{
		{"provider_done", biz.TaskStatusProviderDone, true, `{}`, "IMPORTED", []string{"SUCCESS"}, biz.TaskStatusCallbackPending, true, 1, 0},
		{"imported_callback", biz.TaskStatusResultImported, true, `{"providerCallback":{"totalCount":1,"successCount":1,"failedCount":0}}`, "IMPORTED", []string{"SUCCESS"}, biz.TaskStatusCallbackPending, true, 1, 0},
		{"imported_no_callback", biz.TaskStatusResultImported, true, `{}`, "IMPORTED", []string{"SUCCESS"}, biz.TaskStatusResultImported, false, 1, 0},
		{"pending", biz.TaskStatusCallbackPending, true, `{}`, "IMPORTED", []string{"SUCCESS"}, biz.TaskStatusCallbackPending, true, 1, 0},
		{"done", biz.TaskStatusDone, true, `{}`, "IMPORTED", []string{"SUCCESS"}, biz.TaskStatusDone, false, 1, 0},
		{"no_notify", biz.TaskStatusProviderDone, false, `{}`, "IMPORTED", []string{"SUCCESS"}, biz.TaskStatusResultImported, false, 1, 0},
		{"mismatch_total", biz.TaskStatusProviderDone, true, `{"providerCallback":{"totalCount":2}}`, "IMPORTED", []string{"SUCCESS"}, biz.TaskStatusResultImported, false, 1, 0},
		{"mismatch_success", biz.TaskStatusProviderDone, true, `{"providerCallback":{"totalCount":1,"successCount":0,"failedCount":0}}`, "IMPORTED", []string{"SUCCESS"}, biz.TaskStatusResultImported, false, 1, 0},
		{"success_wins", biz.TaskStatusProviderDone, true, `{}`, "IMPORTED", []string{"SUCCESS", "ERROR"}, biz.TaskStatusCallbackPending, true, 1, 0},
		{"parse_failed", biz.TaskStatusProviderDone, true, `{}`, "ERROR", []string{"ERROR"}, biz.TaskStatusResultImported, false, 0, 1},
		{"missing", biz.TaskStatusProviderDone, true, `{}`, "PENDING", nil, biz.TaskStatusResultImported, false, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var id, itemID int64
			err := db.QueryRow(ctx, `insert into cdp_tasks(batch_no,provider_code,status,raw_request,provider_response,biz_date)
values($1,'local',$2,'{}',$3,current_date) returning id`, tc.name, tc.status, tc.response).Scan(&id)
			if err != nil {
				t.Fatal(err)
			}
			err = db.QueryRow(ctx, `insert into cdp_task_items(task_id,item_id,platform_code,seq,question,result_status,raw_item)
values($1,'item','local',1,'q',$2,'{}') returning id`, id, tc.itemStatus).Scan(&itemID)
			if err != nil {
				t.Fatal(err)
			}
			for seq, status := range tc.results {
				if _, err := db.Exec(ctx, `insert into cdp_task_item_results(task_id,task_item_id,batch_no,item_id,seq,cleaned_payload,parse_status,result_file_id,validation_status)
values($1,$2,$3,'item',$4,'{}',$5,1,'PASS')`, id, itemID, tc.name, seq+1, status); err != nil {
					t.Fatal(err)
				}
			}
			task, ready, err := repo.CompleteResultImport(ctx, id, tc.notify)
			if err != nil {
				t.Fatal(err)
			}
			if task.Status != tc.wantStatus || ready != tc.ready || task.TotalCount != 1 || task.SuccessCount != tc.success || task.FailedCount != tc.failed {
				t.Fatalf("task=%+v ready=%v", task, ready)
			}
			var before, after string
			if err := db.QueryRow(ctx, `select xmin::text from cdp_tasks where id=$1`, id).Scan(&before); err != nil {
				t.Fatal(err)
			}
			_, ready, err = repo.CompleteResultImport(ctx, id, tc.notify)
			if err != nil || ready != tc.ready {
				t.Fatalf("second ready=%v err=%v", ready, err)
			}
			if err := db.QueryRow(ctx, `select xmin::text from cdp_tasks where id=$1`, id).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if before != after {
				t.Fatal("unchanged second check wrote a tuple")
			}
		})
	}
}

func TestProviderProgressSkipsUnchangedItemWrites(t *testing.T) {
	db := openQueryOptimizationTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var id int64
	if err := db.QueryRow(ctx, `insert into cdp_tasks(batch_no,provider_code,status,raw_request,biz_date)
values('progress-noop','local','SUBMITTED','{}',current_date) returning id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `insert into cdp_task_items(task_id,item_id,platform_code,seq,question,raw_item)
values($1,'item','local',1,'q','{}')`, id); err != nil {
		t.Fatal(err)
	}
	repo := NewTaskRepo(db)
	progress := biz.ProviderStatusResult{Questions: []biz.ProviderQuestionStatus{{QuestionID: "item", Status: 1}}}
	if err := repo.MarkProviderStatus(ctx, id, progress); err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err := db.QueryRow(ctx, `select xmin::text from cdp_task_items where task_id=$1`, id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkProviderStatus(ctx, id, progress); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `select xmin::text from cdp_task_items where task_id=$1`, id).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("identical progress rewrote item")
	}
	progress.Questions[0].Status = 2
	if err := repo.MarkProviderStatus(ctx, id, progress); err != nil {
		t.Fatal(err)
	}
	var status int
	if err := db.QueryRow(ctx, `select status,xmin::text from cdp_task_items where task_id=$1`, id).Scan(&status, &after); err != nil {
		t.Fatal(err)
	}
	if status != 2 || before == after {
		t.Fatal("changed progress was not persisted")
	}
}
