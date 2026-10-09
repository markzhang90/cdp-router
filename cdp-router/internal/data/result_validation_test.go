package data

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
)

func TestValidationAggregationPartialOverwriteAndLegacyInitialization(t *testing.T) {
	db := openQueryOptimizationTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var taskID, itemID, fileID int64
	if err := db.QueryRow(ctx, `insert into cdp_tasks(batch_no,provider_code,status,raw_request,biz_date,provider_response) values('risk-rounds','local','CALLBACK_PENDING','{}',current_date,'{"providerCallback":{"totalCount":1,"successCount":1,"failedCount":0}}') returning id`).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `insert into cdp_task_items(task_id,item_id,platform_code,seq,question,result_status,raw_item) values($1,'item','local',1,'q','IMPORTED','{}') returning id`, taskID).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `insert into cdp_task_result_files(task_id,batch_no,external_batch_id,source_type,file_url,parse_status) values($1,'risk-rounds','external','PROVIDER_FILE','https://example.test/old.xlsx','SUCCESS') returning id`, taskID).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	for i, answer := range []string{"短", strings.Repeat("长", 20), strings.Repeat("好", 20), strings.Repeat("引", 20)} {
		status := "UNKNOWN"
		version := ""
		if i == 2 {
			status = "PASS"
			version = "existing"
		}
		references, referenceCount := `[]`, 0
		if i == 3 {
			references, referenceCount = `[{}]`, 1
		}
		if _, err := db.Exec(ctx, `insert into cdp_task_item_results(task_id,task_item_id,batch_no,item_id,seq,cleaned_payload,parse_status,result_file_id,answer_text,references_json,validation_status,validation_rule_version,reference_count) values($1,$2,'risk-rounds','item',$3,'{}','SUCCESS',$4,$5,$8,$6,$7,$9)`, taskID, itemID, i+1, fileID, answer, status, version, references, referenceCount); err != nil {
			t.Fatal(err)
		}
	}
	repo := NewTaskRepo(db)
	cache := biz.NewValidationRuleCache(repo, time.Minute)
	if err := cache.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	usecase := biz.NewTaskUsecase(repo, nil, nil, nil, nil, nil, biz.TaskUsecaseOptions{ValidationRules: cache})
	ids, err := repo.ListInitialValidationTasks(ctx, 10, 0)
	if err != nil || len(ids) != 1 {
		t.Fatalf("candidates=%v err=%v", ids, err)
	}
	if err = usecase.InitializeTaskValidation(ctx, taskID); err != nil {
		t.Fatal(err)
	}
	task, err := repo.GetTask(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.RiskCount != 2 || task.InvalidAnswerCount != 1 || task.Status != biz.TaskStatusResultImported || task.RiskStatsUpdatedAt == nil {
		t.Fatalf("initial task=%+v", task)
	}
	var summary map[string]int
	if err = json.Unmarshal(task.RiskSummary, &summary); err != nil {
		t.Fatal(err)
	}
	if summary[biz.RiskAnswerTooShort] != 1 || summary[biz.RiskAnswerNoReference] != 2 {
		t.Fatalf("summary=%v", summary)
	}
	var version string
	if err = db.QueryRow(ctx, `select validation_rule_version from cdp_task_item_results where task_id=$1 and seq=3`, taskID).Scan(&version); err != nil || version != "existing" {
		t.Fatalf("existing rule version changed %q %v", version, err)
	}
	ids, err = repo.ListInitialValidationTasks(ctx, 10, 0)
	if err != nil || len(ids) != 0 {
		t.Fatalf("repeat candidates=%v err=%v", ids, err)
	}
	save := func(seq int, fileName string, text string) {
		t.Helper()
		answer := biz.TaskItemResult{ItemID: "item", Seq: seq, AnswerText: text, ReferenceCount: 1, ReferencesJSON: json.RawMessage(`[{"url":"https://example.test"}]`), RecommendedQuestions: json.RawMessage(`[]`), CleanedPayload: json.RawMessage(`{}`), ParseStatus: "SUCCESS"}
		answer.AnswerValidation = biz.ValidateAnswer(answer, biz.ResultValidationRules{Version: "2"})
		err := repo.MarkResultImported(ctx, taskID, biz.ResultFileInput{SourceType: "PROVIDER_FILE", FileURL: "https://example.test/" + fileName, FileName: fileName}, biz.ParsedResult{RowCount: 1, ValidRowCount: 1, Payload: json.RawMessage(`{}`), ItemResults: []biz.TaskItemResult{answer}})
		if err != nil {
			t.Fatal(err)
		}
	}
	save(1, "new.xlsx", strings.Repeat("好", 20))
	task, ready, err := repo.CompleteResultImport(ctx, taskID, true)
	if err != nil {
		t.Fatal(err)
	}
	if task.RiskCount != 1 || task.InvalidAnswerCount != 0 || ready {
		t.Fatalf("partial overwrite erased another round's risk: %+v", task)
	}
	// Even direct repository calls cannot reimport a successful file.
	save(1, "new.xlsx", "短")
	task, _, err = repo.CompleteResultImport(ctx, taskID, true)
	if err != nil || task.RiskCount != 1 || task.InvalidAnswerCount != 0 {
		t.Fatalf("old file reimported: %+v %v", task, err)
	}
	if err = repo.UpsertPendingResultFiles(ctx, taskID, []biz.ResultFileInput{{SourceType: "PROVIDER_FILE", FileURL: "https://example.test/older-retry.xlsx"}}); err != nil {
		t.Fatal(err)
	}
	save(2, "newer.xlsx", strings.Repeat("好", 20))
	save(2, "older-retry.xlsx", "旧的短回答")
	task, ready, err = repo.CompleteResultImport(ctx, taskID, true)
	if err != nil {
		t.Fatal(err)
	}
	if task.RiskCount != 0 || !ready || task.Status != biz.TaskStatusCallbackPending {
		t.Fatalf("risk recovery=%+v ready=%v", task, ready)
	}
	// Registering a new unfinished file invalidates the callback gate.
	if err = repo.UpsertPendingResultFiles(ctx, taskID, []biz.ResultFileInput{{SourceType: "PROVIDER_FILE", FileURL: "https://example.test/pending.xlsx"}}); err != nil {
		t.Fatal(err)
	}
	task, ready, err = repo.CompleteResultImport(ctx, taskID, true)
	if err != nil || ready || task.RiskStatsUpdatedAt != nil {
		t.Fatalf("pending file bypassed completion gate: %+v %v", task, err)
	}
	if _, err = db.Exec(ctx, `update cdp_tasks set status='SUBMITTED' where id=$1`, taskID); err != nil {
		t.Fatal(err)
	}
	candidates, err := repo.ListTasksWithPendingResultFiles(ctx, 10)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("unfinished provider task selected for completion: %+v %v", candidates, err)
	}

}

