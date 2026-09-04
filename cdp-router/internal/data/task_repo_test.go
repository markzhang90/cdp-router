package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/GitMarkZhang/cdp-router/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
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
	}, []biz.ProviderSettings{{ProviderCode: "vendor_a", Enabled: true}}, "https://gateway.example.com")
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

func TestRuleBasedDispatchUsesFirstMatchedRuleProviderPool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db := openTaskRepoTestDB(t, ctx)
	defer db.Close()
	platformUserID, platformCode := createTaskRepoTestPlatformUser(t, ctx, db, "rule_dispatch")
	defer cleanupTaskRepoTestPlatformUser(t, platformCode)
	if _, err := db.Exec(ctx, `
insert into cdp_dispatch_profiles (platform_user_id, code, name, strategy, enabled, config)
values ($1,$2,$3,'RULE_BASED',true,$4)
`, platformUserID, platformCode+"_default", "Rule Dispatch", `{
  "providers":[{"code":"vendor_a"}],
  "rules":[
    {"code":"manual","name":"人工校验","sortOrder":10,"taskNameRegex":"人工校验","labels":["MANUAL_VERIFY"],"providerCodes":["vendor_b"],"enabled":true}
  ]
}`); err != nil {
		t.Fatal(err)
	}

	repo := NewTaskRepo(db)
	task, err := repo.CreateTask(ctx, biz.CreateTaskInput{
		JobID:          "job-rule-" + platformCode,
		PlatformUserID: platformUserID,
		PlatformCode:   platformCode,
		BatchNo:        "batch-rule-" + platformCode,
		TaskName:       "需要人工校验的问题",
		BizDate:        time.Now().Format("2006-01-02"),
		CallbackURL:    "https://example.com/callback",
		RawRequest:     json.RawMessage(`{}`),
		Items: []biz.TaskItem{
			{ItemID: "item-1", PlatformCode: "yuanbao", Seq: 1, Question: "q1"},
		},
	}, []biz.ProviderSettings{
		{ProviderCode: "vendor_a", Enabled: true},
		{ProviderCode: "vendor_b", Enabled: true},
	}, "https://gateway.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if task.ProviderCode != "vendor_b" {
		t.Fatalf("provider=%s, want vendor_b", task.ProviderCode)
	}

	var payload []byte
	if err := db.QueryRow(ctx, `
select payload from cdp_task_events
where task_id=$1 and event_type=$2
order by id desc limit 1
`, task.ID, biz.EventTaskDispatched).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var event struct {
		MatchedRuleCode string   `json:"matchedRuleCode"`
		Labels          []string `json:"labels"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatal(err)
	}
	if event.MatchedRuleCode != "manual" || len(event.Labels) != 1 || event.Labels[0] != "MANUAL_VERIFY" {
		t.Fatalf("dispatch event=%+v, want matched manual with MANUAL_VERIFY", event)
	}
}

func TestRuleBasedDispatchFallsBackToDefaultPoolWhenRulePoolQuotaExhausted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db := openTaskRepoTestDB(t, ctx)
	defer db.Close()
	platformUserID, platformCode := createTaskRepoTestPlatformUser(t, ctx, db, "rule_quota_fallback")
	defer cleanupTaskRepoTestPlatformUser(t, platformCode)
	providerA := "vendor_a_" + platformCode
	providerB := "vendor_b_" + platformCode
	if _, err := db.Exec(ctx, `
insert into cdp_dispatch_profiles (platform_user_id, code, name, strategy, enabled, config)
values ($1,$2,$3,'RULE_BASED',true,$4)
`, platformUserID, platformCode+"_default", "Rule Quota Fallback", fmt.Sprintf(`{
  "providers":[{"code":%q}],
  "rules":[
    {"code":"tire","name":"轮胎规则","sortOrder":10,"taskNameRegex":"轮胎","labels":["人工"],"providerCodes":[%q],"enabled":true}
  ]
}`, providerB, providerA)); err != nil {
		t.Fatal(err)
	}

	repo := NewTaskRepo(db)
	providers := []biz.ProviderSettings{
		{ProviderCode: providerA, DailyQuestionQuota: 1, Enabled: true},
		{ProviderCode: providerB, Enabled: true},
	}
	first, err := repo.CreateTask(ctx, biz.CreateTaskInput{
		JobID:          "job-rule-quota-first-" + platformCode,
		PlatformUserID: platformUserID,
		PlatformCode:   platformCode,
		BatchNo:        "batch-rule-quota-first-" + platformCode,
		TaskName:       "轮胎任务第一组",
		BizDate:        time.Now().Format("2006-01-02"),
		CallbackURL:    "https://example.com/callback",
		RawRequest:     json.RawMessage(`{}`),
		Items: []biz.TaskItem{
			{ItemID: "item-1", PlatformCode: "yuanbao", Seq: 1, Question: "q1"},
		},
	}, providers, "https://gateway.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if first.ProviderCode != providerA {
		t.Fatalf("first provider=%s, want %s", first.ProviderCode, providerA)
	}

	second, err := repo.CreateTask(ctx, biz.CreateTaskInput{
		JobID:          "job-rule-quota-second-" + platformCode,
		PlatformUserID: platformUserID,
		PlatformCode:   platformCode,
		BatchNo:        "batch-rule-quota-second-" + platformCode,
		TaskName:       "轮胎任务第二组",
		BizDate:        time.Now().Format("2006-01-02"),
		CallbackURL:    "https://example.com/callback",
		RawRequest:     json.RawMessage(`{}`),
		Items: []biz.TaskItem{
			{ItemID: "item-2", PlatformCode: "yuanbao", Seq: 1, Question: "q2"},
		},
	}, providers, "https://gateway.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if second.ProviderCode != providerB {
		t.Fatalf("second provider=%s, want %s after rule provider quota is exhausted", second.ProviderCode, providerB)
	}

	var payload []byte
	if err := db.QueryRow(ctx, `
select payload from cdp_task_events
where task_id=$1 and event_type=$2
order by id desc limit 1
`, second.ID, biz.EventTaskDispatched).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var event struct {
		MatchedRuleCode    string   `json:"matchedRuleCode"`
		Labels             []string `json:"labels"`
		ProviderCode       string   `json:"providerCode"`
		CandidateProviders []string `json:"candidateProviders"`
		Reason             string   `json:"reason"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatal(err)
	}
	if event.MatchedRuleCode != "tire" || len(event.Labels) != 1 || event.Labels[0] != "fallback" {
		t.Fatalf("dispatch event=%+v, want matched tire with fallback label", event)
	}
	if event.ProviderCode != providerB || event.Reason != "rule_pool_fallback_lowest_quota_usage_rate" {
		t.Fatalf("dispatch event=%+v, want %s with rule pool fallback reason", event, providerB)
	}
	if fmt.Sprint(event.CandidateProviders) != fmt.Sprintf("[%s]", providerB) {
		t.Fatalf("candidate providers=%v, want fallback pool [%s]", event.CandidateProviders, providerB)
	}
}

