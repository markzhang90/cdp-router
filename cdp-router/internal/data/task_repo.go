package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/GitMarkZhang/cdp-router/internal/pkg/timezone"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type TaskRepo struct {
	db *pgxpool.Pool
}

type taskDispatchDecision struct {
	ProfileID          int64
	ProfileCode        string
	Strategy           biz.DispatchStrategy
	ProviderCode       string
	CursorBefore       int64
	CursorAfter        int64
	CandidateProviders []string
	MatchedRuleCode    string
	MatchedRuleName    string
	Labels             []string
	QuestionCount      int
	QuotaDate          time.Time
	QuotaSnapshots     []dispatchQuotaSnapshot
	Reason             string
}

type dispatchQuotaSnapshot struct {
	ProviderCode string  `json:"providerCode"`
	DailyQuota   int     `json:"dailyQuota"`
	Used         int     `json:"used"`
	Remaining    int     `json:"remaining"`
	Score        float64 `json:"score"`
	Unlimited    bool    `json:"unlimited"`
}

type dispatchProviderSettings struct {
	DailyQuestionQuota int
}

type pullDispatchConfig struct {
	dispatchProfileConfig
	Strategy         string `json:"strategy"`
	Enabled          bool   `json:"enabled"`
	Cursor           int64  `json:"cursor"`
	LastProviderCode string `json:"lastProviderCode"`
}

type dispatchProfileConfig struct {
	Pull      *pullDispatchConfig `json:"pull,omitempty"`
	Providers []struct {
		Code string `json:"code"`
	} `json:"providers"`
	FallbackSelectionPolicy string               `json:"fallbackSelectionPolicy"`
	Rules                   []dispatchRuleConfig `json:"rules"`
}

type dispatchRuleConfig struct {
	Code          string   `json:"code"`
	Name          string   `json:"name"`
	SortOrder     int      `json:"sortOrder"`
	TaskNameRegex string   `json:"taskNameRegex"`
	Labels        []string `json:"labels"`
	ProviderCodes []string `json:"providerCodes"`
	Enabled       *bool    `json:"enabled"`
}

func NewTaskRepo(db *pgxpool.Pool) *TaskRepo {
	return &TaskRepo{db: db}
}

func (r *TaskRepo) CreateTask(ctx context.Context, input biz.CreateTaskInput, providerSettings []biz.ProviderSettings, providerCallbackBaseURL string) (*biz.Task, error) {
	if input.ExecutionMode == "" {
		input.ExecutionMode = "push"
	}
	if input.ExecutionMode != "push" && input.ExecutionMode != "pull" {
		return nil, biz.ErrInvalidTaskRequest
	}
	bizDate, err := time.Parse("2006-01-02", input.BizDate)
	if err != nil {
		return nil, fmt.Errorf("%w: bizDate must be yyyy-MM-dd", biz.ErrInvalidTaskRequest)
	}
	source := input.Source
	if source == "" {
		source = input.GatewayCode
	}
	taskName := strings.TrimSpace(input.TaskName)
	questionCount := dispatchQuestionCount(input.Items)
	if questionCount <= 0 {
		return nil, biz.ErrInvalidTaskRequest
	}
	tx, err := r.dbFor(ctx).Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	existing, err := r.scanTask(tx.QueryRow(ctx, taskSelectSQL()+` where batch_no=$1 and deleted_at is null`, input.BatchNo))
	if err == nil {
		device := input.Device
		if device == "" {
			device = "web"
		}
		if existing.PlatformUserID != input.PlatformUserID || existing.Device != device || existing.ExecutionMode != input.ExecutionMode {
			return nil, biz.ErrPullConflict
		}

		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	if input.Device == "" {
		input.Device = "web"
	}
	providerSettings = biz.FilterProviders(providerSettings, input.Device, input.Items)
	decision, err := r.selectDispatchProvider(ctx, tx, input.PlatformUserID, taskName, questionCount, providerSettings, input.ExecutionMode)
	if err != nil {
		return nil, err
	}
	providerCallbackURL := strings.TrimRight(providerCallbackBaseURL, "/") + "/external/callback/" + decision.ProviderCode

	var task biz.Task
	err = tx.QueryRow(ctx, `
	insert into cdp_tasks (
	  job_id, platform_user_id, platform_code, batch_no, task_name, provider_code, gateway_code, source, external_batch_id,
	  provider_external_batch_id, dispatch_profile_id, dispatch_profile_code, dispatch_strategy,
	  biz_date, quota_date, status, callback_url, provider_callback_url, raw_request, total_count
	) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
	returning id, coalesce(job_id,''), coalesce(platform_user_id,0), coalesce(platform_code,''), batch_no, coalesce(task_name,''), provider_code,
	  coalesce(gateway_code,''), coalesce(source,''), coalesce(external_batch_id,''), coalesce(provider_external_batch_id,''),
	  coalesce(reassigned_from_task_id,0), coalesce(callback_batch_no,''), coalesce(callback_external_batch_id,''),
	  coalesce(dispatch_profile_id,0), coalesce(dispatch_profile_code,''), coalesce(dispatch_strategy,''), biz_date, coalesce(quota_date,biz_date), status,
	  coalesce(callback_url,''), coalesce(provider_callback_url,''), raw_request, coalesce(provider_request,'{}'::jsonb),
	  coalesce(provider_response,'{}'::jsonb), coalesce(result_payload,'{}'::jsonb), coalesce(result_object_key,''),
	  coalesce(result_file_name,''), result_row_count, total_count, success_count, failed_count, coalesce(last_error,''), created_at, updated_at
		`, input.JobID, input.PlatformUserID, input.PlatformCode, input.BatchNo, taskName, decision.ProviderCode, input.GatewayCode, source, input.JobID,
		nil, decision.ProfileID, decision.ProfileCode, decision.Strategy,
		bizDate, decision.QuotaDate, biz.TaskStatusCreated, input.CallbackURL, providerCallbackURL, input.RawRequest, questionCount).
		Scan(&task.ID, &task.JobID, &task.PlatformUserID, &task.PlatformCode, &task.BatchNo, &task.TaskName, &task.ProviderCode,
			&task.GatewayCode, &task.Source, &task.ExternalBatchID, &task.ProviderExternalBatchID,
			&task.ReassignedFromTaskID, &task.CallbackBatchNo, &task.CallbackExternalBatchID,
			&task.DispatchProfileID, &task.DispatchProfileCode, &task.DispatchStrategy, &task.BizDate, &task.QuotaDate, &task.Status,
			&task.CallbackURL, &task.ProviderCallbackURL, &task.RawRequest, &task.ProviderRequest, &task.ProviderResponse,
			&task.ResultPayload, &task.ResultObjectKey, &task.ResultFileName, &task.ResultRowCount, &task.TotalCount, &task.SuccessCount,
			&task.FailedCount, &task.LastError, &task.CreatedAt, &task.UpdatedAt)
	if err != nil {
		return nil, err
	}

	for _, item := range input.Items {
		raw := item.RawItem
		if len(raw) == 0 {
			raw, _ = json.Marshal(item)
		}
		_, err := tx.Exec(ctx, `
insert into cdp_task_items (task_id, item_id, platform_code, seq, question, deepthink, web_search, raw_item, updated_at)
values ($1,$2,$3,$4,$5,$6,$7,$8,now())
on conflict (task_id, item_id) do nothing
`, task.ID, item.ItemID, item.PlatformCode, item.Seq, item.Question, item.Deepthink, item.WebSearch, raw)
		if err != nil {
			return nil, err
		}
	}
	task.Device, task.ExecutionMode = input.Device, input.ExecutionMode
	if task.ExecutionMode == "pull" {
		task.Status = biz.TaskStatusSubmitted
	}
	if _, err := tx.Exec(ctx, `update cdp_tasks set device=$2,execution_mode=$3,status=$4,submitted_at=case when $3::varchar='pull' then now() else submitted_at end where id=$1`, task.ID, task.Device, task.ExecutionMode, task.Status); err != nil {
		return nil, err
	}
	dispatchPayload := taskDispatchPayload(task, decision)
	if decision.MatchedRuleCode != "" {
		if _, err := tx.Exec(ctx, `insert into cdp_task_events (task_id,event_type,payload) values ($1,$2,$3)`, task.ID, biz.EventTaskDispatchRuleHit, dispatchPayload); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `insert into cdp_task_events (task_id,event_type,payload) values ($1,$2,$3)`, task.ID, biz.EventTaskDispatchQuota, dispatchPayload); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `insert into cdp_task_events (task_id,event_type,payload) values ($1,$2,$3)`, task.ID, biz.EventTaskDispatched, dispatchPayload); err != nil {
		return nil, err
	}
	eventPayload, _ := json.Marshal(map[string]any{"batchNo": task.BatchNo, "providerCode": task.ProviderCode})
	if _, err := tx.Exec(ctx, `insert into cdp_task_events (task_id,event_type,payload) values ($1,$2,$3)`, task.ID, biz.EventTaskCreated, eventPayload); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx, `
update cdp_tasks
set total_count=(select count(*)::int from cdp_task_items where task_id=$1),
  updated_at=now()
where id=$1
returning total_count
`, task.ID).Scan(&task.TotalCount); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &task, nil
}