func TestRiskPolicyDoesNotSkipLegacyInitialization(t *testing.T) {
	db := openQueryOptimizationTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := db.Exec(ctx, `update cdp_result_validation_rules set risk_callback_allowed_provider_codes='["local"]',version=version+1 where id=1`); err != nil {
		t.Fatal(err)
	}
	var taskID, itemID int64
	if err := db.QueryRow(ctx, `insert into cdp_tasks(batch_no,provider_code,status,raw_request,biz_date) values('disabled-legacy','local','RESULT_IMPORTED','{}',current_date) returning id`).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `insert into cdp_task_items(task_id,item_id,platform_code,seq,question,result_status,raw_item) values($1,'item','local',1,'q','IMPORTED','{}') returning id`, taskID).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	var fileID int64
	if err := db.QueryRow(ctx, `insert into cdp_task_result_files(task_id,batch_no,external_batch_id,source_type,file_url,parse_status) values($1,'disabled-legacy','external','PROVIDER_FILE','http://localhost/legacy.xlsx','SUCCESS') returning id`, taskID).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `insert into cdp_task_item_results(task_id,task_item_id,batch_no,item_id,seq,cleaned_payload,parse_status,answer_text,validation_status,risk_items,result_file_id) values($1,$2,'disabled-legacy','item',1,'{}','SUCCESS','短','UNKNOWN','[]',$3),($1,$2,'disabled-legacy','item',2,'{}','SUCCESS','短','INVALID','[{"code":"ANSWER_TOO_SHORT"}]',$3)`, taskID, itemID, fileID); err != nil {
		t.Fatal(err)
	}
	repo := NewTaskRepo(db)
	cache := biz.NewValidationRuleCache(repo, time.Minute)
	if err := cache.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	usecase := biz.NewTaskUsecase(repo, nil, nil, nil, nil, nil, biz.TaskUsecaseOptions{ValidationRules: cache})
	if err := usecase.InitializeTaskValidation(ctx, taskID); err != nil {
		t.Fatal(err)
	}
	items, err := repo.ListTaskItemResults(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items=%d", len(items))
	}
	for _, item := range items {
		if item.Seq == 1 && (item.Status != biz.ValidationInvalid || len(item.RiskItems) != 2 || item.RuleVersion == "") {
			t.Fatalf("legacy skipped=%+v", item)
		}
		if item.Seq == 2 && item.Status != biz.ValidationInvalid {
			t.Fatalf("existing risk overwritten: %+v", item)
		}
	}
	task, err := repo.GetTask(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.RiskCount != 2 || task.InvalidAnswerCount != 2 || task.RiskStatsUpdatedAt == nil {
		t.Fatalf("aggregate=%+v", task)
	}
}