func TestRuleBasedDispatchUnmatchedFallbackSupportsRoundRobin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db := openTaskRepoTestDB(t, ctx)
	defer db.Close()
	platformUserID, platformCode := createTaskRepoTestPlatformUser(t, ctx, db, "rfrr")
	defer cleanupTaskRepoTestPlatformUser(t, platformCode)
	providerA := "vendor_a_" + platformCode
	providerB := "vendor_b_" + platformCode
	if _, err := db.Exec(ctx, `
insert into cdp_dispatch_profiles (platform_user_id, code, name, strategy, enabled, config)
values ($1,$2,$3,'RULE_BASED',true,$4)
`, platformUserID, platformCode+"_default", "Rule Fallback Round Robin", fmt.Sprintf(`{
  "providers":[{"code":%q},{"code":%q}],
  "fallbackSelectionPolicy":"ROUND_ROBIN",
  "rules":[
    {"code":"manual","name":"人工校验","sortOrder":10,"taskNameRegex":"人工校验","labels":["人工"],"providerCodes":[%q],"enabled":true}
  ]
}`, providerA, providerB, providerB)); err != nil {
		t.Fatal(err)
	}

	repo := NewTaskRepo(db)
	providers := []biz.ProviderSettings{
		{ProviderCode: providerA, Enabled: true},
		{ProviderCode: providerB, Enabled: true},
	}
	first, err := repo.CreateTask(ctx, biz.CreateTaskInput{
		JobID:          "job-rfrr-1-" + platformCode,
		PlatformUserID: platformUserID,
		PlatformCode:   platformCode,
		BatchNo:        "batch-rfrr-1-" + platformCode,
		TaskName:       "普通任务第一组",
		BizDate:        time.Now().Format("2006-01-02"),
		CallbackURL:    "https://example.com/callback",
		RawRequest:     json.RawMessage(`{}`),
		Items: []biz.TaskItem{
			{ItemID: "item-1", PlatformCode: "yuanbao", Seq: 1, Question: "q1"},
		},
	}, providers, "https://gateway.example.com")
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.CreateTask(ctx, biz.CreateTaskInput{
		JobID:          "job-rfrr-2-" + platformCode,
		PlatformUserID: platformUserID,
		PlatformCode:   platformCode,
		BatchNo:        "batch-rfrr-2-" + platformCode,
		TaskName:       "普通任务第二组",
		BizDate:        time.Now().Format("2006-01-02"),
		CallbackURL:    "https://example.com/callback",
		RawRequest:     json.RawMessage(`{}`),
		Items: []biz.TaskItem{
			{ItemID: "item-2", PlatformCode: "yuanbao", Seq: 1, Question: "q2"},
		},
	}, providers, "https://gateway.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if first.ProviderCode != providerA || second.ProviderCode != providerB {
		t.Fatalf("fallback round robin providers=%s,%s, want %s,%s", first.ProviderCode, second.ProviderCode, providerA, providerB)
	}
}