func (r *TaskRepo) CreateReassignedTask(ctx context.Context, input biz.ReassignTaskRecordInput, providerCallbackBaseURL string) (*biz.Task, error) {
	if strings.TrimSpace(input.SourceBatchNo) == "" || strings.TrimSpace(input.TargetBatchNo) == "" || strings.TrimSpace(input.ProviderCode) == "" {
		return nil, biz.ErrInvalidTaskRequest
	}
	tx, err := r.dbFor(ctx).Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	source, err := r.scanTask(tx.QueryRow(ctx, taskSelectSQL()+` where batch_no=$1 and deleted_at is null for update`, input.SourceBatchNo))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, biz.ErrTaskNotFound
	}
	if err != nil {
		return nil, err
	}
	if source.Status == biz.TaskStatusCallbackPending || source.Status == biz.TaskStatusDone || source.Status == biz.TaskStatusReassigned {
		return nil, biz.ErrTaskClosed
	}
	if source.ReassignedFromTaskID > 0 {
		return nil, biz.ErrTaskClosed
	}
	var existingTargetID int64
	err = tx.QueryRow(ctx, `
select id
from cdp_tasks
where reassigned_from_task_id=$1
  and deleted_at is null
  and status <> $2
order by created_at desc, id desc
limit 1
	`, source.ID, biz.TaskStatusFailed).Scan(&existingTargetID)
	if err == nil {
		return nil, biz.ErrTaskClosed
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if source.ProviderCode == input.ProviderCode {
		return nil, fmt.Errorf("%w: target provider must differ from source provider", biz.ErrInvalidTaskRequest)
	}
	if input.TargetJobID == "" {
		input.TargetJobID = "job_" + input.TargetBatchNo
	}
	callbackBatchNo := reassignCallbackBatchNo(source)
	callbackExternalBatchID := reassignCallbackExternalBatchID(source)
	providerCallbackURL := strings.TrimRight(providerCallbackBaseURL, "/") + "/external/callback/" + input.ProviderCode
	var target biz.Task
	err = tx.QueryRow(ctx, `
insert into cdp_tasks (
  job_id, platform_user_id, platform_code, batch_no, task_name, provider_code, gateway_code, source, external_batch_id,
  provider_external_batch_id, reassigned_from_task_id, callback_batch_no, callback_external_batch_id,
  dispatch_profile_id, dispatch_profile_code, dispatch_strategy, biz_date, quota_date, status,
  callback_url, provider_callback_url, raw_request, total_count
) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)
returning id, coalesce(job_id,''), coalesce(platform_user_id,0), coalesce(platform_code,''), batch_no, coalesce(task_name,''), provider_code,
  coalesce(gateway_code,''), coalesce(source,''), coalesce(external_batch_id,''), coalesce(provider_external_batch_id,''),
  coalesce(reassigned_from_task_id,0), coalesce(callback_batch_no,''), coalesce(callback_external_batch_id,''),
  coalesce(dispatch_profile_id,0), coalesce(dispatch_profile_code,''), coalesce(dispatch_strategy,''), biz_date, coalesce(quota_date,biz_date), status,
  coalesce(callback_url,''), coalesce(provider_callback_url,''), raw_request, coalesce(provider_request,'{}'::jsonb),
  coalesce(provider_response,'{}'::jsonb), coalesce(result_payload,'{}'::jsonb), coalesce(result_object_key,''),
  coalesce(result_file_name,''), result_row_count, total_count, success_count, failed_count, coalesce(last_error,''), created_at, updated_at
`, input.TargetJobID, source.PlatformUserID, source.PlatformCode, input.TargetBatchNo, source.TaskName, input.ProviderCode,
		source.GatewayCode, source.Source, input.TargetJobID, nil, source.ID, callbackBatchNo, callbackExternalBatchID,
		source.DispatchProfileID, source.DispatchProfileCode, source.DispatchStrategy, source.BizDate, currentQuotaDate(), biz.TaskStatusCreated,
		source.CallbackURL, providerCallbackURL, source.RawRequest, source.TotalCount).
		Scan(&target.ID, &target.JobID, &target.PlatformUserID, &target.PlatformCode, &target.BatchNo, &target.TaskName, &target.ProviderCode,
			&target.GatewayCode, &target.Source, &target.ExternalBatchID, &target.ProviderExternalBatchID,
			&target.ReassignedFromTaskID, &target.CallbackBatchNo, &target.CallbackExternalBatchID,
			&target.DispatchProfileID, &target.DispatchProfileCode, &target.DispatchStrategy, &target.BizDate, &target.QuotaDate, &target.Status,
			&target.CallbackURL, &target.ProviderCallbackURL, &target.RawRequest, &target.ProviderRequest, &target.ProviderResponse,
			&target.ResultPayload, &target.ResultObjectKey, &target.ResultFileName, &target.ResultRowCount, &target.TotalCount, &target.SuccessCount,
			&target.FailedCount, &target.LastError, &target.CreatedAt, &target.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
insert into cdp_task_items (task_id, item_id, platform_code, seq, question, deepthink, web_search, raw_item, question_id, status, result_status, updated_at)
select $1, item_id, platform_code, seq, question, deepthink, web_search, raw_item, null, 0, 'PENDING', now()
from cdp_task_items
where task_id=$2
on conflict (task_id, item_id) do nothing
`, target.ID, source.ID); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx, `
update cdp_tasks
set total_count=(select count(*)::int from cdp_task_items where task_id=$1),
  updated_at=now()
where id=$1
returning total_count
`, target.ID).Scan(&target.TotalCount); err != nil {
		return nil, err
	}
	payload := map[string]any{
		"sourceTaskId":            source.ID,
		"targetTaskId":            target.ID,
		"targetBatchNo":           target.BatchNo,
		"originalBatchNo":         callbackBatchNo,
		"sourceProviderCode":      source.ProviderCode,
		"targetProviderCode":      target.ProviderCode,
		"callbackExternalBatchId": target.CallbackExternalBatchID,
		"operatorUserId":          input.OperatorUserID,
		"reason":                  input.Reason,
	}
	payloadBody, _ := json.Marshal(payload)
	if _, err := tx.Exec(ctx, `insert into cdp_task_events (task_id,event_type,payload) values ($1,$2,$3)`, source.ID, biz.EventTaskReassignRequested, payloadBody); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `insert into cdp_task_events (task_id,event_type,payload) values ($1,$2,$3)`, target.ID, biz.EventTaskReassignRequested, payloadBody); err != nil {
		return nil, err
	}
	target.Device = source.Device
	target.ExecutionMode = input.ExecutionMode
	if target.ExecutionMode == "" {
		target.ExecutionMode = "push"
	}
	if _, err := tx.Exec(ctx, `update cdp_tasks set device=$2,execution_mode=$3 where id=$1`, target.ID, target.Device, target.ExecutionMode); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &target, nil
}

func reassignCallbackBatchNo(source *biz.Task) string {
	if source == nil {
		return ""
	}
	if value := strings.TrimSpace(source.CallbackBatchNo); value != "" {
		return value
	}
	return source.BatchNo
}

func reassignCallbackExternalBatchID(source *biz.Task) string {
	if source == nil {
		return ""
	}
	if value := strings.TrimSpace(source.CallbackExternalBatchID); value != "" {
		return value
	}
	if value := strings.TrimSpace(source.JobID); value != "" {
		return value
	}
	return strings.TrimSpace(source.ExternalBatchID)
}

func (r *TaskRepo) MarkTaskReassigned(ctx context.Context, sourceTaskID, targetTaskID int64, payload any) error {
	tx, err := r.dbFor(ctx).Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	source, err := r.scanTask(tx.QueryRow(ctx, taskSelectSQL()+` where id=$1 and deleted_at is null for update`, sourceTaskID))
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.ErrTaskNotFound
	}
	if err != nil {
		return err
	}
	if source.Status == biz.TaskStatusCallbackPending || source.Status == biz.TaskStatusDone || source.Status == biz.TaskStatusReassigned {
		return biz.ErrTaskClosed
	}
	if _, err := tx.Exec(ctx, `update cdp_tasks set status=$2,updated_at=now() where id=$1`, sourceTaskID, biz.TaskStatusReassigned); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update cdp_tasks set status='SUBMITTED',submitted_at=now(),updated_at=now() where id=$1 and execution_mode='pull' and status='CREATED'`, targetTaskID); err != nil {
		return err
	}
	payloadBody, _ := json.Marshal(payload)
	if _, err := tx.Exec(ctx, `insert into cdp_task_events (task_id,event_type,payload) values ($1,$2,$3)`, sourceTaskID, biz.EventTaskReassigned, payloadBody); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `insert into cdp_task_events (task_id,event_type,payload) values ($1,$2,$3)`, targetTaskID, biz.EventTaskReassignSubmitted, payloadBody); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *TaskRepo) PrepareTaskForSubmit(ctx context.Context, taskID int64, providerSettings []biz.ProviderSettings, providerCallbackBaseURL string) (*biz.Task, error) {
	tx, err := r.dbFor(ctx).Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	task, err := r.scanTask(tx.QueryRow(ctx, taskSelectSQL()+` where id=$1 and deleted_at is null for update`, taskID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, biz.ErrTaskNotFound
	}
	if err != nil {
		return nil, err
	}
	if task.ReassignedFromTaskID > 0 || task.Status != biz.TaskStatusCreated || task.ProviderExternalBatchID != "" || sameQuotaDate(task.QuotaDate, currentQuotaDate()) {
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return task, nil
	}
	if task.TotalCount <= 0 {
		if err := tx.QueryRow(ctx, `select count(*)::int from cdp_task_items where task_id=$1`, task.ID).Scan(&task.TotalCount); err != nil {
			return nil, err
		}
	}
	rows, err := tx.Query(ctx, `select platform_code from cdp_task_items where task_id=$1`, task.ID)
	if err != nil {
		return nil, err
	}
	items := []biz.TaskItem{}
	for rows.Next() {
		var channel string
		if err = rows.Scan(&channel); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, biz.TaskItem{PlatformCode: channel})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	eligible := []biz.ProviderSettings{}
	for _, p := range biz.FilterProviders(providerSettings, task.Device, items) {
		mode := p.InteractionMode
		if mode == "" {
			mode = "push"
		}
		if mode == task.ExecutionMode {
			eligible = append(eligible, p)
		}
	}
	providerSettings = eligible
	decision, err := r.selectDispatchProvider(ctx, tx, task.PlatformUserID, task.TaskName, task.TotalCount, providerSettings, task.ExecutionMode)
	if err != nil {
		return nil, err
	}
	providerCallbackURL := strings.TrimRight(providerCallbackBaseURL, "/") + "/external/callback/" + decision.ProviderCode
	if _, err := tx.Exec(ctx, `
update cdp_tasks
set provider_code=$2,
  provider_callback_url=$3,
  dispatch_profile_id=$4,
  dispatch_profile_code=$5,
  dispatch_strategy=$6,
  quota_date=$7,
  updated_at=now()
where id=$1
`, task.ID, decision.ProviderCode, providerCallbackURL, decision.ProfileID, decision.ProfileCode, decision.Strategy, decision.QuotaDate); err != nil {
		return nil, err
	}
	task.ProviderCode = decision.ProviderCode
	task.ProviderCallbackURL = providerCallbackURL
	task.DispatchProfileID = decision.ProfileID
	task.DispatchProfileCode = decision.ProfileCode
	task.DispatchStrategy = decision.Strategy
	task.QuotaDate = decision.QuotaDate
	dispatchPayload := taskDispatchPayload(*task, decision)
	if decision.MatchedRuleCode != "" {
		if _, err := tx.Exec(ctx, `insert into cdp_task_events (task_id,event_type,payload) values ($1,$2,$3)`, task.ID, biz.EventTaskDispatchRuleHit, dispatchPayload); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `insert into cdp_task_events (task_id,event_type,payload) values ($1,$2,$3)`, task.ID, biz.EventTaskDispatchQuota, dispatchPayload); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `insert into cdp_task_events (task_id,event_type,payload) values ($1,$2,$3)`, task.ID, biz.EventTaskDispatched, dispatchPayload); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return r.GetTask(ctx, task.ID)
}

func (r *TaskRepo) selectDispatchProvider(ctx context.Context, tx pgx.Tx, platformUserID int64, taskName string, questionCount int, providers []biz.ProviderSettings, mode string) (taskDispatchDecision, error) {
	eligible := make([]biz.ProviderSettings, 0, len(providers))
	for _, p := range providers {
		m := p.InteractionMode
		if m == "" {
			m = "push"
		}
		if m == mode {
			eligible = append(eligible, p)
		}
	}
	available := dispatchProviderSettingsMap(eligible)
	var profileID int64
	var profileCode string
	var strategy string
	var enabled bool
	var cursor int64
	var config json.RawMessage
	err := tx.QueryRow(ctx, `
select id, code, strategy, enabled, cursor, config
from cdp_dispatch_profiles
where platform_user_id=$1
for update
`, platformUserID).Scan(&profileID, &profileCode, &strategy, &enabled, &cursor, &config)
	if errors.Is(err, pgx.ErrNoRows) {
		return taskDispatchDecision{}, biz.ErrProviderUnavailable
	}
	if err != nil {
		return taskDispatchDecision{}, err
	}
	parsed, err := parseDispatchProfileConfig(config)
	if err != nil {
		return taskDispatchDecision{}, biz.ErrProviderUnavailable
	}
	if mode == "pull" {
		if parsed.Pull == nil {
			return taskDispatchDecision{}, biz.ErrDispatchDisabled
		}
		strategy, enabled, cursor = parsed.Pull.Strategy, parsed.Pull.Enabled, parsed.Pull.Cursor
		parsed = parsed.Pull.dispatchProfileConfig
	}
	if !enabled {
		return taskDispatchDecision{}, biz.ErrDispatchDisabled
	}
	if strategy == "" {
		strategy = string(biz.DispatchStrategyRoundRobin)
	}
	if strategy != string(biz.DispatchStrategyRoundRobin) && strategy != string(biz.DispatchStrategyRuleBased) {
		return taskDispatchDecision{}, biz.ErrProviderUnavailable
	}
	fallbackCandidates := dispatchProfileProviderCodes(parsed, available)
	if len(fallbackCandidates) == 0 {
		return taskDispatchDecision{}, biz.ErrProviderUnavailable
	}
	quotaDate := currentQuotaDate()

	var decision taskDispatchDecision
	switch biz.DispatchStrategy(strategy) {
	case biz.DispatchStrategyRoundRobin:
		decision, err = r.selectRoundRobinDispatchProvider(ctx, tx, profileID, profileCode, cursor, fallbackCandidates, available, quotaDate, questionCount)
	case biz.DispatchStrategyRuleBased:
		decision, err = r.selectRuleBasedDispatchProvider(ctx, tx, profileID, profileCode, cursor, taskName, parsed, fallbackCandidates, available, quotaDate, questionCount)
	}
	if err != nil {
		return taskDispatchDecision{}, err
	}
	if mode == "pull" {
		_, err = tx.Exec(ctx, `update cdp_dispatch_profiles set config=jsonb_set(config,'{pull}',config->'pull' || jsonb_build_object('cursor',$2::bigint,'lastProviderCode',$3::text)),updated_at=now() where id=$1`, profileID, decision.CursorAfter, decision.ProviderCode)
	} else {
		_, err = tx.Exec(ctx, `update cdp_dispatch_profiles set cursor=$2,last_provider_code=$3,updated_at=now() where id=$1`, profileID, decision.CursorAfter, decision.ProviderCode)
	}
	return decision, err
}

