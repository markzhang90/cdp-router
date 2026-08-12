package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type TaskRepo struct {
	db *pgxpool.Pool
}

func NewTaskRepo(db *pgxpool.Pool) *TaskRepo {
	return &TaskRepo{db: db}
}

func (r *TaskRepo) CreateTask(ctx context.Context, input biz.CreateTaskInput, providerCallbackURL string) (*biz.Task, error) {
	bizDate, err := time.Parse("2006-01-02", input.BizDate)
	if err != nil {
		return nil, fmt.Errorf("%w: bizDate must be yyyy-MM-dd", biz.ErrInvalidTaskRequest)
	}
	source := input.Source
	if source == "" {
		source = input.GatewayCode
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var task biz.Task
	err = tx.QueryRow(ctx, `
insert into cdp_tasks (
  job_id, platform_user_id, platform_code, batch_no, provider_code, gateway_code, source, external_batch_id,
  biz_date, status, callback_url, provider_callback_url, raw_request, total_count
) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
on conflict (batch_no) do update set updated_at=cdp_tasks.updated_at
returning id, coalesce(job_id,''), coalesce(platform_user_id,0), coalesce(platform_code,''), batch_no, provider_code,
  coalesce(gateway_code,''), coalesce(source,''), coalesce(external_batch_id,''), coalesce(provider_external_batch_id,''), biz_date, status,
  coalesce(callback_url,''), coalesce(provider_callback_url,''), raw_request, coalesce(provider_request,'{}'::jsonb),
  coalesce(provider_response,'{}'::jsonb), coalesce(result_payload,'{}'::jsonb), coalesce(result_object_key,''),
  coalesce(result_file_name,''), result_row_count, total_count, success_count, failed_count, coalesce(last_error,''), created_at, updated_at
	`, input.JobID, input.PlatformUserID, input.PlatformCode, input.BatchNo, input.ProviderCode, input.GatewayCode, source, input.JobID,
		bizDate, biz.TaskStatusCreated, input.CallbackURL, providerCallbackURL, input.RawRequest, taskItemExpectedCount(input.Items)).
		Scan(&task.ID, &task.JobID, &task.PlatformUserID, &task.PlatformCode, &task.BatchNo, &task.ProviderCode,
			&task.GatewayCode, &task.Source, &task.ExternalBatchID, &task.ProviderExternalBatchID, &task.BizDate, &task.Status,
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

func (r *TaskRepo) GetTask(ctx context.Context, id int64) (*biz.Task, error) {
	task, err := r.scanTask(r.db.QueryRow(ctx, taskSelectSQL()+` where id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, biz.ErrTaskNotFound
	}
	return task, err
}

func (r *TaskRepo) GetTaskByJobID(ctx context.Context, jobID string, platformUserID int64) (*biz.Task, error) {
	task, err := r.scanTask(r.db.QueryRow(ctx, taskSelectSQL()+` where job_id=$1 and platform_user_id=$2`, jobID, platformUserID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, biz.ErrTaskNotFound
	}
	return task, err
}

func (r *TaskRepo) GetTaskByBatchNo(ctx context.Context, batchNo string) (*biz.Task, error) {
	task, err := r.scanTask(r.db.QueryRow(ctx, taskSelectSQL()+` where batch_no=$1`, batchNo))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, biz.ErrTaskNotFound
	}
	return task, err
}

func (r *TaskRepo) ListTasksByStatus(ctx context.Context, status biz.TaskStatus, limit int) ([]*biz.Task, error) {
	rows, err := r.db.Query(ctx, taskSelectSQL()+` where status=$1 order by created_at asc limit $2`, status, limit)
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
	rows, err := r.db.Query(ctx, taskSelectSQL()+`
where status=$1
  and provider_external_batch_id is not null
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
	if err := r.db.QueryRow(ctx, `select count(*)::int from cdp_task_items where task_id=$1`, taskID).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (r *TaskRepo) ListTaskItems(ctx context.Context, taskID int64) ([]biz.TaskItem, error) {
	rows, err := r.db.Query(ctx, `
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
	rows, err := r.db.Query(ctx, `
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
  cross join lateral generate_series(1, ti.expected_count) as expected(seq)
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
	  coalesce(tir.references_json,'[]'::jsonb),
	  coalesce(tir.reference_count,0),
	  coalesce(tir.parse_status,''),
  coalesce(tir.parse_error,'')
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
		var generatedAt *time.Time
		if err := rows.Scan(&item.ItemID, &item.PlatformCode, &item.Seq, &item.Question, &item.ProviderStatus,
			&item.ResultStatus, &item.AnswerText, &item.ThinkingProcess, &generatedAt, &item.GeneratedAtText,
			&item.RecommendedQuestions, &item.DeviceID, &item.RecordingURL, &item.ReferencesJSON, &item.ReferenceCount,
			&item.ParseStatus, &item.ParseError); err != nil {
			return nil, err
		}
		if generatedAt != nil {
			item.GeneratedAt = *generatedAt
		}
		results = append(results, item)
	}
	return results, rows.Err()
}

func (r *TaskRepo) GetCallbackReadiness(ctx context.Context, taskID int64) (biz.CallbackReadiness, error) {
	var readiness biz.CallbackReadiness
	var status string
	if err := r.db.QueryRow(ctx, `
with stats as (
  select
    t.status,
    coalesce(nullif(t.provider_response #>> '{providerCallback,totalCount}', '')::int, 0) as provider_total_count,
    count(ti.id)::int as task_item_count,
    count(ti.id) filter (where ti.result_status='IMPORTED')::int as imported_count,
    count(ti.id) filter (
      where exists (
        select 1 from cdp_task_item_results tir
        where tir.task_item_id=ti.id and tir.parse_status='SUCCESS'
      )
    )::int as success_count,
    count(ti.id) filter (
      where not exists (
        select 1 from cdp_task_item_results tir
        where tir.task_item_id=ti.id and tir.parse_status='SUCCESS'
      )
      and exists (
        select 1 from cdp_task_item_results tir
        where tir.task_item_id=ti.id and tir.parse_status <> 'SUCCESS'
      )
    )::int as failed_count
  from cdp_tasks t
  left join cdp_task_items ti on ti.task_id=t.id
  where t.id=$1
  group by t.id
)
select
  status,
  provider_total_count,
  task_item_count,
  imported_count,
  success_count,
  failed_count
from stats
	`, taskID).Scan(&status, &readiness.ProviderTotal, &readiness.TaskItemCount, &readiness.ImportedCount, &readiness.SuccessCount, &readiness.FailedCount); err != nil {
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
	rows, err := r.db.Query(ctx, `
select source_type, coalesce(object_key,''), coalesce(file_url,''), coalesce(file_name,'')
from cdp_task_result_files
where task_id=$1 and parse_status='SUCCESS'
order by created_at desc, id desc
limit 20
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
	rows, err := r.db.Query(ctx, `
select source_type, coalesce(object_key,''), coalesce(file_name,''), coalesce(external_batch_id,'')
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
		if err := rows.Scan(&file.SourceType, &file.ObjectKey, &file.FileName, &file.ExternalBatchID); err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

func (r *TaskRepo) CountSuccessfulResultFiles(ctx context.Context, taskID int64) (int, error) {
	var count int
	err := r.db.QueryRow(ctx, `select count(*) from cdp_task_result_files where task_id=$1 and parse_status='SUCCESS'`, taskID).Scan(&count)
	return count, err
}

func (r *TaskRepo) MarkSubmitted(ctx context.Context, taskID int64, providerRequest json.RawMessage, result biz.SubmitResult) error {
	_, err := r.db.Exec(ctx, `
update cdp_tasks
set status=$2,
  provider_request=$3,
  provider_response=$4,
  provider_external_batch_id=$5,
  total_count=(select count(*)::int from cdp_task_items where task_id=$1),
  submitted_at=now(),
  updated_at=now()
where id=$1
`, taskID, biz.TaskStatusSubmitted, providerRequest, result.RawResponse, result.ExternalBatchID)
	return err
}

func (r *TaskRepo) MarkSubmitFailed(ctx context.Context, taskID int64, errMessage string) error {
	_, err := r.db.Exec(ctx, `update cdp_tasks set status=$2,last_error=$3,updated_at=now() where id=$1`, taskID, biz.TaskStatusFailed, errMessage)
	return err
}

func (r *TaskRepo) FailSubmittedOlderThan(ctx context.Context, cutoff time.Time, errMessage string, limit int) (int, error) {
	rows, err := r.db.Query(ctx, `
with picked as (
  select id
  from cdp_tasks
  where status=$1 and coalesce(submitted_at, created_at) < $2
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
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	task, err := r.scanTask(tx.QueryRow(ctx, taskSelectSQL()+` where id=$1 for update`, taskID))
	if err != nil {
		return err
	}
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
`, taskID, task.BatchNo, task.ProviderExternalBatchID, key, fileName)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *TaskRepo) MarkProviderCallback(ctx context.Context, input biz.ProviderCallbackInput) (*biz.Task, error) {
	task, err := r.GetTaskByBatchNo(ctx, input.BatchNo)
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
	_, err = r.db.Exec(ctx, `
		with item_counts as (
		  select
		    cdp_tasks.id as task_id,
		    coalesce((select count(*)::int from cdp_task_items where task_id=cdp_tasks.id), 0) as item_count,
		    coalesce((select count(*)::int from cdp_task_items where task_id=cdp_tasks.id and status=2), 0) as local_success_count
		  from cdp_tasks
		  where batch_no=$1 and provider_code=$8
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
		set status=$2,
		  total_count=callback_counts.total_count,
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
		  success_count=callback_counts.success_count,
		  failed_count=callback_counts.failed_count,
		  provider_finished_at=coalesce(provider_finished_at, $7::timestamptz),
		  updated_at=now()
		from callback_counts
		where batch_no=$1 and provider_code=$8
		`, input.BatchNo, biz.TaskStatusProviderDone, input.ExternalBatchID, input.SuccessCount, input.FailedCount, nullableInt(input.TotalCount), finishedAt, input.ProviderCode, rawRequest)
	if err != nil {
		return nil, err
	}
	return r.GetTaskByBatchNo(ctx, input.BatchNo)
}

func (r *TaskRepo) MarkProviderStatus(ctx context.Context, taskID int64, result biz.ProviderStatusResult) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, question := range result.Questions {
		_, err := tx.Exec(ctx, `
	update cdp_task_items
set question_id=$3, status=$4, updated_at=now()
where task_id=$1 and (item_id=$2 or question_id=$2)
`, taskID, question.QuestionID, question.QuestionID, question.Status)
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `
update cdp_tasks
set provider_response=coalesce($2::jsonb, '{}'::jsonb) ||
    case
      when coalesce(provider_response, '{}'::jsonb) ? 'providerCallback'
      then jsonb_build_object('providerCallback', provider_response->'providerCallback')
      else '{}'::jsonb
    end,
  total_count=coalesce((select count(*) from cdp_task_items where task_id=$1), 0),
  success_count=coalesce((select count(*) from cdp_task_items where task_id=$1 and status=2), 0),
  updated_at=now()
where id=$1
	`, taskID, result.RawResponse)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *TaskRepo) MarkResultFileFailed(ctx context.Context, taskID int64, file biz.ResultFileInput, errMessage string) error {
	task, err := r.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if file.ExternalBatchID == "" {
		file.ExternalBatchID = task.ProviderExternalBatchID
	}
	if file.ObjectKey != "" {
		_, err = r.db.Exec(ctx, `
insert into cdp_task_result_files (
  task_id,batch_no,external_batch_id,source_type,object_key,file_name,parse_status,last_error,fetched_at
) values ($1,$2,$3,$4,$5,$6,'FAILED',$7,now())
on conflict (batch_no, object_key) where object_key is not null do update set
  parse_status='FAILED',
  last_error=excluded.last_error,
  updated_at=now()
`, taskID, task.BatchNo, file.ExternalBatchID, file.SourceType, file.ObjectKey, file.FileName, errMessage)
		return err
	}
	if file.FileURL != "" {
		_, err = r.db.Exec(ctx, `
insert into cdp_task_result_files (
  task_id,batch_no,external_batch_id,source_type,file_url,file_name,parse_status,last_error,fetched_at
) values ($1,$2,$3,$4,$5,$6,'FAILED',$7,now())
on conflict (batch_no, file_url) where file_url is not null do update set
  parse_status='FAILED',
  last_error=excluded.last_error,
  updated_at=now()
`, taskID, task.BatchNo, file.ExternalBatchID, file.SourceType, file.FileURL, file.FileName, errMessage)
		return err
	}
	return nil
}

func (r *TaskRepo) MarkResultImported(ctx context.Context, taskID int64, file biz.ResultFileInput, result biz.ParsedResult) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	task, err := r.scanTask(tx.QueryRow(ctx, taskSelectSQL()+` where id=$1 for update`, taskID))
	if err != nil {
		return err
	}
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
`, taskID, task.BatchNo, file.ExternalBatchID, file.SourceType, file.ObjectKey, file.FileName, file.Checksum,
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
`, taskID, task.BatchNo, file.ExternalBatchID, file.SourceType, file.FileURL, file.FileName, file.Checksum,
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
	`, taskID, task.BatchNo, file.ExternalBatchID, resultFileID, row.SheetName, row.RowIndex, nullableString(row.ItemID), row.Seq,
			row.RowData, row.RowHash, nullableString(row.ParseError))
		if err != nil {
			return err
		}
	}
	for _, itemResult := range result.ItemResults {
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
		_, err = tx.Exec(ctx, `
insert into cdp_task_item_results (
  task_id,task_item_id,batch_no,item_id,seq,platform_code,question,answer_text,thinking_process,
	  generated_at,generated_at_text,recommended_questions,device_id,recording_url,references_json,
	  cleaned_payload,parse_status,parse_error,result_file_id,answer_sheet_row_index,reference_count,row_hash
	) values (
	  $1,$2,$3,$4,$5,$6,$7,$8,$9,
	  $10,$11,$12,$13,$14,$15,
	  $16,$17,$18,$19,$20,$21,$22
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
	  updated_at=now()
	`, taskID, taskItemID, task.BatchNo, itemResult.ItemID, resultSeq(itemResult.Seq), nullableString(itemResult.PlatformCode),
			nullableString(itemResult.Question), nullableString(itemResult.AnswerText), nullableString(itemResult.ThinkingProcess),
			nullableTime(itemResult.GeneratedAt), nullableString(itemResult.GeneratedAtText), itemResult.RecommendedQuestions,
			nullableString(itemResult.DeviceID), nullableString(itemResult.RecordingURL), itemResult.ReferencesJSON,
			itemResult.CleanedPayload, itemResult.ParseStatus, nullableString(itemResult.ParseError), resultFileID,
			nullableInt(itemResult.AnswerSheetRowIndex), itemResult.ReferenceCount, nullableString(itemResult.RowHash))
		if err != nil {
			return err
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
  select
    count(ti.id)::int as total_count,
    count(ti.id) filter (
      where exists (
        select 1 from cdp_task_item_results tir
        where tir.task_item_id=ti.id and tir.parse_status='SUCCESS'
      )
    )::int as success_count,
    count(ti.id) filter (
      where not exists (
        select 1 from cdp_task_item_results tir
        where tir.task_item_id=ti.id and tir.parse_status='SUCCESS'
      )
      and exists (
        select 1 from cdp_task_item_results tir
        where tir.task_item_id=ti.id and tir.parse_status <> 'SUCCESS'
      )
    )::int as failed_count
  from cdp_task_items ti
  where ti.task_id=$1
)
update cdp_tasks
set result_payload=$2,
  result_imported_at=now(),
  result_row_count=(select count(*) from cdp_task_result_rows where task_id=$1),
  total_count=item_result_counts.total_count,
  success_count=item_result_counts.success_count,
  failed_count=item_result_counts.failed_count,
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
	_, err := r.db.Exec(ctx, `
with readiness as (
  select
    t.id,
    t.status,
    nullif(t.provider_response #>> '{providerCallback,totalCount}', '')::int as provider_total_count,
    nullif(t.provider_response #>> '{providerCallback,successCount}', '')::int as provider_success_count,
    nullif(t.provider_response #>> '{providerCallback,failedCount}', '')::int as provider_failed_count,
    count(ti.id)::int as item_count,
    count(ti.id) filter (where ti.result_status='IMPORTED')::int as imported_item_count,
    count(ti.id) filter (
      where exists (
        select 1 from cdp_task_item_results tir
        where tir.task_item_id=ti.id and tir.parse_status='SUCCESS'
      )
    )::int as success_count,
    count(ti.id) filter (
      where not exists (
        select 1 from cdp_task_item_results tir
        where tir.task_item_id=ti.id and tir.parse_status='SUCCESS'
      )
      and exists (
        select 1 from cdp_task_item_results tir
        where tir.task_item_id=ti.id and tir.parse_status <> 'SUCCESS'
      )
    )::int as failed_count
  from cdp_tasks t
  left join cdp_task_items ti on ti.task_id=t.id
  where t.id=$1
  group by t.id
),
decision as (
  select
    id,
    item_count,
    success_count,
    failed_count,
    (
      $2
      and status=$3
      and item_count > 0
      and imported_item_count = item_count
      and success_count + failed_count = item_count
      and coalesce(provider_total_count, item_count) = item_count
      and coalesce(provider_success_count, success_count) + coalesce(provider_failed_count, failed_count) = coalesce(provider_total_count, item_count)
    ) as callback_ready
  from readiness
)
update cdp_tasks t
set status=case
      when t.status in ($4,$5) then t.status
      when decision.callback_ready then $4
      else $6
    end,
    total_count=decision.item_count,
	    success_count=decision.success_count,
	    failed_count=decision.failed_count,
	    result_imported_at=coalesce(t.result_imported_at, now()),
	    updated_at=now()
from decision
where t.id=decision.id
	`, taskID, notifyCallback, biz.TaskStatusProviderDone, biz.TaskStatusCallbackPending, biz.TaskStatusDone, biz.TaskStatusResultImported)
	if err != nil {
		return nil, false, err
	}
	task, err := r.GetTask(ctx, taskID)
	if err != nil {
		return nil, false, err
	}
	var callbackReady bool
	if err := r.db.QueryRow(ctx, `
with readiness as (
  select
    t.status,
    nullif(t.provider_response #>> '{providerCallback,totalCount}', '')::int as provider_total_count,
    nullif(t.provider_response #>> '{providerCallback,successCount}', '')::int as provider_success_count,
    nullif(t.provider_response #>> '{providerCallback,failedCount}', '')::int as provider_failed_count,
    count(ti.id)::int as item_count,
    count(ti.id) filter (where ti.result_status='IMPORTED')::int as imported_item_count,
    count(ti.id) filter (
      where exists (
        select 1 from cdp_task_item_results tir
        where tir.task_item_id=ti.id and tir.parse_status='SUCCESS'
      )
    )::int as success_count,
    count(ti.id) filter (
      where not exists (
        select 1 from cdp_task_item_results tir
        where tir.task_item_id=ti.id and tir.parse_status='SUCCESS'
      )
      and exists (
        select 1 from cdp_task_item_results tir
        where tir.task_item_id=ti.id and tir.parse_status <> 'SUCCESS'
      )
    )::int as failed_count
  from cdp_tasks t
  left join cdp_task_items ti on ti.task_id=t.id
  where t.id=$1
  group by t.id
)
select
  $2
  and status=$3
  and item_count > 0
  and imported_item_count = item_count
  and success_count + failed_count = item_count
  and coalesce(provider_total_count, item_count) = item_count
  and coalesce(provider_success_count, success_count) + coalesce(provider_failed_count, failed_count) = coalesce(provider_total_count, item_count)
from readiness
	`, taskID, notifyCallback, biz.TaskStatusCallbackPending).Scan(&callbackReady); err != nil {
		return nil, false, err
	}
	return task, callbackReady, nil
}

func (r *TaskRepo) MarkCallbackDone(ctx context.Context, taskID int64, targetURL string, statusCode int, responseBody string, payload json.RawMessage) error {
	tx, err := r.db.Begin(ctx)
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
	tx, err := r.db.Begin(ctx)
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

func (r *TaskRepo) AddEvent(ctx context.Context, taskID int64, eventType biz.EventType, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		body = []byte(`{}`)
	}
	_, err = r.db.Exec(ctx, `insert into cdp_task_events (task_id,event_type,payload) values ($1,$2,$3)`, taskID, eventType, body)
	return err
}

type taskScanner interface {
	Scan(dest ...any) error
}

func taskSelectSQL() string {
	return `select id,coalesce(job_id,''),coalesce(platform_user_id,0),coalesce(platform_code,''),batch_no,provider_code,
coalesce(gateway_code,''),coalesce(source,''),coalesce(external_batch_id,''),coalesce(provider_external_batch_id,''),biz_date,status,
coalesce(callback_url,''),coalesce(provider_callback_url,''),raw_request,coalesce(provider_request,'{}'::jsonb),
coalesce(provider_response,'{}'::jsonb),coalesce(result_payload,'{}'::jsonb),coalesce(result_object_key,''),
coalesce(result_file_name,''),result_row_count,total_count,success_count,failed_count,coalesce(last_error,''),created_at,updated_at from cdp_tasks`
}

func (r *TaskRepo) scanTask(scanner taskScanner) (*biz.Task, error) {
	var task biz.Task
	if err := scanner.Scan(&task.ID, &task.JobID, &task.PlatformUserID, &task.PlatformCode, &task.BatchNo, &task.ProviderCode,
		&task.GatewayCode, &task.Source, &task.ExternalBatchID, &task.ProviderExternalBatchID, &task.BizDate,
		&task.Status, &task.CallbackURL, &task.ProviderCallbackURL, &task.RawRequest, &task.ProviderRequest, &task.ProviderResponse,
		&task.ResultPayload, &task.ResultObjectKey, &task.ResultFileName, &task.ResultRowCount, &task.TotalCount, &task.SuccessCount,
		&task.FailedCount, &task.LastError, &task.CreatedAt, &task.UpdatedAt); err != nil {
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