func TestDispatchQuotaUsesTaskTotalCountWithoutTaskItemScan(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db := openTaskRepoTestDB(t, ctx)
	defer db.Close()
	platformUserID, platformCode := createTaskRepoTestPlatformUser(t, ctx, db, "quota_dispatch")
	defer cleanupTaskRepoTestPlatformUser(t, platformCode)
	providerA := "vendor_a_" + platformCode
	providerB := "vendor_b_" + platformCode
	if _, err := db.Exec(ctx, `
insert into cdp_dispatch_profiles (platform_user_id, code, name, strategy, enabled, config)
values ($1,$2,$3,'RULE_BASED',true,$4)
`, platformUserID, platformCode+"_default", "Quota Dispatch", fmt.Sprintf(`{"providers":[{"code":%q},{"code":%q}],"rules":[]}`, providerA, providerB)); err != nil {
		t.Fatal(err)
	}

	repo := NewTaskRepo(db)
	providers := []biz.ProviderSettings{
		{ProviderCode: providerA, DailyQuestionQuota: 1, Enabled: true},
		{ProviderCode: providerB, Enabled: true},
	}
	first, err := repo.CreateTask(ctx, biz.CreateTaskInput{
		JobID:          "job-quota-first-" + platformCode,
		PlatformUserID: platformUserID,
		PlatformCode:   platformCode,
		BatchNo:        "batch-quota-first-" + platformCode,
		TaskName:       "普通任务",
		BizDate:        time.Now().Format("2006-01-02"),
		CallbackURL:    "https://example.com/callback",
		RawRequest:     json.RawMessage(`{}`),
		Items: []biz.TaskItem{
			{ItemID: "item-1", PlatformCode: "yuanbao", Seq: 1, Question: "q1"},
		},
	}, providers, "https://gateway.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if first.ProviderCode != providerA {
		t.Fatalf("first provider=%s, want %s", first.ProviderCode, providerA)
	}
	second, err := repo.CreateTask(ctx, biz.CreateTaskInput{
		JobID:          "job-quota-second-" + platformCode,
		PlatformUserID: platformUserID,
		PlatformCode:   platformCode,
		BatchNo:        "batch-quota-second-" + platformCode,
		TaskName:       "普通任务",
		BizDate:        time.Now().Format("2006-01-02"),
		CallbackURL:    "https://example.com/callback",
		RawRequest:     json.RawMessage(`{}`),
		Items: []biz.TaskItem{
			{ItemID: "item-2", PlatformCode: "yuanbao", Seq: 1, Question: "q2"},
		},
	}, providers, "https://gateway.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if second.ProviderCode != providerB {
		t.Fatalf("second provider=%s, want %s after %s quota is exhausted", second.ProviderCode, providerB, providerA)
	}
}