func parseDispatchProfileConfig(config json.RawMessage) (dispatchProfileConfig, error) {
	var parsed dispatchProfileConfig
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(config, &parsed); err != nil {
		return dispatchProfileConfig{}, err
	}
	sort.SliceStable(parsed.Rules, func(i, j int) bool {
		if parsed.Rules[i].SortOrder == parsed.Rules[j].SortOrder {
			return i < j
		}
		return parsed.Rules[i].SortOrder < parsed.Rules[j].SortOrder
	})
	if parsed.Pull != nil {
		sort.SliceStable(parsed.Pull.Rules, func(i, j int) bool { return parsed.Pull.Rules[i].SortOrder < parsed.Pull.Rules[j].SortOrder })
	}
	return parsed, nil
}

func dispatchProviderSettingsMap(providers []biz.ProviderSettings) map[string]dispatchProviderSettings {
	available := make(map[string]dispatchProviderSettings, len(providers))
	for _, provider := range providers {
		code := strings.TrimSpace(provider.ProviderCode)
		if code == "" || !provider.Enabled {
			continue
		}
		available[code] = dispatchProviderSettings{
			DailyQuestionQuota: provider.DailyQuestionQuota,
		}
	}
	return available
}

func dispatchProfileProviderCodes(parsed dispatchProfileConfig, available map[string]dispatchProviderSettings) []string {
	candidates := make([]string, 0, len(parsed.Providers))
	seen := map[string]bool{}
	for _, provider := range parsed.Providers {
		code := strings.TrimSpace(provider.Code)
		if code == "" || seen[code] {
			continue
		}
		if _, ok := available[code]; !ok {
			continue
		}
		seen[code] = true
		candidates = append(candidates, code)
	}
	return candidates
}

func (r *TaskRepo) selectRoundRobinDispatchProvider(ctx context.Context, tx pgx.Tx, profileID int64, profileCode string, cursor int64, candidates []string, available map[string]dispatchProviderSettings, quotaDate time.Time, questionCount int) (taskDispatchDecision, error) {
	if cursor < 0 {
		cursor = 0
	}
	snapshots, err := r.dispatchQuotaSnapshots(ctx, tx, candidates, available, quotaDate, questionCount)
	if err != nil {
		return taskDispatchDecision{}, err
	}
	selected, nextCursor := selectRoundRobinProviderFromSnapshots(candidates, snapshots, cursor)
	if selected == "" {
		return taskDispatchDecision{}, biz.ErrProviderUnavailable
	}
	return taskDispatchDecision{
		ProfileID:          profileID,
		ProfileCode:        profileCode,
		Strategy:           biz.DispatchStrategyRoundRobin,
		ProviderCode:       selected,
		CursorBefore:       cursor,
		CursorAfter:        nextCursor,
		CandidateProviders: candidates,
		QuestionCount:      questionCount,
		QuotaDate:          quotaDate,
		QuotaSnapshots:     snapshots,
		Reason:             "round_robin",
	}, nil
}

func (r *TaskRepo) selectRuleBasedDispatchProvider(ctx context.Context, tx pgx.Tx, profileID int64, profileCode string, cursor int64, taskName string, parsed dispatchProfileConfig, fallbackCandidates []string, available map[string]dispatchProviderSettings, quotaDate time.Time, questionCount int) (taskDispatchDecision, error) {
	match, err := matchedDispatchRule(taskName, parsed.Rules, available)
	if err != nil {
		return taskDispatchDecision{}, err
	}
	fallbackPolicy := normalizeDispatchFallbackSelectionPolicy(parsed.FallbackSelectionPolicy)
	ruleCandidates := []string(nil)
	if match.Code != "" {
		ruleCandidates = cleanDispatchProviderCodes(match.ProviderCodes, available)
	}
	allCandidates := mergeDispatchProviderCodes(fallbackCandidates, ruleCandidates, available)
	allSnapshots, err := r.dispatchQuotaSnapshots(ctx, tx, allCandidates, available, quotaDate, questionCount)
	if err != nil {
		return taskDispatchDecision{}, err
	}
	fallbackSnapshots := filterDispatchQuotaSnapshots(allSnapshots, fallbackCandidates)
	candidates := fallbackCandidates
	snapshots := fallbackSnapshots
	reason := "lowest_quota_usage_rate"
	labels := cleanStringList(match.Labels)
	selected := ""
	nextCursor := cursor + 1
	if match.Code != "" {
		ruleSnapshots := filterDispatchQuotaSnapshots(allSnapshots, ruleCandidates)
		selected = selectLowestQuotaScoreProvider(ruleSnapshots, cursor)
		if selected != "" {
			candidates = ruleCandidates
			snapshots = ruleSnapshots
		} else {
			selected, nextCursor = selectFallbackProvider(fallbackCandidates, fallbackSnapshots, cursor, fallbackPolicy)
			reason = fallbackReason("rule_pool_fallback", fallbackPolicy)
			labels = []string{"fallback"}
		}
	} else {
		selected, nextCursor = selectFallbackProvider(fallbackCandidates, fallbackSnapshots, cursor, fallbackPolicy)
		reason = fallbackReason("fallback", fallbackPolicy)
	}
	if selected == "" {
		return taskDispatchDecision{}, biz.ErrProviderUnavailable
	}
	return taskDispatchDecision{
		ProfileID:          profileID,
		ProfileCode:        profileCode,
		Strategy:           biz.DispatchStrategyRuleBased,
		ProviderCode:       selected,
		CursorBefore:       cursor,
		CursorAfter:        nextCursor,
		CandidateProviders: candidates,
		MatchedRuleCode:    match.Code,
		MatchedRuleName:    match.Name,
		Labels:             labels,
		QuestionCount:      questionCount,
		QuotaDate:          quotaDate,
		QuotaSnapshots:     snapshots,
		Reason:             reason,
	}, nil
}

func matchedDispatchRule(taskName string, rules []dispatchRuleConfig, available map[string]dispatchProviderSettings) (dispatchRuleConfig, error) {
	for _, rule := range rules {
		if rule.Enabled != nil && !*rule.Enabled {
			continue
		}
		if strings.TrimSpace(rule.TaskNameRegex) == "" {
			continue
		}
		compiled, err := regexp.Compile(rule.TaskNameRegex)
		if err != nil {
			return dispatchRuleConfig{}, fmt.Errorf("%w: invalid dispatch rule regex %s: %v", biz.ErrInvalidTaskRequest, rule.Code, err)
		}
		if !compiled.MatchString(taskName) {
			continue
		}
		rule.ProviderCodes = cleanDispatchProviderCodes(rule.ProviderCodes, available)
		return rule, nil
	}
	return dispatchRuleConfig{}, nil
}

func normalizeDispatchFallbackSelectionPolicy(value string) biz.DispatchFallbackSelectionPolicy {
	switch biz.DispatchFallbackSelectionPolicy(strings.TrimSpace(strings.ToUpper(value))) {
	case biz.DispatchFallbackSelectionPolicyRoundRobin:
		return biz.DispatchFallbackSelectionPolicyRoundRobin
	case biz.DispatchFallbackSelectionPolicyLowestUsageRate:
		return biz.DispatchFallbackSelectionPolicyLowestUsageRate
	default:
		return biz.DispatchFallbackSelectionPolicyLowestUsageRate
	}
}

func mergeDispatchProviderCodes(a []string, b []string, available map[string]dispatchProviderSettings) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(a)+len(b))
	for _, values := range [][]string{a, b} {
		for _, code := range cleanDispatchProviderCodes(values, available) {
			if seen[code] {
				continue
			}
			seen[code] = true
			out = append(out, code)
		}
	}
	return out
}

func selectFallbackProvider(candidates []string, snapshots []dispatchQuotaSnapshot, cursor int64, policy biz.DispatchFallbackSelectionPolicy) (string, int64) {
	switch policy {
	case biz.DispatchFallbackSelectionPolicyRoundRobin:
		return selectRoundRobinProviderFromSnapshots(candidates, snapshots, cursor)
	default:
		return selectLowestQuotaScoreProvider(snapshots, cursor), cursor + 1
	}
}

func fallbackReason(prefix string, policy biz.DispatchFallbackSelectionPolicy) string {
	switch policy {
	case biz.DispatchFallbackSelectionPolicyRoundRobin:
		return prefix + "_round_robin"
	default:
		return prefix + "_lowest_quota_usage_rate"
	}
}

func cleanDispatchProviderCodes(values []string, available map[string]dispatchProviderSettings) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		code := strings.TrimSpace(value)
		if code == "" || seen[code] {
			continue
		}
		if _, ok := available[code]; !ok {
			continue
		}
		seen[code] = true
		out = append(out, code)
	}
	return out
}

func filterDispatchQuotaSnapshots(snapshots []dispatchQuotaSnapshot, candidates []string) []dispatchQuotaSnapshot {
	allowed := make(map[string]bool, len(candidates))
	for _, code := range candidates {
		allowed[code] = true
	}
	out := make([]dispatchQuotaSnapshot, 0, len(candidates))
	for _, snapshot := range snapshots {
		if allowed[snapshot.ProviderCode] {
			out = append(out, snapshot)
		}
	}
	return out
}

