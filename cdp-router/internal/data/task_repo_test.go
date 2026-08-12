package data

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/GitMarkZhang/cdp-router/internal/config"
)

func TestTaskItemExpectedCountUsesGeneratedItemCount(t *testing.T) {
	items := []biz.TaskItem{
		{ItemID: "item-1", Seq: 1},
		{ItemID: "item-2", Seq: 2},
		{ItemID: "item-3", Seq: 3},
	}

	if got := taskItemExpectedCount(items); got != len(items) {
		t.Fatalf("expected count=%d, want generated item count %d", got, len(items))
	}
}

func TestTaskTotalCountUsesPersistedItemCount(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg, err := config.Load(filepath.Join("..", "..", "configs", "cdp-router.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureDatabase(ctx, cfg.Postgres); err != nil {
		t.Fatal(err)
	}
	db, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(ctx, db, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}

	batchNo := fmt.Sprintf("data-count-test-%d", time.Now().UnixNano())
	platformCode := "data_count_" + fmt.Sprint(time.Now().UnixNano())
	defer func() {
		taskStatements := []string{
			`delete from cdp_task_callbacks where task_id in (select id from cdp_tasks where batch_no=$1)`,
			`delete from cdp_task_events where task_id in (select id from cdp_tasks where batch_no=$1)`,
			`delete from cdp_task_items where task_id in (select id from cdp_tasks where batch_no=$1)`,
			`delete from cdp_tasks where batch_no=$1`,
		}
		for _, statement := range taskStatements {
			if _, err := db.Exec(context.Background(), statement, batchNo); err != nil {
				t.Logf("cleanup failed: %v", err)
			}
		}
		if _, err := db.Exec(context.Background(), `delete from cdp_dispatch_profiles where platform_user_id in (select id from platform_users where platform_code=$1)`, platformCode); err != nil {
			t.Logf("cleanup failed: %v", err)
		}
		if _, err := db.Exec(context.Background(), `delete from platform_users where platform_code=$1`, platformCode); err != nil {
			t.Logf("cleanup failed: %v", err)
		}
	}()
	var platformUserID int64
	if err := db.QueryRow(ctx, `
insert into platform_users (platform_code,name,api_key,status,default_provider_code,callback_mode,callback_config)
values ($1,$2,$3,'ACTIVE',null,'DEFAULT_HTTP','{}'::jsonb)
returning id
`, platformCode, "Data Count Test", "api-key-"+platformCode).Scan(&platformUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
insert into cdp_dispatch_profiles (platform_user_id, code, name, strategy, enabled, config)
values ($1,$2,$3,'ROUND_ROBIN',true,'{"providers":[{"code":"vendor_a"}]}'::jsonb)
`, platformUserID, platformCode+"_default", "Data Count Test Dispatch"); err != nil {
		t.Fatal(err)
	}

	repo := NewTaskRepo(db)
	task, err := repo.CreateTask(ctx, biz.CreateTaskInput{
		JobID:          "job-" + batchNo,
		PlatformUserID: platformUserID,
		PlatformCode:   platformCode,
		BatchNo:        batchNo,
		BizDate:        time.Now().Format("2006-01-02"),
		CallbackURL:    "https://example.com/callback",
		RawRequest:     json.RawMessage(`{}`),
		Items: []biz.TaskItem{
			{ItemID: "item-1", PlatformCode: "yuanbao", Seq: 1, Question: "q1"},
			{ItemID: "item-2", PlatformCode: "yuanbao", Seq: 2, Question: "q2"},
			{ItemID: "item-2", PlatformCode: "yuanbao", Seq: 3, Question: "duplicate should not expand persisted total"},
		},
	}, []string{"vendor_a"}, "https://gateway.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if task.TotalCount != 2 {
		t.Fatalf("created task total_count=%d, want persisted item count 2", task.TotalCount)
	}
	itemCount, err := repo.CountTaskItems(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if itemCount != 2 {
		t.Fatalf("count task items=%d, want 2", itemCount)
	}

}