func TestMarkSubmittedRefreshesQuotaDateToSubmitDate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db := openTaskRepoTestDB(t, ctx)
	defer db.Close()
	platformUserID, platformCode := createTaskRepoTestPlatformUser(t, ctx, db, "quota_submit_date")
	defer cleanupTaskRepoTestPlatformUser(t, platformCode)
	if _, err := db.Exec(ctx, `
insert into cdp_dispatch_profiles (platform_user_id, code, name, strategy, enabled, config)
values ($1,$2,$3,'ROUND_ROBIN',true,$4)
`, platformUserID, platformCode+"_default", "Quota Submit Date", `{"providers":[{"code":"vendor_a"}]}`); err != nil {
		t.Fatal(err)
	}

	repo := NewTaskRepo(db)
	task, err := repo.CreateTask(ctx, biz.CreateTaskInput{
		JobID:          "job-quota-submit-date-" + platformCode,
		PlatformUserID: platformUserID,
		PlatformCode:   platformCode,
		BatchNo:        "batch-quota-submit-date-" + platformCode,
		TaskName:       "普通任务",
		BizDate:        time.Now().Format("2006-01-02"),
		CallbackURL:    "https://example.com/callback",
		RawRequest:     json.RawMessage(`{}`),
		Items: []biz.TaskItem{
			{ItemID: "item-1", PlatformCode: "yuanbao", Seq: 1, Question: "q1"},
		},
	}, []biz.ProviderSettings{{ProviderCode: "vendor_a", Enabled: true}}, "https://gateway.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `update cdp_tasks set quota_date='2000-01-01'::date where id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkSubmitted(ctx, task.ID, json.RawMessage(`{}`), biz.SubmitResult{
		ExternalBatchID: "external-" + platformCode,
		AcceptedCount:   1,
		RawResponse:     json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	var quotaDate string
	var today string
	if err := db.QueryRow(ctx, `
select quota_date::text, (now() at time zone 'Asia/Shanghai')::date::text
from cdp_tasks
where id=$1
`, task.ID).Scan(&quotaDate, &today); err != nil {
		t.Fatal(err)
	}
	if quotaDate != today {
		t.Fatalf("quota_date=%s, want submit date %s", quotaDate, today)
	}
}