func cleanStringList(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		item := strings.TrimSpace(value)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

func eligibleQuotaProviders(snapshots []dispatchQuotaSnapshot) map[string]bool {
	out := map[string]bool{}
	for _, snapshot := range snapshots {
		if snapshot.Unlimited || snapshot.Remaining >= 0 {
			out[snapshot.ProviderCode] = true
		}
	}
	return out
}

func selectRoundRobinProviderFromSnapshots(candidates []string, snapshots []dispatchQuotaSnapshot, cursor int64) (string, int64) {
	if cursor < 0 {
		cursor = 0
	}
	eligible := eligibleQuotaProviders(snapshots)
	if len(eligible) == 0 || len(candidates) == 0 {
		return "", cursor
	}
	for offset := int64(0); offset < int64(len(candidates)); offset++ {
		code := candidates[int((cursor+offset)%int64(len(candidates)))]
		if eligible[code] {
			return code, cursor + offset + 1
		}
	}
	return "", cursor
}

func selectLowestQuotaScoreProvider(snapshots []dispatchQuotaSnapshot, cursor int64) string {
	eligible := make([]dispatchQuotaSnapshot, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if snapshot.Unlimited || snapshot.Remaining >= 0 {
			eligible = append(eligible, snapshot)
		}
	}
	if len(eligible) == 0 {
		return ""
	}
	if cursor < 0 {
		cursor = 0
	}
	order := make(map[string]int64, len(eligible))
	for i, snapshot := range eligible {
		rotated := (int64(i) - cursor) % int64(len(eligible))
		if rotated < 0 {
			rotated += int64(len(eligible))
		}
		order[snapshot.ProviderCode] = rotated
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		if math.Abs(eligible[i].Score-eligible[j].Score) > 0.000001 {
			return eligible[i].Score < eligible[j].Score
		}
		rotatedI := order[eligible[i].ProviderCode]
		rotatedJ := order[eligible[j].ProviderCode]
		if rotatedI == rotatedJ {
			return eligible[i].ProviderCode < eligible[j].ProviderCode
		}
		return rotatedI < rotatedJ
	})
	return eligible[0].ProviderCode
}