func TestPrepareTaskForSubmitRedispatchesStaleQuotaDate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db := openTaskRepoTestDB(t, ctx)
	defer db.Close()
	platformUserID, platformCode := createTaskRepoTestPlatformUser(t, ctx, db, "stale_quota_submit")
	defer cleanupTaskRepoTestPlatformUser(t, platformCode)
	providerA := "vendor_a_" + platformCode
	providerB := "vendor_b_" + platformCode
	if _, err := db.Exec(ctx, `
insert into cdp_dispatch_profiles (platform_user_id, code, name, strategy, enabled, config)
values ($1,$2,$3,'ROUND_ROBIN',true,$4)
`, platformUserID, platformCode+"_default", "Stale Quota Submit", fmt.Sprintf(`{"providers":[{"code":%q},{"code":%q}]}`, providerA, providerB)); err != nil {
		t.Fatal(err)
	}

	repo := NewTaskRepo(db)
	providers := []biz.ProviderSettings{
		{ProviderCode: providerA, DailyQuestionQuota: 1, Enabled: true},
		{ProviderCode: providerB, Enabled: true},
	}
	used, err := repo.CreateTask(ctx, biz.CreateTaskInput{
		JobID:          "job-stale-quota-used-" + platformCode,
		PlatformUserID: platformUserID,
		PlatformCode:   platformCode,
		BatchNo:        "batch-stale-quota-used-" + platformCode,
		TaskName:       "今日额度占用",
		BizDate:        time.Now().Format("2006-01-02"),
		CallbackURL:    "https://example.com/callback",
		RawRequest:     json.RawMessage(`{}`),
		Items: []biz.TaskItem{
			{ItemID: "item-used", PlatformCode: "yuanbao", Seq: 1, Question: "q1"},
		},
	}, providers, "https://gateway.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if used.ProviderCode != providerA {
		t.Fatalf("used provider=%s, want %s", used.ProviderCode, providerA)
	}
	stale, err := repo.CreateTask(ctx, biz.CreateTaskInput{
		JobID:          "job-stale-quota-pending-" + platformCode,
		PlatformUserID: platformUserID,
		PlatformCode:   platformCode,
		BatchNo:        "batch-stale-quota-pending-" + platformCode,
		TaskName:       "跨天待提交",
		BizDate:        time.Now().Format("2006-01-02"),
		CallbackURL:    "https://example.com/callback",
		RawRequest:     json.RawMessage(`{}`),
		Items: []biz.TaskItem{
			{ItemID: "item-pending", PlatformCode: "yuanbao", Seq: 1, Question: "q2"},
		},
	}, providers, "https://gateway.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `update cdp_tasks set provider_code=$2, quota_date='2000-01-01'::date where id=$1`, stale.ID, providerA); err != nil {
		t.Fatal(err)
	}
	prepared, err := repo.PrepareTaskForSubmit(ctx, stale.ID, providers, "https://gateway.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if prepared.ProviderCode != providerB {
		t.Fatalf("prepared provider=%s, want fallback provider %s", prepared.ProviderCode, providerB)
	}
	var quotaDate string
	var today string
	if err := db.QueryRow(ctx, `
select quota_date::text, (now() at time zone 'Asia/Shanghai')::date::text
from cdp_tasks
where id=$1
`, prepared.ID).Scan(&quotaDate, &today); err != nil {
		t.Fatal(err)
	}
	if quotaDate != today {
		t.Fatalf("quota_date=%s, want submit date %s", quotaDate, today)
	}
}

func TestGetTaskByBatchNoIgnoresSoftDeletedTask(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db := openTaskRepoTestDB(t, ctx)
	defer db.Close()
	platformUserID, platformCode := createTaskRepoTestPlatformUser(t, ctx, db, "soft_deleted_batch")
	defer cleanupTaskRepoTestPlatformUser(t, platformCode)
	if _, err := db.Exec(ctx, `
insert into cdp_dispatch_profiles (platform_user_id, code, name, strategy, enabled, config)
values ($1,$2,$3,'ROUND_ROBIN',true,$4)
`, platformUserID, platformCode+"_default", "Soft Deleted Batch", `{"providers":[{"code":"vendor_a"}]}`); err != nil {
		t.Fatal(err)
	}

	repo := NewTaskRepo(db)
	batchNo := "batch-soft-delete-" + platformCode
	first, err := repo.CreateTask(ctx, biz.CreateTaskInput{
		JobID:          "job-first-" + platformCode,
		PlatformUserID: platformUserID,
		PlatformCode:   platformCode,
		BatchNo:        batchNo,
		TaskName:       "第一组",
		BizDate:        time.Now().Format("2006-01-02"),
		CallbackURL:    "https://example.com/callback",
		RawRequest:     json.RawMessage(`{}`),
		Items: []biz.TaskItem{
			{ItemID: "item-1", PlatformCode: "yuanbao", Seq: 1, Question: "q1"},
		},
	}, []biz.ProviderSettings{{ProviderCode: "vendor_a", Enabled: true}}, "https://gateway.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `update cdp_tasks set deleted_at=now(), deleted_by='test' where id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	second, err := repo.CreateTask(ctx, biz.CreateTaskInput{
		JobID:          "job-second-" + platformCode,
		PlatformUserID: platformUserID,
		PlatformCode:   platformCode,
		BatchNo:        batchNo,
		TaskName:       "第二组",
		BizDate:        time.Now().Format("2006-01-02"),
		CallbackURL:    "https://example.com/callback",
		RawRequest:     json.RawMessage(`{}`),
		Items: []biz.TaskItem{
			{ItemID: "item-2", PlatformCode: "yuanbao", Seq: 1, Question: "q2"},
		},
	}, []biz.ProviderSettings{{ProviderCode: "vendor_a", Enabled: true}}, "https://gateway.example.com")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.GetTaskByBatchNo(ctx, batchNo)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != second.ID {
		t.Fatalf("loaded task id=%d, want active task id=%d", loaded.ID, second.ID)
	}
}