func (r *TaskRepo) dispatchQuotaSnapshots(ctx context.Context, tx pgx.Tx, candidates []string, available map[string]dispatchProviderSettings, quotaDate time.Time, questionCount int) ([]dispatchQuotaSnapshot, error) {
	candidates = cleanDispatchProviderCodes(candidates, available)
	sort.Strings(candidates)
	for _, code := range candidates {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext($1), hashtext($2))`, code, quotaDate.Format("2006-01-02")); err != nil {
			return nil, err
		}
	}
	usedByProvider, err := r.providerQuotaUsage(ctx, tx, candidates, quotaDate)
	if err != nil {
		return nil, err
	}
	maxQuota := 0
	for _, code := range candidates {
		quota := available[code].DailyQuestionQuota
		if quota > maxQuota {
			maxQuota = quota
		}
	}
	if maxQuota <= 0 {
		maxQuota = 1
	}
	snapshots := make([]dispatchQuotaSnapshot, 0, len(candidates))
	for _, code := range candidates {
		quota := available[code].DailyQuestionQuota
		used := usedByProvider[code]
		unlimited := quota <= 0
		remaining := quota - used - questionCount
		scoreBase := quota
		if scoreBase <= 0 {
			scoreBase = maxQuota
		}
		snapshots = append(snapshots, dispatchQuotaSnapshot{
			ProviderCode: code,
			DailyQuota:   quota,
			Used:         used,
			Remaining:    remaining,
			Score:        float64(used) / float64(scoreBase),
			Unlimited:    unlimited,
		})
	}
	return snapshots, nil
}

func (r *TaskRepo) providerQuotaUsage(ctx context.Context, tx pgx.Tx, candidates []string, quotaDate time.Time) (map[string]int, error) {
	usage := make(map[string]int, len(candidates))
	for _, code := range candidates {
		usage[code] = 0
	}
	if len(candidates) == 0 {
		return usage, nil
	}
	rows, err := tx.Query(ctx, `
select provider_code, coalesce(sum(total_count), 0)::int
from cdp_tasks
where provider_code = any($1::text[])
  and quota_date=$2
  and deleted_at is null
  and (
    status = any($3::text[])
    or (status='FAILED' and provider_external_batch_id is not null)
  )
group by provider_code
`, candidates, quotaDate, []string{
		string(biz.TaskStatusCreated),
		string(biz.TaskStatusSubmitted),
		string(biz.TaskStatusProviderDone),
		string(biz.TaskStatusResultImported),
		string(biz.TaskStatusCallbackPending),
		string(biz.TaskStatusDone),
	})
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var code string
		var used int
		if err := rows.Scan(&code, &used); err != nil {
			return nil, err
		}
		usage[code] = used
	}
	return usage, rows.Err()
}

func currentQuotaDate() time.Time {
	now := time.Now().In(timezone.AdminLocation())
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, timezone.AdminLocation())
}

func sameQuotaDate(a time.Time, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return false
	}
	return a.In(timezone.AdminLocation()).Format("2006-01-02") == b.In(timezone.AdminLocation()).Format("2006-01-02")
}

func taskDispatchPayload(task biz.Task, decision taskDispatchDecision) json.RawMessage {
	payload, _ := json.Marshal(map[string]any{
		"executionMode":        task.ExecutionMode,
		"profileId":            decision.ProfileID,
		"profileCode":          decision.ProfileCode,
		"strategy":             decision.Strategy,
		"taskName":             task.TaskName,
		"matchedRuleCode":      decision.MatchedRuleCode,
		"matchedRuleName":      decision.MatchedRuleName,
		"labels":               decision.Labels,
		"providerCode":         decision.ProviderCode,
		"selectedProviderCode": decision.ProviderCode,
		"cursorBefore":         decision.CursorBefore,
		"cursorAfter":          decision.CursorAfter,
		"candidateProviders":   decision.CandidateProviders,
		"quotaDate":            decision.QuotaDate.Format("2006-01-02"),
		"questionCount":        decision.QuestionCount,
		"quotaSnapshots":       decision.QuotaSnapshots,
		"reason":               decision.Reason,
	})
	return payload
}

func (r *TaskRepo) GetTask(ctx context.Context, id int64) (*biz.Task, error) {
	task, err := r.scanTask(r.dbFor(ctx).QueryRow(ctx, taskSelectSQL()+` where id=$1 and deleted_at is null`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, biz.ErrTaskNotFound
	}
	return task, err
}

// GetTaskResultPayload loads the retained full result only when explicitly needed.
// Normal task reads intentionally leave Task.ResultPayload unset.
func (r *TaskRepo) GetTaskResultPayload(ctx context.Context, id int64) (json.RawMessage, error) {
	var payload json.RawMessage
	err := r.dbFor(ctx).QueryRow(ctx, `select coalesce(result_payload, '{}'::jsonb)
from cdp_tasks where id=$1 and deleted_at is null`, id).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, biz.ErrTaskNotFound
	}
	return payload, err
}

func (r *TaskRepo) GetTaskByJobID(ctx context.Context, jobID string, platformUserID int64) (*biz.Task, error) {
	task, err := r.scanTask(r.dbFor(ctx).QueryRow(ctx, taskSelectSQL()+` where job_id=$1 and platform_user_id=$2 and deleted_at is null`, jobID, platformUserID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, biz.ErrTaskNotFound
	}
	if err != nil {
		return nil, err
	}
	if task.Status == biz.TaskStatusReassigned {
		reassigned, reassignErr := r.scanTask(r.dbFor(ctx).QueryRow(ctx, taskSelectSQL()+`
where reassigned_from_task_id=$1
  and platform_user_id=$2
  and deleted_at is null
order by created_at desc, id desc
limit 1
`, task.ID, platformUserID))
		if reassignErr == nil {
			return reassigned, nil
		}
		if !errors.Is(reassignErr, pgx.ErrNoRows) {
			return nil, reassignErr
		}
	}
	return task, err
}

func (r *TaskRepo) GetTaskByBatchNo(ctx context.Context, batchNo string) (*biz.Task, error) {
	task, err := r.scanTask(r.dbFor(ctx).QueryRow(ctx, taskSelectSQL()+` where batch_no=$1 and deleted_at is null`, batchNo))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, biz.ErrTaskNotFound
	}
	return task, err
}

func (r *TaskRepo) ResolveProviderTask(ctx context.Context, providerCode, batchNo string) (*biz.Task, error) {
	task, err := r.scanTask(r.dbFor(ctx).QueryRow(ctx, taskSelectSQL()+`
where id=(
  select child.id
  from cdp_tasks child
  join cdp_tasks source on source.id=child.reassigned_from_task_id
  where child.provider_code=$1
    and child.callback_batch_no=$2
    and source.batch_no=$2
    and child.deleted_at is null
    and source.deleted_at is null
  order by child.created_at desc, child.id desc
  limit 1
)
`, providerCode, batchNo))
	if err == nil {
		if task.ExecutionMode == "pull" {
			return nil, biz.ErrInvalidTaskRequest
		}
		return task, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	task, err = r.scanTask(r.dbFor(ctx).QueryRow(ctx, taskSelectSQL()+` where provider_code=$1 and batch_no=$2 and deleted_at is null`, providerCode, batchNo))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, biz.ErrTaskNotFound
	}
	if err != nil {
		return nil, err
	}
	if task.Status == biz.TaskStatusReassigned {
		return nil, biz.ErrTaskClosed
	}
	if task.ExecutionMode == "pull" {
		return nil, biz.ErrInvalidTaskRequest
	}
	return task, nil
}

func (r *TaskRepo) ListTasksByStatus(ctx context.Context, status biz.TaskStatus, limit int) ([]*biz.Task, error) {
	rows, err := r.dbFor(ctx).Query(ctx, taskSelectSQL()+` where status=$1 and (execution_mode='push' or $1='CALLBACK_PENDING') and deleted_at is null and (execution_mode='push' or not exists(select 1 from cdp_task_result_submissions s where s.task_id=cdp_tasks.id and s.status not in ('APPLIED','SUPERSEDED'))) and ($1<>'CALLBACK_PENDING' or ((risk_count=0 or provider_code=any($3::text[])) and risk_stats_updated_at is not null)) order by created_at asc limit $2`, status, limit, biz.RiskCallbackProviderCodes(ctx))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []*biz.Task
	for rows.Next() {
		task, err := r.scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (r *TaskRepo) ListResultImportedTasksWithProviderCallback(ctx context.Context, limit int) ([]*biz.Task, error) {
	rows, err := r.dbFor(ctx).Query(ctx, taskSelectSQL()+`
where status=$1 and execution_mode='push'
  and (risk_count=0 or provider_code=any($3::text[])) and risk_stats_updated_at is not null
  and coalesce(provider_response, '{}'::jsonb) ? 'providerCallback'
  and deleted_at is null
order by coalesce(result_imported_at, updated_at) asc, id asc
limit $2
`, biz.TaskStatusResultImported, limit, biz.RiskCallbackProviderCodes(ctx))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []*biz.Task
	for rows.Next() {
		task, err := r.scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (r *TaskRepo) ListSubmittedTasksForProgress(ctx context.Context, limit int, submittedBefore time.Time) ([]*biz.Task, error) {
	rows, err := r.dbFor(ctx).Query(ctx, taskSelectSQL()+`
where status=$1 and execution_mode='push'
  and provider_external_batch_id is not null
  and deleted_at is null
  and coalesce(submitted_at, created_at) <= $2
order by coalesce(submitted_at, created_at) asc, id asc
limit $3
`, biz.TaskStatusSubmitted, submittedBefore, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []*biz.Task
	for rows.Next() {
		task, err := r.scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (r *TaskRepo) CountTaskItems(ctx context.Context, taskID int64) (int, error) {
	var count int
	if err := r.dbFor(ctx).QueryRow(ctx, `select count(*)::int from cdp_task_items where task_id=$1`, taskID).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (r *TaskRepo) ListTaskItems(ctx context.Context, taskID int64) ([]biz.TaskItem, error) {
	rows, err := r.dbFor(ctx).Query(ctx, `
select item_id, platform_code, seq, question, deepthink, web_search, raw_item
from cdp_task_items
where task_id=$1
order by id asc
	`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []biz.TaskItem
	for rows.Next() {
		var item biz.TaskItem
		if err := rows.Scan(&item.ItemID, &item.PlatformCode, &item.Seq, &item.Question, &item.Deepthink, &item.WebSearch, &item.RawItem); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *TaskRepo) ListTaskItemResults(ctx context.Context, taskID int64) ([]biz.TaskItemResultView, error) {
	rows, err := r.dbFor(ctx).Query(ctx, `
with item_bounds as (
  select ti.*, greatest(coalesce(max(tir.seq), 0), 1) as expected_count
  from cdp_task_items ti
  left join cdp_task_item_results tir on tir.task_item_id=ti.id
  where ti.task_id=$1
  group by ti.id
),
expanded as (
  select ti.*, expected.seq as result_seq
  from item_bounds ti
  cross join lateral (
 select generate_series(1,ti.expected_count) seq where (select execution_mode from cdp_tasks where id=$1)='push'
 union all select r.seq from cdp_task_item_results r where r.task_item_id=ti.id and (select execution_mode from cdp_tasks where id=$1)='pull'
 ) as expected
)
select
	  ti.item_id,
	  ti.platform_code,
	  ti.result_seq,
	  ti.question,
	  ti.status,
	  case
	    when tir.parse_status='SUCCESS' then 'IMPORTED'
	    when tir.parse_status is not null then tir.parse_status
	    when ti.result_status='IMPORTED' then 'PENDING'
	    else ti.result_status
	  end,
	  coalesce(tir.answer_text,''),
	  coalesce(tir.thinking_process,''),
		  tir.generated_at,
	  coalesce(tir.generated_at_text,''),
	  coalesce(tir.recommended_questions,'[]'::jsonb),
	  coalesce(tir.device_id,''),
	  coalesce(tir.recording_url,''),
	  coalesce(tir.references_json,'[]'::jsonb),coalesce(tir.product_cards,'[]'::jsonb),
	  coalesce(tir.reference_count,0),
	  coalesce(tir.parse_status,''),
  coalesce(tir.parse_error,''),coalesce(tir.validation_status,'UNKNOWN'),coalesce(tir.risk_items,'[]'::jsonb),coalesce(tir.validation_rule_version,''),tir.validated_at
from expanded ti
left join cdp_task_item_results tir on tir.task_item_id=ti.id and tir.seq=ti.result_seq
order by ti.id asc, ti.result_seq asc
	`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []biz.TaskItemResultView
	for rows.Next() {
		var item biz.TaskItemResultView
		var generatedAt, validatedAt *time.Time
		var riskJSON []byte
		if err := rows.Scan(&item.ItemID, &item.PlatformCode, &item.Seq, &item.Question, &item.ProviderStatus,
			&item.ResultStatus, &item.AnswerText, &item.ThinkingProcess, &generatedAt, &item.GeneratedAtText,
			&item.RecommendedQuestions, &item.DeviceID, &item.RecordingURL, &item.ReferencesJSON, &item.ProductCards, &item.ReferenceCount,
			&item.ParseStatus, &item.ParseError, &item.AnswerValidation.Status, &riskJSON, &item.RuleVersion, &validatedAt); err != nil {
			return nil, err
		}
		if generatedAt != nil {
			item.GeneratedAt = *generatedAt
		}
		if err := json.Unmarshal(riskJSON, &item.RiskItems); err != nil {
			return nil, err
		}
		if validatedAt != nil {
			item.ValidatedAt = *validatedAt
		}
		results = append(results, item)
	}
	return results, rows.Err()
}

func (r *TaskRepo) GetCallbackReadiness(ctx context.Context, taskID int64) (biz.CallbackReadiness, error) {
	var readiness biz.CallbackReadiness
	var mode string
	if err := r.dbFor(ctx).QueryRow(ctx, `select execution_mode from cdp_tasks where id=$1`, taskID).Scan(&mode); err != nil {
		return readiness, err
	}
	if mode == "pull" {
		return (&PullRepo{tasks: r}).callbackReadiness(ctx, taskID)
	}
	var status string
	if err := r.dbFor(ctx).QueryRow(ctx, `
select t.status,
  coalesce(nullif(t.provider_response #>> '{providerCallback,totalCount}', '')::int, 0),
  stats.item_count, stats.imported_count, stats.result_success_count, stats.result_failed_count, t.risk_count,t.risk_stats_updated_at is not null
from cdp_tasks t
cross join lateral (
`+taskItemStatsSQL("t.id")+`
) stats
where t.id=$1 and t.deleted_at is null
	`, taskID).Scan(&status, &readiness.ProviderTotal, &readiness.TaskItemCount, &readiness.ImportedCount, &readiness.SuccessCount, &readiness.FailedCount, &readiness.RiskCount, &readiness.RiskStatsReady); err != nil {
		return biz.CallbackReadiness{}, err
	}
	readiness.TotalCount = readiness.TaskItemCount
	readiness.ProviderDone = status == string(biz.TaskStatusProviderDone) ||
		status == string(biz.TaskStatusResultImported) ||
		status == string(biz.TaskStatusCallbackPending) ||
		status == string(biz.TaskStatusDone) ||
		status == string(biz.TaskStatusFailed)
	readiness.Ready = readiness.ProviderDone &&
		readiness.TaskItemCount > 0 &&
		readiness.ImportedCount == readiness.TaskItemCount &&
		readiness.TotalCount > 0 &&
		readiness.SuccessCount+readiness.FailedCount == readiness.TotalCount &&
		(readiness.ProviderTotal == 0 || readiness.ProviderTotal == readiness.TaskItemCount)
	readiness.ForceRequired = readiness.ProviderDone && !readiness.Ready && readiness.SuccessCount > 0
	readiness.Reason = callbackReadinessReason(readiness)
	return readiness, nil
}

func (r *TaskRepo) ListTaskResultFiles(ctx context.Context, taskID int64) ([]biz.CallbackResultFile, error) {
	rows, err := r.dbFor(ctx).Query(ctx, `
select source_type, coalesce(object_key,''), coalesce(file_url,''), coalesce(file_name,'')
from cdp_task_result_files
where task_id=$1 and parse_status='SUCCESS'
order by created_at desc, id desc
`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []biz.CallbackResultFile
	for rows.Next() {
		var file biz.CallbackResultFile
		if err := rows.Scan(&file.SourceType, &file.ObjectKey, &file.FileURL, &file.FileName); err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

func (r *TaskRepo) ListUploadedResultFiles(ctx context.Context, taskID int64, includeImported bool) ([]biz.ResultFileInput, error) {
	rows, err := r.dbFor(ctx).Query(ctx, `
select source_type, coalesce(object_key,''), coalesce(file_name,''), coalesce(external_batch_id,''), parse_status
from cdp_task_result_files
where task_id=$1
  and object_key is not null
  and source_type in ('OSS_UPLOAD','OSS')
  and ($2 or parse_status <> 'SUCCESS')
order by created_at asc, id asc
`, taskID, includeImported)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []biz.ResultFileInput
	for rows.Next() {
		var file biz.ResultFileInput
		if err := rows.Scan(&file.SourceType, &file.ObjectKey, &file.FileName, &file.ExternalBatchID, &file.ParseStatus); err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

func (r *TaskRepo) ListPendingResultFiles(ctx context.Context, taskID int64) ([]biz.ResultFileInput, error) {
	rows, err := r.dbFor(ctx).Query(ctx, `
select source_type, coalesce(object_key,''), coalesce(file_url,''), coalesce(file_name,''), coalesce(external_batch_id,''), parse_status
from cdp_task_result_files
where task_id=$1
  and parse_status <> 'SUCCESS'
order by created_at asc, id asc
	`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []biz.ResultFileInput
	for rows.Next() {
		var file biz.ResultFileInput
		if err := rows.Scan(&file.SourceType, &file.ObjectKey, &file.FileURL, &file.FileName, &file.ExternalBatchID, &file.ParseStatus); err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

func (r *TaskRepo) CountSuccessfulResultFiles(ctx context.Context, taskID int64) (int, error) {
	var count int
	err := r.dbFor(ctx).QueryRow(ctx, `select count(*) from cdp_task_result_files where task_id=$1 and parse_status='SUCCESS'`, taskID).Scan(&count)
	return count, err
}

func (r *TaskRepo) MarkSubmitted(ctx context.Context, taskID int64, providerRequest json.RawMessage, result biz.SubmitResult) error {
	_, err := r.dbFor(ctx).Exec(ctx, `
update cdp_tasks
set status=$2,
  provider_request=$3,
  provider_response=$4,
  provider_external_batch_id=$5,
  total_count=(select count(*)::int from cdp_task_items where task_id=$1),
  quota_date=(now() at time zone 'Asia/Shanghai')::date,
  submitted_at=now(),
  updated_at=now()
where id=$1
`, taskID, biz.TaskStatusSubmitted, providerRequest, result.RawResponse, result.ExternalBatchID)
	return err
}

func (r *TaskRepo) MarkSubmitFailed(ctx context.Context, taskID int64, errMessage string) error {
	_, err := r.dbFor(ctx).Exec(ctx, `update cdp_tasks set status=$2,last_error=$3,updated_at=now() where id=$1`, taskID, biz.TaskStatusFailed, errMessage)
	return err
}

func (r *TaskRepo) FailSubmittedOlderThan(ctx context.Context, cutoff time.Time, errMessage string, limit int) (int, error) {
	rows, err := r.dbFor(ctx).Query(ctx, `
with picked as (
  select id
  from cdp_tasks
  where status=$1 and execution_mode='push'
    and deleted_at is null
    and coalesce(submitted_at, created_at) < $2
	  order by coalesce(submitted_at, created_at) asc, id asc
	  limit $3
),
updated as (
update cdp_tasks t
set status=$4,last_error=$5,updated_at=now()
from picked
where t.id=picked.id
returning t.id, t.batch_no, t.provider_code
),
events as (
  insert into cdp_task_events (task_id,event_type,payload)
  select id,$6,jsonb_build_object('batchNo',batch_no,'providerCode',provider_code,'reason',$5,'cutoff',$2::text)
  from updated
  returning task_id
)
select task_id from events
`, biz.TaskStatusSubmitted, cutoff, limit, biz.TaskStatusFailed, errMessage, biz.EventTaskTimedOut)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return count, err
		}
		count++
	}
	return count, rows.Err()
}

func (r *TaskRepo) SaveUploadObjectKey(ctx context.Context, taskID int64, key string, fileName string) error {
	if !taskLocked(ctx, taskID) {
		return r.WithTaskLock(ctx, taskID, func(ctx context.Context) error { return r.SaveUploadObjectKey(ctx, taskID, key, fileName) })
	}
	tx, err := r.dbFor(ctx).Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	task, err := r.scanTask(tx.QueryRow(ctx, taskSelectSQL()+` where id=$1 for update`, taskID))
	if err != nil {
		return err
	}
	batchNo := taskBatchNoForProvider(task)
	if _, err := tx.Exec(ctx, `update cdp_tasks set result_object_key=$2,result_file_name=$3,updated_at=now() where id=$1`, taskID, key, fileName); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
insert into cdp_task_result_files (
  task_id,batch_no,external_batch_id,source_type,object_key,file_name,parse_status
) values ($1,$2,$3,'OSS_UPLOAD',$4,$5,'PENDING')
on conflict (batch_no, object_key) where object_key is not null do update set
  task_id=excluded.task_id,
  external_batch_id=excluded.external_batch_id,
  source_type=excluded.source_type,
  file_name=excluded.file_name,
  updated_at=now()
`, taskID, batchNo, task.ProviderExternalBatchID, key, fileName)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update cdp_tasks set risk_stats_updated_at=null where id=$1 and risk_stats_updated_at is not null and exists(select 1 from cdp_task_result_files where task_id=$1 and parse_status<>'SUCCESS')`, taskID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *TaskRepo) MarkProviderCallback(ctx context.Context, input biz.ProviderCallbackInput) (*biz.Task, error) {
	task, err := r.getProviderCallbackTask(ctx, input)
	if err != nil {
		return nil, err
	}
	if task.ProviderCode != input.ProviderCode {
		return nil, biz.ErrProviderNotFound
	}
	if task.Status == biz.TaskStatusCallbackPending || task.Status == biz.TaskStatusDone {
		return task, nil
	}
	finishedAt := time.Now()
	if !input.FinishedAt.IsZero() {
		finishedAt = input.FinishedAt
	}
	rawRequest := input.RawRequest
	if len(rawRequest) == 0 {
		rawRequest = json.RawMessage(`{}`)
	}
	_, err = r.dbFor(ctx).Exec(ctx, `
with item_counts as (
  select
    cdp_tasks.id as task_id,
    coalesce((select count(*)::int from cdp_task_items where task_id=cdp_tasks.id), 0) as item_count,
    coalesce((select count(*)::int from cdp_task_items where task_id=cdp_tasks.id and status=2), 0) as local_success_count
  from cdp_tasks
  where id=$1 and provider_code=$8 and deleted_at is null
),
callback_counts as (
  select
    task_id,
    item_count as total_count,
    least(greatest(case when $4::int > 0 then $4::int else local_success_count end, 0), item_count) as success_count,
    least(greatest($5::int, 0), greatest(item_count - least(greatest(case when $4::int > 0 then $4::int else local_success_count end, 0), item_count), 0)) as failed_count
  from item_counts
)
update cdp_tasks
set status=case when cdp_tasks.status in ($10,$11,$12) then cdp_tasks.status else $2 end,
  total_count=case when cdp_tasks.status in ($10,$11,$12) then cdp_tasks.total_count else callback_counts.total_count end,
  provider_external_batch_id=coalesce(nullif($3,''), provider_external_batch_id),
  provider_response=coalesce(provider_response, '{}'::jsonb) || jsonb_build_object(
		    'providerCallback', jsonb_build_object(
		      'totalCount', $6::int,
		      'successCount', $4,
		      'failedCount', $5,
		      'externalBatchId', nullif($3, ''),
		      'finishedAt', $7::timestamptz,
		      'rawRequest', $9::jsonb
		    )
		  ),
			  success_count=case when cdp_tasks.status in ($10,$11,$12) then cdp_tasks.success_count else callback_counts.success_count end,
			  failed_count=case when cdp_tasks.status in ($10,$11,$12) then cdp_tasks.failed_count else callback_counts.failed_count end,
		  provider_finished_at=coalesce(provider_finished_at, $7::timestamptz),
		  updated_at=now()
		from callback_counts
		where id=$1 and provider_code=$8
			`, task.ID, biz.TaskStatusProviderDone, input.ExternalBatchID, input.SuccessCount, input.FailedCount, nullableInt(input.TotalCount), finishedAt, input.ProviderCode, rawRequest, biz.TaskStatusResultImported, biz.TaskStatusCallbackPending, biz.TaskStatusDone)
	if err != nil {
		return nil, err
	}
	return r.GetTask(ctx, task.ID)
}

func (r *TaskRepo) MarkProviderCallbackProgress(ctx context.Context, input biz.ProviderCallbackInput) (*biz.Task, error) {
	task, err := r.getProviderCallbackTask(ctx, input)
	if err != nil {
		return nil, err
	}
	if task.ProviderCode != input.ProviderCode {
		return nil, biz.ErrProviderNotFound
	}
	if task.Status == biz.TaskStatusCallbackPending || task.Status == biz.TaskStatusDone {
		return task, nil
	}
	finishedAt := time.Now()
	if !input.FinishedAt.IsZero() {
		finishedAt = input.FinishedAt
	}
	rawRequest := input.RawRequest
	if len(rawRequest) == 0 {
		rawRequest = json.RawMessage(`{}`)
	}
	_, err = r.dbFor(ctx).Exec(ctx, `
update cdp_tasks
set provider_external_batch_id=coalesce(nullif($2,''), provider_external_batch_id),
  provider_response=coalesce(provider_response, '{}'::jsonb) || jsonb_build_object(
    'providerCallbackProgress', jsonb_build_object(
      'totalCount', $3::int,
      'successCount', $4::int,
      'failedCount', $5::int,
      'externalBatchId', nullif($2, ''),
      'finishedAt', $6::timestamptz,
      'rawRequest', $7::jsonb
    )
  ),
  total_count=coalesce((select count(*)::int from cdp_task_items where task_id=cdp_tasks.id), 0),
  success_count=least(greatest($4::int, 0), coalesce((select count(*)::int from cdp_task_items where task_id=cdp_tasks.id), 0)),
  failed_count=least(greatest($5::int, 0), greatest(coalesce((select count(*)::int from cdp_task_items where task_id=cdp_tasks.id), 0) - least(greatest($4::int, 0), coalesce((select count(*)::int from cdp_task_items where task_id=cdp_tasks.id), 0)), 0)),
  updated_at=now()
where id=$1 and provider_code=$8 and deleted_at is null
	`, task.ID, input.ExternalBatchID, nullableInt(input.TotalCount), input.SuccessCount, input.FailedCount, finishedAt, rawRequest, input.ProviderCode)
	if err != nil {
		return nil, err
	}
	return r.GetTask(ctx, task.ID)
}

func (r *TaskRepo) getProviderCallbackTask(ctx context.Context, input biz.ProviderCallbackInput) (*biz.Task, error) {
	if input.TaskID > 0 {
		task, err := r.GetTask(ctx, input.TaskID)
		if err != nil {
			return nil, err
		}
		if task.ProviderCode != input.ProviderCode {
			return nil, biz.ErrProviderNotFound
		}
		if task.Status == biz.TaskStatusReassigned {
			return nil, biz.ErrTaskClosed
		}
		return task, nil
	}
	return r.ResolveProviderTask(ctx, input.ProviderCode, input.BatchNo)
}

func taskBatchNoForProvider(task *biz.Task) string {
	if task != nil && task.ReassignedFromTaskID > 0 && strings.TrimSpace(task.CallbackBatchNo) != "" {
		return task.CallbackBatchNo
	}
	if task == nil {
		return ""
	}
	return task.BatchNo
}

func (r *TaskRepo) MarkProviderStatus(ctx context.Context, taskID int64, result biz.ProviderStatusResult) error {
	tx, err := r.dbFor(ctx).Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, question := range result.Questions {
		_, err := tx.Exec(ctx, `
	update cdp_task_items
set question_id=$3, status=$4, updated_at=now()
where task_id=$1 and (item_id=$2 or question_id=$2)
  and (question_id, status) is distinct from ($3::varchar, $4::int)
`, taskID, question.QuestionID, question.QuestionID, question.Status)
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `
with item_counts as (
  select
    coalesce((select count(*)::int from cdp_task_items where task_id=$1), 0) as item_count,
    coalesce((select count(*)::int from cdp_task_items where task_id=$1 and status=2), 0) as provider_done_count
),
progress_counts as (
  select
    item_count,
    least(
      greatest(case when $3::int + $4::int > 0 then $3::int else provider_done_count end, 0),
      item_count
    ) as success_count
  from item_counts
),
bounded_counts as (
  select
    item_count,
    success_count,
    least(greatest($4::int, 0), greatest(item_count - success_count, 0)) as failed_count
  from progress_counts
)
update cdp_tasks
set provider_response=coalesce($2::jsonb, '{}'::jsonb) ||
    case
      when coalesce(provider_response, '{}'::jsonb) ? 'providerCallback'
      then jsonb_build_object('providerCallback', provider_response->'providerCallback')
      else '{}'::jsonb
    end,
  total_count=bounded_counts.item_count,
  success_count=case
    when cdp_tasks.status in ($5,$6,$7,$8,$9) then cdp_tasks.success_count
    else bounded_counts.success_count
  end,
  failed_count=case
    when cdp_tasks.status in ($5,$6,$7,$8,$9) then cdp_tasks.failed_count
    else bounded_counts.failed_count
  end,
  updated_at=now()
from bounded_counts
where id=$1
	`, taskID, result.RawResponse, result.SuccessCount, result.FailedCount, biz.TaskStatusResultImported, biz.TaskStatusCallbackPending, biz.TaskStatusDone, biz.TaskStatusFailed, biz.TaskStatusReassigned)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *TaskRepo) UpsertPendingResultFiles(ctx context.Context, taskID int64, files []biz.ResultFileInput) error {
	if !taskLocked(ctx, taskID) {
		return r.WithTaskLock(ctx, taskID, func(ctx context.Context) error { return r.UpsertPendingResultFiles(ctx, taskID, files) })
	}
	if len(files) == 0 {
		return nil
	}
	tx, err := r.dbFor(ctx).Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	task, err := r.scanTask(tx.QueryRow(ctx, taskSelectSQL()+` where id=$1 for update`, taskID))
	if err != nil {
		return err
	}
	batchNo := taskBatchNoForProvider(task)
	for _, file := range files {
		if file.ExternalBatchID == "" {
			file.ExternalBatchID = task.ProviderExternalBatchID
		}
		if file.ParseStatus == "" {
			file.ParseStatus = "PENDING"
		}
		if file.SourceType == "" {
			if file.ObjectKey != "" {
				file.SourceType = "OSS"
			} else {
				file.SourceType = "PROVIDER_FILE"
			}
		}
		if file.ObjectKey != "" {
			if file.FileName == "" {
				file.FileName = path.Base(file.ObjectKey)
			}
			if _, err := tx.Exec(ctx, `
insert into cdp_task_result_files (
  task_id,batch_no,external_batch_id,source_type,object_key,file_name,parse_status
) values ($1,$2,$3,$4,$5,$6,$7)
on conflict (batch_no, object_key) where object_key is not null do update set
  task_id=excluded.task_id,
  external_batch_id=excluded.external_batch_id,
  source_type=excluded.source_type,
  file_name=excluded.file_name,
  parse_status=case
    when cdp_task_result_files.parse_status='SUCCESS' then cdp_task_result_files.parse_status
    else excluded.parse_status
  end,
  updated_at=now()
`, taskID, batchNo, file.ExternalBatchID, file.SourceType, file.ObjectKey, file.FileName, file.ParseStatus); err != nil {
				return err
			}
			continue
		}
		if file.FileURL == "" {
			continue
		}
		if file.FileName == "" {
			file.FileName = path.Base(file.FileURL)
		}
		if _, err := tx.Exec(ctx, `
insert into cdp_task_result_files (
  task_id,batch_no,external_batch_id,source_type,file_url,file_name,parse_status
) values ($1,$2,$3,$4,$5,$6,$7)
on conflict (batch_no, file_url) where file_url is not null do update set
  task_id=excluded.task_id,
  external_batch_id=excluded.external_batch_id,
  source_type=excluded.source_type,
  file_name=excluded.file_name,
  parse_status=case
    when cdp_task_result_files.parse_status='SUCCESS' then cdp_task_result_files.parse_status
    else excluded.parse_status
  end,
  updated_at=now()
`, taskID, batchNo, file.ExternalBatchID, file.SourceType, file.FileURL, file.FileName, file.ParseStatus); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `update cdp_tasks set risk_stats_updated_at=null where id=$1 and risk_stats_updated_at is not null and exists(select 1 from cdp_task_result_files where task_id=$1 and parse_status<>'SUCCESS')`, taskID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *TaskRepo) MarkResultFileFailed(ctx context.Context, taskID int64, file biz.ResultFileInput, errMessage string) error {
	if !taskLocked(ctx, taskID) {
		return r.WithTaskLock(ctx, taskID, func(ctx context.Context) error { return r.MarkResultFileFailed(ctx, taskID, file, errMessage) })
	}
	task, err := r.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	batchNo := taskBatchNoForProvider(task)
	if file.ExternalBatchID == "" {
		file.ExternalBatchID = task.ProviderExternalBatchID
	}
	if file.ObjectKey != "" {
		_, err = r.dbFor(ctx).Exec(ctx, `
insert into cdp_task_result_files (
  task_id,batch_no,external_batch_id,source_type,object_key,file_name,parse_status,last_error,fetched_at
) values ($1,$2,$3,$4,$5,$6,'FAILED',$7,now())
on conflict (batch_no, object_key) where object_key is not null do update set
  parse_status='FAILED',
  last_error=excluded.last_error,
  updated_at=now()
`, taskID, batchNo, file.ExternalBatchID, file.SourceType, file.ObjectKey, file.FileName, errMessage)
		return err
	}
	if file.FileURL != "" {
		_, err = r.dbFor(ctx).Exec(ctx, `
insert into cdp_task_result_files (
  task_id,batch_no,external_batch_id,source_type,file_url,file_name,parse_status,last_error,fetched_at
) values ($1,$2,$3,$4,$5,$6,'FAILED',$7,now())
on conflict (batch_no, file_url) where file_url is not null do update set
  parse_status='FAILED',
  last_error=excluded.last_error,
  updated_at=now()
`, taskID, batchNo, file.ExternalBatchID, file.SourceType, file.FileURL, file.FileName, errMessage)
		return err
	}
	return nil
}

func (r *TaskRepo) MarkResultImported(ctx context.Context, taskID int64, file biz.ResultFileInput, result biz.ParsedResult) error {
	if !taskLocked(ctx, taskID) {
		return r.WithTaskLock(ctx, taskID, func(ctx context.Context) error { return r.MarkResultImported(ctx, taskID, file, result) })
	}
	tx, err := r.dbFor(ctx).Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	task, err := r.scanTask(tx.QueryRow(ctx, taskSelectSQL()+` where id=$1 for update`, taskID))
	if err != nil {
		return err
	}
	var imported bool
	if err := tx.QueryRow(ctx, `select exists(select 1 from cdp_task_result_files where task_id=$1 and parse_status='SUCCESS' and (($2<>'' and object_key=$2) or ($2='' and $3<>'' and file_url=$3)))`, taskID, file.ObjectKey, file.FileURL).Scan(&imported); err != nil {
		return err
	}
	if imported {
		return nil
	}
	batchNo := taskBatchNoForProvider(task)
	if file.ExternalBatchID == "" {
		file.ExternalBatchID = task.ProviderExternalBatchID
	}
	var resultFileID int64
	if file.ObjectKey != "" {
		err = tx.QueryRow(ctx, `
insert into cdp_task_result_files (
  task_id,batch_no,external_batch_id,source_type,object_key,file_name,checksum,parse_status,
  row_count,valid_row_count,invalid_row_count,fetched_at,parsed_at
) values ($1,$2,$3,$4,$5,$6,$7,'SUCCESS',$8,$9,$10,now(),now())
on conflict (batch_no, object_key) where object_key is not null do update set
  checksum=excluded.checksum,
  parse_status=excluded.parse_status,
  row_count=excluded.row_count,
  valid_row_count=excluded.valid_row_count,
  invalid_row_count=excluded.invalid_row_count,
  parsed_at=now(),
  updated_at=now()
returning id
`, taskID, batchNo, file.ExternalBatchID, file.SourceType, file.ObjectKey, file.FileName, file.Checksum,
			result.RowCount, result.ValidRowCount, result.InvalidRowCount).Scan(&resultFileID)
	} else {
		err = tx.QueryRow(ctx, `
insert into cdp_task_result_files (
  task_id,batch_no,external_batch_id,source_type,file_url,file_name,checksum,parse_status,
  row_count,valid_row_count,invalid_row_count,fetched_at,parsed_at
) values ($1,$2,$3,$4,$5,$6,$7,'SUCCESS',$8,$9,$10,now(),now())
on conflict (batch_no, file_url) where file_url is not null do update set
  checksum=excluded.checksum,
  parse_status=excluded.parse_status,
  row_count=excluded.row_count,
  valid_row_count=excluded.valid_row_count,
  invalid_row_count=excluded.invalid_row_count,
  parsed_at=now(),
  updated_at=now()
returning id
`, taskID, batchNo, file.ExternalBatchID, file.SourceType, file.FileURL, file.FileName, file.Checksum,
			result.RowCount, result.ValidRowCount, result.InvalidRowCount).Scan(&resultFileID)
	}
	if err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
update cdp_task_items
set result_status='PENDING', result_imported_at=null, updated_at=now()
where id in (
  select task_item_id from cdp_task_item_results where result_file_id=$1
)
`, resultFileID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `delete from cdp_task_item_results where result_file_id=$1`, resultFileID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `delete from cdp_task_result_rows where result_file_id=$1`, resultFileID); err != nil {
		return err
	}
	for _, row := range result.Rows {
		if len(row.RowData) == 0 {
			row.RowData = json.RawMessage(`{}`)
		}
		_, err := tx.Exec(ctx, `
insert into cdp_task_result_rows (
  task_id,batch_no,external_batch_id,result_file_id,sheet_name,row_index,item_id,seq,row_data,row_hash,parse_error
) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
on conflict (result_file_id, sheet_name, row_index) do update set
	  item_id=excluded.item_id,
	  seq=excluded.seq,
	  row_data=excluded.row_data,
	  row_hash=excluded.row_hash,
	  parse_error=excluded.parse_error,
	  updated_at=now()
	`, taskID, batchNo, file.ExternalBatchID, resultFileID, row.SheetName, row.RowIndex, nullableString(row.ItemID), row.Seq,
			row.RowData, row.RowHash, nullableString(row.ParseError))
		if err != nil {
			return err
		}
	}
	for _, itemResult := range result.ItemResults {
		if itemResult.Status == "" {
			itemResult.Status = biz.ValidationUnknown
		}
		if itemResult.RiskItems == nil {
			itemResult.RiskItems = []biz.AnswerRisk{}
		}
		riskJSON, marshalErr := json.Marshal(itemResult.RiskItems)
		if marshalErr != nil {
			return marshalErr
		}
		var taskItemID int64
		err := tx.QueryRow(ctx, `
select id from cdp_task_items
where task_id=$1 and item_id=$2
`, taskID, itemResult.ItemID).Scan(&taskItemID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if len(itemResult.RecommendedQuestions) == 0 {
			itemResult.RecommendedQuestions = json.RawMessage(`[]`)
		}
		if len(itemResult.ReferencesJSON) == 0 {
			itemResult.ReferencesJSON = json.RawMessage(`[]`)
		}
		if len(itemResult.CleanedPayload) == 0 {
			itemResult.CleanedPayload = json.RawMessage(`{}`)
		}
		tag, err := tx.Exec(ctx, `
insert into cdp_task_item_results (
  task_id,task_item_id,batch_no,item_id,seq,platform_code,question,answer_text,thinking_process,
	  generated_at,generated_at_text,recommended_questions,device_id,recording_url,references_json,
	  cleaned_payload,parse_status,parse_error,result_file_id,answer_sheet_row_index,reference_count,row_hash,validation_status,risk_items,validation_rule_version,validated_at
	) values (
	  $1,$2,$3,$4,$5,$6,$7,$8,$9,
	  $10,$11,$12,$13,$14,$15,
	  $16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26
	)
on conflict (task_item_id, seq) do update set
	  batch_no=excluded.batch_no,
	  item_id=excluded.item_id,
	  platform_code=excluded.platform_code,
  question=excluded.question,
  answer_text=excluded.answer_text,
  thinking_process=excluded.thinking_process,
  generated_at=excluded.generated_at,
  generated_at_text=excluded.generated_at_text,
  recommended_questions=excluded.recommended_questions,
  device_id=excluded.device_id,
  recording_url=excluded.recording_url,
  references_json=excluded.references_json,
  cleaned_payload=excluded.cleaned_payload,
  parse_status=excluded.parse_status,
  parse_error=excluded.parse_error,
  result_file_id=excluded.result_file_id,
  answer_sheet_row_index=excluded.answer_sheet_row_index,
	  reference_count=excluded.reference_count,
	  row_hash=excluded.row_hash,
 validation_status=excluded.validation_status, risk_items=excluded.risk_items,
 validation_rule_version=excluded.validation_rule_version,validated_at=excluded.validated_at,
	  updated_at=now()
where excluded.result_file_id >= cdp_task_item_results.result_file_id
	`, taskID, taskItemID, batchNo, itemResult.ItemID, resultSeq(itemResult.Seq), nullableString(itemResult.PlatformCode),
			nullableString(itemResult.Question), nullableString(itemResult.AnswerText), nullableString(itemResult.ThinkingProcess),
			nullableTime(itemResult.GeneratedAt), nullableString(itemResult.GeneratedAtText), itemResult.RecommendedQuestions,
			nullableString(itemResult.DeviceID), nullableString(itemResult.RecordingURL), itemResult.ReferencesJSON,
			itemResult.CleanedPayload, itemResult.ParseStatus, nullableString(itemResult.ParseError), resultFileID,
			nullableInt(itemResult.AnswerSheetRowIndex), itemResult.ReferenceCount, nullableString(itemResult.RowHash), itemResult.Status, riskJSON, itemResult.RuleVersion, nullableTime(itemResult.ValidatedAt))
		if err != nil {
			return err
		}
		// A failed older file may finish after a newer file. Keep the newer
		// answer and its item status instead of applying the stale retry.
		if tag.RowsAffected() == 0 {
			continue
		}
		resultStatus := "IMPORTED"
		if itemResult.ParseStatus != "" && itemResult.ParseStatus != "SUCCESS" {
			resultStatus = itemResult.ParseStatus
		}
		_, err = tx.Exec(ctx, `
update cdp_task_items ti
set result_status=case
    when $3 <> 'IMPORTED' then $3
    when (
      select count(*)
      from cdp_task_item_results tir
      where tir.task_item_id=ti.id and tir.parse_status='SUCCESS'
    ) >= 1 then 'IMPORTED'
    else 'PENDING'
  end,
	  result_imported_at=now(),
	  updated_at=now()
where task_id=$1 and item_id=$2
`, taskID, itemResult.ItemID, resultStatus)
		if err != nil {
			return err
		}
	}

	_, err = tx.Exec(ctx, `
with item_result_counts as (
`+taskItemStatsSQL("$1")+`
)
update cdp_tasks
set result_payload=$2,
 risk_stats_updated_at=null,
  result_imported_at=now(),
  result_row_count=(select count(*) from cdp_task_result_rows where task_id=$1),
  total_count=item_result_counts.item_count,
  success_count=item_result_counts.result_success_count,
  failed_count=item_result_counts.result_failed_count,
  updated_at=now()
from item_result_counts
where id=$1
	`, taskID, result.Payload)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *TaskRepo) CompleteResultImport(ctx context.Context, taskID int64, notifyCallback bool) (*biz.Task, bool, error) {
	if !taskLocked(ctx, taskID) {
		var task *biz.Task
		var ready bool
		err := r.WithTaskLock(ctx, taskID, func(ctx context.Context) error {
			var err error
			task, ready, err = r.CompleteResultImport(ctx, taskID, notifyCallback)
			return err
		})
		return task, ready, err
	}
	tx, err := r.dbFor(ctx).Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Serialize the decision with result import and task status updates. Reuse
	// one statistics snapshot for both the transition and callback readiness.
	task, err := r.scanTask(tx.QueryRow(ctx, taskSelectSQL()+` where id=$1 and deleted_at is null for update`, taskID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, biz.ErrTaskNotFound
	}
	if err != nil {
		return nil, false, err
	}
	if err := aggregateTaskValidation(ctx, tx, taskID); err != nil {
		return nil, false, err
	}
	var nextStatus biz.TaskStatus
	var total, success, failed int
	var callbackReady bool
	err = tx.QueryRow(ctx, `
with stats as materialized (
`+taskItemStatsSQL("$1")+`
), readiness as (
  select t.status, stats.*,
    coalesce(t.provider_response, '{}'::jsonb) ? 'providerCallback' as has_provider_callback,
    ($2 and (t.risk_count=0 or $7) and t.risk_stats_updated_at is not null and item_count > 0 and imported_count=item_count
      and result_success_count + result_failed_count=item_count
      and coalesce(nullif(t.provider_response #>> '{providerCallback,totalCount}', '')::int, item_count)=item_count
      and coalesce(nullif(t.provider_response #>> '{providerCallback,successCount}', '')::int, result_success_count)
        + coalesce(nullif(t.provider_response #>> '{providerCallback,failedCount}', '')::int, result_failed_count)
        = coalesce(nullif(t.provider_response #>> '{providerCallback,totalCount}', '')::int, item_count)
    ) as counts_ready
  from cdp_tasks t cross join stats where t.id=$1
), decision as (
  select *, case
    when status=$5 then status
    when status=$4 and counts_ready then status
    when counts_ready and (status=$3 or (status=$6 and has_provider_callback)) then $4
    else $6
  end as next_status
  from readiness
)
select next_status, item_count, result_success_count, result_failed_count,
  (counts_ready and next_status=$4)
from decision`, taskID, notifyCallback, biz.TaskStatusProviderDone, biz.TaskStatusCallbackPending, biz.TaskStatusDone, biz.TaskStatusResultImported, biz.AllowsRiskCallback(ctx, task.ProviderCode)).
		Scan(&nextStatus, &total, &success, &failed, &callbackReady)
	if err != nil {
		return nil, false, err
	}
	var updatedAt time.Time
	err = tx.QueryRow(ctx, `
update cdp_tasks
set status=$2, total_count=$3, success_count=$4, failed_count=$5,
  result_imported_at=coalesce(result_imported_at, now()), updated_at=now()
where id=$1 and (
  (status, total_count, success_count, failed_count) is distinct from ($2::varchar, $3::int, $4::int, $5::int)
  or result_imported_at is null
)
returning updated_at`, taskID, nextStatus, total, success, failed).Scan(&updatedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}
	if err == nil {
		task.UpdatedAt = updatedAt
	}
	task.Status, task.TotalCount, task.SuccessCount, task.FailedCount = nextStatus, total, success, failed
	if err := tx.QueryRow(ctx, `select risk_count,invalid_answer_count,risk_summary,risk_stats_updated_at from cdp_tasks where id=$1`, taskID).Scan(&task.RiskCount, &task.InvalidAnswerCount, &task.RiskSummary, &task.RiskStatsUpdatedAt); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return task, callbackReady, nil
}

func (r *TaskRepo) MarkCallbackDone(ctx context.Context, taskID int64, targetURL string, statusCode int, responseBody string, payload json.RawMessage) error {
	tx, err := r.dbFor(ctx).Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if targetURL == "" {
		task, err := r.GetTask(ctx, taskID)
		if err != nil {
			return err
		}
		targetURL = task.CallbackURL
	}
	_, err = tx.Exec(ctx, `
insert into cdp_task_callbacks (task_id,target_url,request_payload,response_status,response_body,status)
values ($1,$2,$3,$4,$5,$6)
`, taskID, targetURL, payload, statusCode, responseBody, biz.TaskStatusDone)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `update cdp_tasks set status=$2,callback_sent_at=now(),updated_at=now() where id=$1`, taskID, biz.TaskStatusDone)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *TaskRepo) MarkCallbackFailed(ctx context.Context, taskID int64, targetURL string, errMessage string, payload json.RawMessage, maxRetry int) (int, bool, error) {
	tx, err := r.dbFor(ctx).Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	task, err := r.scanTask(tx.QueryRow(ctx, taskSelectSQL()+` where id=$1 for update`, taskID))
	if err != nil {
		return 0, false, err
	}
	if targetURL == "" {
		targetURL = task.CallbackURL
	}
	_, err = tx.Exec(ctx, `
insert into cdp_task_callbacks (task_id,target_url,request_payload,status,last_error)
values ($1,$2,$3,$4,$5)
`, taskID, targetURL, payload, "FAILED", errMessage)
	if err != nil {
		return 0, false, err
	}

	var attemptCount int
	if err := tx.QueryRow(ctx, `select count(*) from cdp_task_callbacks where task_id=$1 and status='FAILED'`, taskID).Scan(&attemptCount); err != nil {
		return 0, false, err
	}
	stopped := maxRetry > 0 && attemptCount >= maxRetry
	if stopped {
		_, err = tx.Exec(ctx, `
update cdp_tasks
set status=$2,last_error=$3,updated_at=now()
where id=$1
`, taskID, biz.TaskStatusFailed, errMessage)
		if err != nil {
			return 0, false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, false, err
	}
	return attemptCount, stopped, nil
}

// RecordForcedCallbackDone records a forced callback as succeeded.
// It is intentionally independent from MarkCallbackDone: it only inserts a
// callback row and bumps cdp_tasks.updated_at. It MUST NOT change task status
// or callback_sent_at, so that subsequent normal imports/callbacks keep working.
func (r *TaskRepo) RecordForcedCallbackDone(ctx context.Context, taskID int64, targetURL string, statusCode int, responseBody string, payload json.RawMessage) error {
	tx, err := r.dbFor(ctx).Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if targetURL == "" {
		task, err := r.GetTask(ctx, taskID)
		if err != nil {
			return err
		}
		targetURL = task.CallbackURL
	}
	if _, err := tx.Exec(ctx, `
insert into cdp_task_callbacks (task_id,target_url,request_payload,response_status,response_body,status)
values ($1,$2,$3,$4,$5,$6)
`, taskID, targetURL, payload, statusCode, responseBody, biz.TaskStatusDone); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update cdp_tasks set updated_at=now() where id=$1`, taskID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RecordForcedCallbackFailed records a forced callback as failed.
// It is intentionally independent from MarkCallbackFailed: it only inserts a
// callback row and bumps cdp_tasks.updated_at. It MUST NOT change task status,
// callback_sent_at, last_error or retry counters.
func (r *TaskRepo) RecordForcedCallbackFailed(ctx context.Context, taskID int64, targetURL string, errMessage string, payload json.RawMessage) error {
	tx, err := r.dbFor(ctx).Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if targetURL == "" {
		task, err := r.GetTask(ctx, taskID)
		if err != nil {
			return err
		}
		targetURL = task.CallbackURL
	}
	if _, err := tx.Exec(ctx, `
insert into cdp_task_callbacks (task_id,target_url,request_payload,status,last_error)
values ($1,$2,$3,$4,$5)
`, taskID, targetURL, payload, "FAILED", errMessage); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update cdp_tasks set updated_at=now() where id=$1`, taskID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *TaskRepo) AddEvent(ctx context.Context, taskID int64, eventType biz.EventType, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		body = []byte(`{}`)
	}
	_, err = r.dbFor(ctx).Exec(ctx, `insert into cdp_task_events (task_id,event_type,payload) values ($1,$2,$3)`, taskID, eventType, body)
	return err
}

type taskScanner interface {
	Scan(dest ...any) error
}

func taskSelectSQL() string {
	return `select id,coalesce(job_id,''),coalesce(platform_user_id,0),coalesce(platform_code,''),batch_no,coalesce(task_name,''),provider_code,
coalesce(gateway_code,''),coalesce(source,''),coalesce(external_batch_id,''),coalesce(provider_external_batch_id,''),
coalesce(reassigned_from_task_id,0),coalesce(callback_batch_no,''),coalesce(callback_external_batch_id,''),
coalesce(dispatch_profile_id,0),coalesce(dispatch_profile_code,''),coalesce(dispatch_strategy,''),biz_date,coalesce(quota_date,biz_date),status,
coalesce(callback_url,''),coalesce(provider_callback_url,''),raw_request,coalesce(provider_request,'{}'::jsonb),
coalesce(provider_response,'{}'::jsonb),coalesce(result_object_key,''),
coalesce(result_file_name,''),result_row_count,total_count,success_count,failed_count,coalesce(last_error,''),created_at,updated_at,risk_count,invalid_answer_count,risk_summary,risk_stats_updated_at,device,execution_mode from cdp_tasks`
}

func (r *TaskRepo) scanTask(scanner taskScanner) (*biz.Task, error) {
	var task biz.Task
	if err := scanner.Scan(&task.ID, &task.JobID, &task.PlatformUserID, &task.PlatformCode, &task.BatchNo, &task.TaskName,
		&task.ProviderCode, &task.GatewayCode, &task.Source, &task.ExternalBatchID, &task.ProviderExternalBatchID,
		&task.ReassignedFromTaskID, &task.CallbackBatchNo, &task.CallbackExternalBatchID,
		&task.DispatchProfileID, &task.DispatchProfileCode, &task.DispatchStrategy, &task.BizDate,
		&task.QuotaDate, &task.Status, &task.CallbackURL, &task.ProviderCallbackURL, &task.RawRequest, &task.ProviderRequest, &task.ProviderResponse,
		&task.ResultObjectKey, &task.ResultFileName, &task.ResultRowCount, &task.TotalCount, &task.SuccessCount,
		&task.FailedCount, &task.LastError, &task.CreatedAt, &task.UpdatedAt, &task.RiskCount, &task.InvalidAnswerCount, &task.RiskSummary, &task.RiskStatsUpdatedAt, &task.Device, &task.ExecutionMode); err != nil {
		return nil, err
	}
	return &task, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func taskItemExpectedCount(items []biz.TaskItem) int {
	return len(items)
}

func dispatchQuestionCount(items []biz.TaskItem) int {
	seen := map[string]bool{}
	count := 0
	for _, item := range items {
		key := strings.TrimSpace(item.ItemID)
		if key != "" {
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		count++
	}
	return count
}

func resultSeq(seq int) int {
	if seq <= 0 {
		return 1
	}
	return seq
}

func callbackReadinessReason(readiness biz.CallbackReadiness) string {
	if readiness.Ready {
		return "数量校验通过"
	}
	if !readiness.ProviderDone {
		return "供应商回调尚未完成"
	}
	if readiness.TaskItemCount == 0 {
		return "任务没有问答明细"
	}
	if readiness.ProviderTotal > 0 && readiness.ProviderTotal != readiness.TaskItemCount {
		return fmt.Sprintf("数量不一致：供应商总数 %d / 本地任务总数 %d，当前成功 %d / 任务总数 %d", readiness.ProviderTotal, readiness.TaskItemCount, readiness.SuccessCount, readiness.TotalCount)
	}
	if readiness.ImportedCount != readiness.TaskItemCount {
		return fmt.Sprintf("数量不一致：当前成功 %d / 任务总数 %d，已导入 item %d / %d", readiness.SuccessCount, readiness.TotalCount, readiness.ImportedCount, readiness.TaskItemCount)
	}
	if readiness.SuccessCount+readiness.FailedCount != readiness.TotalCount {
		return fmt.Sprintf("数量不一致：当前成功 %d / 任务总数 %d", readiness.SuccessCount, readiness.TotalCount)
	}
	return "回调数量校验未通过"
}

func nullableInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}