func TestCreateReassignedTaskCopiesSourceAndResolvesProviderBatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db := openTaskRepoTestDB(t, ctx)
	defer db.Close()
	platformUserID, platformCode := createTaskRepoTestPlatformUser(t, ctx, db, "reassign")
	defer cleanupTaskRepoTestPlatformUser(t, platformCode)
	if _, err := db.Exec(ctx, `
insert into cdp_dispatch_profiles (platform_user_id, code, name, strategy, enabled, config)
values ($1,$2,$3,'ROUND_ROBIN',true,$4)
`, platformUserID, platformCode+"_default", "Reassign Dispatch", `{"providers":[{"code":"vendor_a"}]}`); err != nil {
		t.Fatal(err)
	}

	repo := NewTaskRepo(db)
	sourceBatchNo := "batch-reassign-" + platformCode
	source, err := repo.CreateTask(ctx, biz.CreateTaskInput{
		JobID:          "job-source-" + platformCode,
		PlatformUserID: platformUserID,
		PlatformCode:   platformCode,
		BatchNo:        sourceBatchNo,
		TaskName:       "原任务",
		BizDate:        time.Now().Format("2006-01-02"),
		CallbackURL:    "https://example.com/callback",
		RawRequest:     json.RawMessage(`{"batchNo":"source"}`),
		Items: []biz.TaskItem{
			{ItemID: "item-1", PlatformCode: "yuanbao", Seq: 1, Question: "q1"},
			{ItemID: "item-2", PlatformCode: "yuanbao", Seq: 2, Question: "q2"},
		},
	}, []biz.ProviderSettings{{ProviderCode: "vendor_a", Enabled: true}}, "https://gateway.example.com")
	if err != nil {
		t.Fatal(err)
	}

	target, err := repo.CreateReassignedTask(ctx, biz.ReassignTaskRecordInput{
		SourceBatchNo:  sourceBatchNo,
		TargetBatchNo:  sourceBatchNo + "-REASSIGN-test",
		TargetJobID:    "job-target-" + platformCode,
		ProviderCode:   "vendor_b",
		Reason:         "test",
		OperatorUserID: "tester",
	}, "https://gateway.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if target.ReassignedFromTaskID != source.ID {
		t.Fatalf("reassigned_from_task_id=%d, want %d", target.ReassignedFromTaskID, source.ID)
	}
	if target.CallbackBatchNo != sourceBatchNo {
		t.Fatalf("callback_batch_no=%q, want %q", target.CallbackBatchNo, sourceBatchNo)
	}
	if target.CallbackExternalBatchID != source.JobID {
		t.Fatalf("callback_external_batch_id=%q, want %q", target.CallbackExternalBatchID, source.JobID)
	}
	itemCount, err := repo.CountTaskItems(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if itemCount != 2 {
		t.Fatalf("target item count=%d, want 2", itemCount)
	}
	if _, err := db.Exec(ctx, `update cdp_tasks set quota_date=now() - interval '1 day' where id=$1`, target.ID); err != nil {
		t.Fatal(err)
	}
	prepared, err := repo.PrepareTaskForSubmit(ctx, target.ID, []biz.ProviderSettings{{ProviderCode: "vendor_a", Enabled: true}}, "https://gateway.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if prepared.ProviderCode != "vendor_b" || prepared.ProviderCallbackURL != "https://gateway.example.com/external/callback/vendor_b" {
		t.Fatalf("prepared reassigned task provider=%s callback_url=%s, want vendor_b without redispatch", prepared.ProviderCode, prepared.ProviderCallbackURL)
	}
	resolved, err := repo.ResolveProviderTask(ctx, "vendor_b", sourceBatchNo)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ID != target.ID {
		t.Fatalf("resolved task id=%d, want target id=%d", resolved.ID, target.ID)
	}
	if err := repo.MarkTaskReassigned(ctx, source.ID, target.ID, map[string]any{"reason": "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ResolveProviderTask(ctx, "vendor_a", sourceBatchNo); !errors.Is(err, biz.ErrTaskClosed) {
		t.Fatalf("source provider resolve err=%v, want task closed", err)
	}
}

func TestCreateReassignedTaskInheritsOriginalCallbackIdentifiersWhenSourceIsReassignedTask(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db := openTaskRepoTestDB(t, ctx)
	defer db.Close()
	platformUserID, platformCode := createTaskRepoTestPlatformUser(t, ctx, db, "reassign_chain")
	defer cleanupTaskRepoTestPlatformUser(t, platformCode)
	if _, err := db.Exec(ctx, `
insert into cdp_dispatch_profiles (platform_user_id, code, name, strategy, enabled, config)
values ($1,$2,$3,'ROUND_ROBIN',true,$4)
`, platformUserID, platformCode+"_default", "Reassign Chain Dispatch", `{"providers":[{"code":"vendor_a"}]}`); err != nil {
		t.Fatal(err)
	}

	repo := NewTaskRepo(db)
	sourceBatchNo := "batch-reassign-chain-" + platformCode
	source, err := repo.CreateTask(ctx, biz.CreateTaskInput{
		JobID:          "job-source-" + platformCode,
		PlatformUserID: platformUserID,
		PlatformCode:   platformCode,
		BatchNo:        sourceBatchNo,
		TaskName:       "原任务",
		BizDate:        time.Now().Format("2006-01-02"),
		CallbackURL:    "https://example.com/callback",
		RawRequest:     json.RawMessage(`{"batchNo":"source"}`),
		Items: []biz.TaskItem{
			{ItemID: "item-1", PlatformCode: "yuanbao", Seq: 1, Question: "q1"},
		},
	}, []biz.ProviderSettings{{ProviderCode: "vendor_a", Enabled: true}}, "https://gateway.example.com")
	if err != nil {
		t.Fatal(err)
	}

	firstTarget, err := repo.CreateReassignedTask(ctx, biz.ReassignTaskRecordInput{
		SourceBatchNo:  sourceBatchNo,
		TargetBatchNo:  sourceBatchNo + "-REASSIGN-first",
		TargetJobID:    "job-first-" + platformCode,
		ProviderCode:   "vendor_b",
		Reason:         "first",
		OperatorUserID: "tester",
	}, "https://gateway.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if firstTarget.CallbackBatchNo != sourceBatchNo || firstTarget.CallbackExternalBatchID != source.JobID {
		t.Fatalf("first callback identifiers batch=%q external=%q, want batch=%q external=%q",
			firstTarget.CallbackBatchNo, firstTarget.CallbackExternalBatchID, sourceBatchNo, source.JobID)
	}

	secondTarget, err := repo.CreateReassignedTask(ctx, biz.ReassignTaskRecordInput{
		SourceBatchNo:  firstTarget.BatchNo,
		TargetBatchNo:  sourceBatchNo + "-REASSIGN-second",
		TargetJobID:    "job-second-" + platformCode,
		ProviderCode:   "vendor_c",
		Reason:         "second",
		OperatorUserID: "tester",
	}, "https://gateway.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if secondTarget.ReassignedFromTaskID != firstTarget.ID {
		t.Fatalf("second reassigned_from_task_id=%d, want %d", secondTarget.ReassignedFromTaskID, firstTarget.ID)
	}
	if secondTarget.CallbackBatchNo != sourceBatchNo {
		t.Fatalf("second callback_batch_no=%q, want original %q", secondTarget.CallbackBatchNo, sourceBatchNo)
	}
	if secondTarget.CallbackExternalBatchID != source.JobID {
		t.Fatalf("second callback_external_batch_id=%q, want original %q", secondTarget.CallbackExternalBatchID, source.JobID)
	}

	var payload []byte
	if err := db.QueryRow(ctx, `
select payload from cdp_task_events
where task_id=$1 and event_type=$2
order by id desc limit 1
`, firstTarget.ID, biz.EventTaskReassignRequested).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var event struct {
		OriginalBatchNo         string `json:"originalBatchNo"`
		CallbackExternalBatchID string `json:"callbackExternalBatchId"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatal(err)
	}
	if event.OriginalBatchNo != sourceBatchNo || event.CallbackExternalBatchID != source.JobID {
		t.Fatalf("second reassign event=%+v, want original identifiers", event)
	}
}

func openTaskRepoTestDB(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
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
	if err := Migrate(ctx, db, filepath.Join("..", "..", "migrations")); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}

func createTaskRepoTestPlatformUser(t *testing.T, ctx context.Context, db *pgxpool.Pool, prefix string) (int64, string) {
	t.Helper()
	platformCode := prefix + "_" + fmt.Sprint(time.Now().UnixNano())
	var platformUserID int64
	if err := db.QueryRow(ctx, `
insert into platform_users (platform_code,name,api_key,status,default_provider_code,callback_mode,callback_config)
values ($1,$2,$3,'ACTIVE',null,'DEFAULT_HTTP','{}'::jsonb)
returning id
`, platformCode, "Task Repo Test", "api-key-"+platformCode).Scan(&platformUserID); err != nil {
		t.Fatal(err)
	}
	return platformUserID, platformCode
}

func cleanupTaskRepoTestPlatformUser(t *testing.T, platformCode string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg, err := config.Load(filepath.Join("..", "..", "configs", "cdp-router.conf"))
	if err != nil {
		t.Logf("cleanup config load failed: %v", err)
		return
	}
	db, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Logf("cleanup db open failed: %v", err)
		return
	}
	defer db.Close()
	statements := []string{
		`delete from cdp_task_callbacks where task_id in (select id from cdp_tasks where platform_code=$1)`,
		`delete from cdp_task_events where task_id in (select id from cdp_tasks where platform_code=$1)`,
		`delete from cdp_task_items where task_id in (select id from cdp_tasks where platform_code=$1)`,
		`delete from cdp_tasks where platform_code=$1`,
		`delete from cdp_dispatch_profiles where platform_user_id in (select id from platform_users where platform_code=$1)`,
		`delete from platform_users where platform_code=$1`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(ctx, statement, platformCode); err != nil {
			t.Logf("cleanup failed: %v", err)
		}
	}
}
