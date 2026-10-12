package data

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const adminMetricDateSQL = "to_char(t.created_at AT TIME ZONE 'Asia/Shanghai', 'YYYY-MM-DD')"
const adminTaskExecutionEndedAtSQL = "t.provider_finished_at"
const adminTaskSubmitFailedSQL = `exists (
  select 1
  from cdp_task_events submit_failed_event
  where submit_failed_event.task_id=t.id
    and submit_failed_event.event_type='PROVIDER_SUBMIT_FAILED'
)`
const adminTaskDispatchLabelsSQL = `coalesce((
  select array_agg(distinct dispatch_label.label order by dispatch_label.label)
  from cdp_task_events dispatch_event
  cross join lateral jsonb_array_elements_text(
    case
      when jsonb_typeof(dispatch_event.payload->'labels') = 'array' then dispatch_event.payload->'labels'
      else '[]'::jsonb
    end
  ) as dispatch_label(label)
  where dispatch_event.task_id=t.id
    and dispatch_event.event_type='TASK_DISPATCHED'
), array[]::text[])`
const adminTaskReassignedToTaskIDSQL = `coalesce((
  select child.id
  from cdp_tasks child
  where child.reassigned_from_task_id=t.id
    and child.deleted_at is null
  order by child.created_at desc, child.id desc
  limit 1
), 0)`
const adminTaskSummarySelectSQL = `
  t.id,
  t.batch_no,
  coalesce(t.task_name,''),
  coalesce(t.source,''),
  coalesce(t.platform_code,''),
  coalesce(t.external_batch_id,''),
  t.provider_code,
  t.status,
  admin_task_progress.total_count,
  admin_task_progress.completed_count,
  admin_task_progress.success_count,
  admin_task_progress.failed_count,
  admin_task_progress.result_success_count,
  admin_task_progress.result_failed_count,
  admin_task_progress.result_missing_count,
  t.created_at,
  t.updated_at,
  ` + adminTaskExecutionEndedAtSQL + `,
  ` + adminTaskSubmitFailedSQL + `,
  ` + adminTaskDispatchLabelsSQL + `,
  coalesce(t.reassigned_from_task_id,0),
  ` + adminTaskReassignedToTaskIDSQL + `,t.risk_count,t.invalid_answer_count,t.risk_summary,t.risk_stats_updated_at,t.device,t.execution_mode,
 (select count(*) from cdp_task_shards where task_id=t.id),
 (select count(*) from cdp_task_shards where task_id=t.id and status='COMPLETED'),
 (select count(*) from cdp_task_result_submissions where task_id=t.id and status in ('ACCEPTED','PROCESSING','RETRY_WAIT')),
 (select count(*) from cdp_task_result_submissions where task_id=t.id and status='FAILED')`

var adminTaskProgressJoinSQL = `
left join lateral (
` + taskItemStatsSQL("t.id") + `
) admin_task_item_progress on true
left join lateral (
  select
    greatest(
      coalesce(nullif(t.provider_response #>> '{data,successCount}', '')::int, 0)
        + coalesce(nullif(t.provider_response #>> '{data,failedCount}', '')::int, 0),
      coalesce(nullif(t.provider_response #>> '{providerCallback,successCount}', '')::int, 0)
        + coalesce(nullif(t.provider_response #>> '{providerCallback,failedCount}', '')::int, 0),
      coalesce(nullif(t.provider_response #>> '{providerCallbackProgress,successCount}', '')::int, 0)
        + coalesce(nullif(t.provider_response #>> '{providerCallbackProgress,failedCount}', '')::int, 0),
      coalesce(admin_task_item_progress.provider_done_count, 0),
      case
        when t.result_imported_at is null then coalesce(t.success_count, 0) + coalesce(t.failed_count, 0)
        else 0
      end
    ) as raw_completed_count,
    greatest(
      coalesce(nullif(t.provider_response #>> '{data,failedCount}', '')::int, 0),
      coalesce(nullif(t.provider_response #>> '{providerCallback,failedCount}', '')::int, 0),
      coalesce(nullif(t.provider_response #>> '{providerCallbackProgress,failedCount}', '')::int, 0),
      case
        when t.result_imported_at is null then coalesce(t.failed_count, 0)
        else 0
      end
    ) as raw_failed_count,
    greatest(
      coalesce(nullif(t.provider_response #>> '{data,totalCount}', '')::int, 0),
      coalesce(nullif(t.provider_response #>> '{providerCallback,totalCount}', '')::int, 0),
      coalesce(nullif(t.provider_response #>> '{providerCallbackProgress,totalCount}', '')::int, 0),
      coalesce(admin_task_item_progress.item_count, 0),
      coalesce(t.total_count, 0)
    ) as raw_total_count
) admin_task_progress_source on true
left join lateral (
  select
    greatest(admin_task_progress_source.raw_total_count, 0) as total_count,
    least(
      greatest(admin_task_progress_source.raw_completed_count, 0),
      greatest(admin_task_progress_source.raw_total_count, 0)
    ) as completed_count
) admin_task_progress_bounds on true
left join lateral (
  select
    admin_task_progress_bounds.total_count,
    admin_task_progress_bounds.completed_count,
    least(
      greatest(admin_task_progress_source.raw_failed_count, 0),
      admin_task_progress_bounds.completed_count
    ) as raw_failed_count
) admin_task_progress_failure_source on true
left join lateral (
  select
    admin_task_progress_failure_source.total_count,
    admin_task_progress_failure_source.completed_count,
    least(
      admin_task_progress_failure_source.raw_failed_count,
      admin_task_progress_bounds.completed_count
    ) as failed_count
) admin_task_progress_failure on true
left join lateral (
  select
    admin_task_progress_failure.total_count,
    case when t.execution_mode='pull' then coalesce(admin_task_item_progress.result_success_count,0)+coalesce(admin_task_item_progress.result_failed_count,0) else admin_task_progress_failure.completed_count end as completed_count,
 case when t.execution_mode='pull' then coalesce(admin_task_item_progress.result_success_count,0) else admin_task_progress_failure.completed_count - admin_task_progress_failure.failed_count end as success_count,
 case when t.execution_mode='pull' then coalesce(admin_task_item_progress.result_failed_count,0) else admin_task_progress_failure.failed_count end as failed_count,
    coalesce(admin_task_item_progress.result_success_count, 0) as result_success_count,
    coalesce(admin_task_item_progress.result_failed_count, 0) as result_failed_count,
    case
      when t.result_imported_at is null and t.execution_mode='push' then 0
      else greatest(
        coalesce(admin_task_item_progress.item_count, 0)
          - coalesce(admin_task_item_progress.result_success_count, 0)
          - coalesce(admin_task_item_progress.result_failed_count, 0),
        0
      )
    end as result_missing_count
) admin_task_progress on true`

type AdminRepo struct {
	db *pgxpool.Pool
}

func NewAdminRepo(db *pgxpool.Pool) *AdminRepo {
	return &AdminRepo{db: db}
}

func (r *AdminRepo) ListTasks(ctx context.Context, filter biz.AdminTaskFilter) (biz.AdminTaskList, error) {
	normalizeTaskFilter(&filter)
	where, args := adminTaskWhere(filter)

	var total int
	if err := r.db.QueryRow(ctx, `select count(*) from cdp_tasks t `+where, args...).Scan(&total); err != nil {
		return biz.AdminTaskList{}, err
	}

	limit := filter.PageSize
	offset := (filter.Page - 1) * filter.PageSize
	queryArgs := append(append([]any{}, args...), limit, offset)
	rows, err := r.db.Query(ctx, `
with page_tasks as materialized (
  select t.id, t.batch_no, t.task_name, t.source, t.platform_code,
    t.external_batch_id, t.provider_code, t.status, t.provider_response,
    t.total_count, t.success_count, t.failed_count, t.result_imported_at,
    t.created_at, t.updated_at, t.provider_finished_at, t.reassigned_from_task_id,
 t.risk_count,t.invalid_answer_count,t.risk_summary,t.risk_stats_updated_at,t.device,t.execution_mode
  from cdp_tasks t
`+where+`
  order by t.created_at desc, t.id desc
  limit $`+strconv.Itoa(len(args)+1)+` offset $`+strconv.Itoa(len(args)+2)+`
)
select
`+adminTaskSummarySelectSQL+`
from page_tasks t
`+adminTaskProgressJoinSQL+`
order by t.created_at desc, t.id desc`, queryArgs...)
	if err != nil {
		return biz.AdminTaskList{}, err
	}
	defer rows.Close()

	out := biz.AdminTaskList{
		Total:    total,
		Page:     filter.Page,
		PageSize: filter.PageSize,
	}
	for rows.Next() {
		summary, err := scanAdminTaskSummary(rows)
		if err != nil {
			return biz.AdminTaskList{}, err
		}
		out.Tasks = append(out.Tasks, summary)
	}
	return out, rows.Err()
}

func (r *AdminRepo) GetTaskDetail(ctx context.Context, id int64, filter biz.AdminTaskDetailFilter) (biz.AdminTaskDetail, error) {
	normalizeTaskItemFilter(&filter.Items)
	normalizePaginationFilter(&filter.Files)
	normalizePaginationFilter(&filter.Events)
	summary, err := r.getTaskSummary(ctx, id)
	if err != nil {
		return biz.AdminTaskDetail{}, err
	}

	items, itemTotal, err := r.listTaskItems(ctx, id, filter.Items)
	if err != nil {
		return biz.AdminTaskDetail{}, err
	}
	files, fileTotal, err := r.listResultFiles(ctx, id, filter.Files)
	if err != nil {
		return biz.AdminTaskDetail{}, err
	}
	events, eventTotal, err := r.listTaskEvents(ctx, id, filter.Events)
	if err != nil {
		return biz.AdminTaskDetail{}, err
	}
	callbacks, err := r.listTaskCallbacks(ctx, id)
	if err != nil {
		return biz.AdminTaskDetail{}, err
	}
	return biz.AdminTaskDetail{
		Task:       summary,
		Items:      items,
		ItemTotal:  itemTotal,
		ItemPage:   filter.Items.Page,
		ItemSize:   filter.Items.PageSize,
		Files:      files,
		FileTotal:  fileTotal,
		FilePage:   filter.Files.Page,
		FileSize:   filter.Files.PageSize,
		Events:     events,
		EventTotal: eventTotal,
		EventPage:  filter.Events.Page,
		EventSize:  filter.Events.PageSize,
		Callbacks:  callbacks,
	}, nil
}

func (r *AdminRepo) ExportTaskItems(ctx context.Context, id int64) (biz.AdminTaskItemsExport, error) {
	summary, err := r.getTaskSummary(ctx, id)
	if err != nil {
		return biz.AdminTaskItemsExport{}, err
	}
	items, err := r.listAllTaskItems(ctx, id)
	if err != nil {
		return biz.AdminTaskItemsExport{}, err
	}
	return biz.AdminTaskItemsExport{Task: summary, Items: items}, nil
}

func (r *AdminRepo) SoftDeleteSubmitFailedTask(ctx context.Context, id int64, deletedBy string) (biz.AdminTaskSummary, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return biz.AdminTaskSummary{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	summary, err := scanAdminTaskSummary(tx.QueryRow(ctx, `
with locked_task as (
  select *
  from cdp_tasks
  where id=$1
    and deleted_at is null
  for update
)
select
`+adminTaskSummarySelectSQL+`
from locked_task t
`+adminTaskProgressJoinSQL+`
`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.AdminTaskSummary{}, biz.ErrTaskNotFound
	}
	if err != nil {
		return biz.AdminTaskSummary{}, err
	}
	if summary.Status != biz.TaskStatusFailed || !summary.SubmitFailed {
		return biz.AdminTaskSummary{}, fmt.Errorf("%w: only submit failed tasks can be deleted", biz.ErrInvalidTaskRequest)
	}

	if _, err = tx.Exec(ctx, `
update cdp_tasks
set deleted_at=now(),
  deleted_by=$2,
  updated_at=now()
where id=$1
`, id, strings.TrimSpace(deletedBy)); err != nil {
		return biz.AdminTaskSummary{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return biz.AdminTaskSummary{}, err
	}
	return summary, nil
}

func (r *AdminRepo) getTaskSummary(ctx context.Context, id int64) (biz.AdminTaskSummary, error) {
	summary, err := scanAdminTaskSummary(r.db.QueryRow(ctx, `
select
`+adminTaskSummarySelectSQL+`
from cdp_tasks t
`+adminTaskProgressJoinSQL+`
where t.id=$1
  and t.deleted_at is null
	`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.AdminTaskSummary{}, biz.ErrTaskNotFound
	}
	if err != nil {
		return biz.AdminTaskSummary{}, err
	}
	return summary, nil
}

func (r *AdminRepo) ListPlatformUsers(ctx context.Context, filter biz.AdminPlatformUserFilter) (biz.AdminPlatformUserList, error) {
	normalizePlatformUserFilter(&filter)
	where, args := adminPlatformUserWhere(filter)

	var total int
	if err := r.db.QueryRow(ctx, `select count(*) from platform_users u `+where, args...).Scan(&total); err != nil {
		return biz.AdminPlatformUserList{}, err
	}

	limit := filter.PageSize
	offset := (filter.Page - 1) * filter.PageSize
	queryArgs := append(append([]any{}, args...), limit, offset)
	rows, err := r.db.Query(ctx, `
select
  u.id,u.platform_code,u.name,u.api_key,u.status,u.callback_mode,coalesce(u.callback_url,''),u.callback_config,u.created_at,u.updated_at,
  coalesce(dp.id,0), coalesce(dp.platform_user_id,0), coalesce(dp.code,''), coalesce(dp.name,''), coalesce(dp.strategy,''), coalesce(dp.enabled,false),
  coalesce(dp.cursor,0), coalesce(dp.last_provider_code,''), coalesce(dp.config,'{}'::jsonb), dp.created_at, dp.updated_at
from platform_users u
left join cdp_dispatch_profiles dp on dp.platform_user_id=u.id
`+where+`
order by u.created_at desc
limit $`+strconv.Itoa(len(args)+1)+` offset $`+strconv.Itoa(len(args)+2), queryArgs...)
	if err != nil {
		return biz.AdminPlatformUserList{}, err
	}
	defer rows.Close()

	out := biz.AdminPlatformUserList{Total: total, Page: filter.Page, PageSize: filter.PageSize}
	for rows.Next() {
		user, err := scanPlatformUser(rows)
		if err != nil {
			return biz.AdminPlatformUserList{}, err
		}
		out.Users = append(out.Users, user)
	}
	return out, rows.Err()
}

func (r *AdminRepo) GetPlatformUser(ctx context.Context, id int64) (biz.PlatformUser, error) {
	user, err := scanPlatformUser(r.db.QueryRow(ctx, `
select
  u.id,u.platform_code,u.name,u.api_key,u.status,u.callback_mode,coalesce(u.callback_url,''),u.callback_config,u.created_at,u.updated_at,
  coalesce(dp.id,0), coalesce(dp.platform_user_id,0), coalesce(dp.code,''), coalesce(dp.name,''), coalesce(dp.strategy,''), coalesce(dp.enabled,false),
  coalesce(dp.cursor,0), coalesce(dp.last_provider_code,''), coalesce(dp.config,'{}'::jsonb), dp.created_at, dp.updated_at
from platform_users u
left join cdp_dispatch_profiles dp on dp.platform_user_id=u.id
where u.id=$1
`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.PlatformUser{}, biz.ErrPlatformUserNotFound
	}
	return user, err
}

func (r *AdminRepo) GetPlatformUserMetrics(ctx context.Context, id int64, filter biz.AdminPlatformUserMetricsFilter) (biz.AdminPlatformUserMetrics, error) {
	user, err := r.GetPlatformUser(ctx, id)
	if err != nil {
		return biz.AdminPlatformUserMetrics{}, err
	}
	where, args := adminPlatformUserMetricWhere(id, user.PlatformCode, filter)
	metrics := biz.AdminPlatformUserMetrics{
		User:        user,
		CreatedFrom: filter.CreatedFrom,
		CreatedTo:   filter.CreatedTo,
	}

	if err := r.scanPlatformTaskMetrics(ctx, where, args, &metrics); err != nil {
		return biz.AdminPlatformUserMetrics{}, err
	}
	if err := r.scanPlatformQuestionMetrics(ctx, where, args, &metrics); err != nil {
		return biz.AdminPlatformUserMetrics{}, err
	}
	if err := r.scanPlatformResultMetrics(ctx, where, args, &metrics); err != nil {
		return biz.AdminPlatformUserMetrics{}, err
	}
	if err := r.scanPlatformCallbackMetrics(ctx, where, args, &metrics); err != nil {
		return biz.AdminPlatformUserMetrics{}, err
	}

	metrics.StatusBreakdown, err = r.listPlatformStatusBreakdown(ctx, where, args, metrics.TaskMetrics.SubmittedTaskCount)
	if err != nil {
		return biz.AdminPlatformUserMetrics{}, err
	}
	metrics.FailureReasons, err = r.listPlatformFailureReasons(ctx, where, args, metrics.TaskMetrics.FailedTaskCount)
	if err != nil {
		return biz.AdminPlatformUserMetrics{}, err
	}
	metrics.RecentTasks, err = r.listPlatformRecentTasks(ctx, where, args)
	if err != nil {
		return biz.AdminPlatformUserMetrics{}, err
	}
	metrics.ProviderDaily, err = r.listPlatformProviderDailyMetrics(ctx, where, args)
	if err != nil {
		return biz.AdminPlatformUserMetrics{}, err
	}
	return metrics, nil
}

func (r *AdminRepo) ExportPlatformUserReconciliation(ctx context.Context, id int64, filter biz.AdminPlatformUserReconciliationFilter) (biz.AdminPlatformUserReconciliationExport, error) {
	user, err := r.GetPlatformUser(ctx, id)
	if err != nil {
		return biz.AdminPlatformUserReconciliationExport{}, err
	}
	where, args := adminPlatformUserReconciliationWhere(id, user.PlatformCode, filter)
	rows, err := r.db.Query(ctx, `
select
  `+adminMetricDateSQL+` as metric_date,
  coalesce(t.task_name, ''),
  t.batch_no,
  t.provider_code,
  t.total_count
from cdp_tasks t
`+where+`
order by t.created_at asc, t.id asc
`, args...)
	if err != nil {
		return biz.AdminPlatformUserReconciliationExport{}, err
	}
	defer rows.Close()
	out := biz.AdminPlatformUserReconciliationExport{
		User:        user,
		CreatedFrom: filter.CreatedFrom,
		CreatedTo:   filter.CreatedTo,
	}
	for rows.Next() {
		var item biz.AdminPlatformUserReconciliationRow
		if err := rows.Scan(
			&item.MetricDate,
			&item.TaskName,
			&item.BatchNo,
			&item.ProviderCode,
			&item.QuestionCount,
		); err != nil {
			return biz.AdminPlatformUserReconciliationExport{}, err
		}
		out.Rows = append(out.Rows, item)
	}
	return out, rows.Err()
}

func (r *AdminRepo) GetProviderMetrics(ctx context.Context, providerCode string, filter biz.AdminProviderMetricsFilter) (biz.AdminProviderMetrics, error) {
	metrics := biz.AdminProviderMetrics{
		ProviderCode: providerCode,
		CreatedFrom:  filter.CreatedFrom,
		CreatedTo:    filter.CreatedTo,
	}
	where, args := adminProviderMetricWhere(providerCode, filter)
	if err := r.db.QueryRow(ctx, `
select
	  count(distinct t.id),
	  coalesce(sum(item_stats.question_count), 0),
	  coalesce(sum(coalesce(result_stats.success_answer_count, 0)), 0),
	  coalesce(sum(greatest(item_stats.question_count - coalesce(result_stats.success_answer_count, 0), 0)), 0)
from cdp_tasks t
left join lateral (
  select count(*) as question_count
  from cdp_task_items ti
  where ti.task_id=t.id
) item_stats on true
left join lateral (
  select count(distinct tir.task_item_id) filter (where tir.parse_status='SUCCESS') as success_answer_count
  from cdp_task_item_results tir
  where tir.task_id=t.id
) result_stats on true
	`+where, args...).Scan(
		&metrics.Summary.TaskCount,
		&metrics.Summary.QuestionCount,
		&metrics.Summary.SuccessAnswerCount,
		&metrics.Summary.FailedAnswerCount,
	); err != nil {
		return biz.AdminProviderMetrics{}, err
	}
	if metrics.Summary.QuestionCount > 0 {
		metrics.Summary.AnswerSuccessRate = float64(metrics.Summary.SuccessAnswerCount) / float64(metrics.Summary.QuestionCount)
	}
	daily, err := r.listProviderDailyMetrics(ctx, where, args)
	if err != nil {
		return biz.AdminProviderMetrics{}, err
	}
	metrics.Daily = daily
	return metrics, nil
}

func (r *AdminRepo) ListProviderQuotaUsage(ctx context.Context, providerCodes []string) (map[string]biz.AdminProviderQuotaUsage, error) {
	codes := cleanProviderCodes(providerCodes)
	usage := make(map[string]biz.AdminProviderQuotaUsage, len(codes))
	for _, code := range codes {
		usage[code] = biz.AdminProviderQuotaUsage{ProviderCode: code}
	}
	if len(codes) == 0 {
		return usage, nil
	}
	rows, err := r.db.Query(ctx, `
select provider_code, coalesce(sum(total_count), 0)::int
from cdp_tasks
where provider_code = any($1::text[])
  and quota_date = (now() at time zone 'Asia/Shanghai')::date
  and deleted_at is null
  and (
    status = any($2::text[])
    or (status='FAILED' and provider_external_batch_id is not null)
  )
group by provider_code
`, codes, []string{
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
		var item biz.AdminProviderQuotaUsage
		if err := rows.Scan(&item.ProviderCode, &item.Used); err != nil {
			return nil, err
		}
		usage[item.ProviderCode] = item
	}
	return usage, rows.Err()
}

func (r *AdminRepo) CreatePlatformUser(ctx context.Context, input biz.AdminPlatformUserInput) (biz.PlatformUser, error) {
	normalizePlatformUserInput(&input)
	apiKey, err := newAdminAPIKey()
	if err != nil {
		return biz.PlatformUser{}, err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return biz.PlatformUser{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	user, err := scanPlatformUser(tx.QueryRow(ctx, `
insert into platform_users (
  platform_code,name,api_key,status,default_provider_code,callback_mode,callback_url,callback_config
) values ($1,$2,$3,$4,null,$5,$6,'{}'::jsonb)
returning id,platform_code,name,api_key,status,callback_mode,coalesce(callback_url,''),callback_config,created_at,updated_at,
  0::bigint, 0::bigint, ''::text, ''::text, ''::text, false, 0::bigint, ''::text, '{}'::jsonb, null::timestamptz, null::timestamptz
`, input.PlatformCode, input.Name, apiKey, input.Status, input.CallbackMode, nullableAdminString(input.CallbackURL)))
	if err != nil {
		return biz.PlatformUser{}, mapPlatformUserWriteError(err)
	}
	if err := r.upsertDispatchProfile(ctx, tx, user.ID, user.PlatformCode, input.DispatchProfile); err != nil {
		return biz.PlatformUser{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return biz.PlatformUser{}, err
	}
	return r.GetPlatformUser(ctx, user.ID)
}

func (r *AdminRepo) UpdatePlatformUser(ctx context.Context, id int64, input biz.AdminPlatformUserInput) (biz.PlatformUser, error) {
	normalizePlatformUserInput(&input)
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return biz.PlatformUser{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	user, err := scanPlatformUser(tx.QueryRow(ctx, `
update platform_users set
  platform_code=$2,
  name=$3,
  status=$4,
  callback_mode=$5,
  callback_url=$6,
  updated_at=now()
where id=$1
returning id,platform_code,name,api_key,status,callback_mode,coalesce(callback_url,''),callback_config,created_at,updated_at,
  0::bigint, 0::bigint, ''::text, ''::text, ''::text, false, 0::bigint, ''::text, '{}'::jsonb, null::timestamptz, null::timestamptz
`, id, input.PlatformCode, input.Name, input.Status, input.CallbackMode, nullableAdminString(input.CallbackURL)))
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.PlatformUser{}, biz.ErrPlatformUserNotFound
	}
	if err != nil {
		return biz.PlatformUser{}, mapPlatformUserWriteError(err)
	}
	if err := r.upsertDispatchProfile(ctx, tx, user.ID, user.PlatformCode, input.DispatchProfile); err != nil {
		return biz.PlatformUser{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return biz.PlatformUser{}, err
	}
	return r.GetPlatformUser(ctx, user.ID)
}

func (r *AdminRepo) upsertDispatchProfile(ctx context.Context, tx pgx.Tx, platformUserID int64, platformCode string, input biz.DispatchProfileInput) error {
	providerCodes := cleanProviderCodes(input.ProviderCodes)
	if len(providerCodes) == 0 {
		return fmt.Errorf("%w: providerCodes are required", biz.ErrInvalidTaskRequest)
	}
	if input.Strategy == "" {
		input.Strategy = biz.DispatchStrategyRoundRobin
	}
	if input.Strategy != biz.DispatchStrategyRoundRobin && input.Strategy != biz.DispatchStrategyRuleBased {
		return fmt.Errorf("%w: unsupported dispatch strategy", biz.ErrInvalidTaskRequest)
	}
	code := strings.TrimSpace(input.Code)
	if code == "" {
		code = platformCode + "_default"
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = platformCode + " 默认调度"
	}
	config, err := dispatchConfigJSON(providerCodes, input.FallbackSelectionPolicy, input.Rules)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
insert into cdp_dispatch_profiles (
  platform_user_id, code, name, strategy, enabled, config
) values ($1,$2,$3,$4,$5,$6)
on conflict (platform_user_id) do update set
  code=excluded.code,
  name=excluded.name,
  strategy=excluded.strategy,
  enabled=excluded.enabled,
  config=excluded.config,
  updated_at=now()
`, platformUserID, code, name, input.Strategy, input.Enabled, config)
	return err
}

func (r *AdminRepo) AddAuditLog(ctx context.Context, log biz.AdminAuditLog) error {
	if len(log.Payload) == 0 {
		log.Payload = json.RawMessage(`{}`)
	}
	_, err := r.db.Exec(ctx, `
insert into cdp_admin_audit_logs (
  user_id, username, role, action, resource_type, resource_id, request_id, payload
) values ($1,$2,$3,$4,$5,$6,$7,$8)
`, log.UserID, log.Username, log.Role, log.Action, log.ResourceType, log.ResourceID, log.RequestID, log.Payload)
	return err
}

func (r *AdminRepo) listTaskItems(ctx context.Context, taskID int64, filter biz.AdminTaskItemFilter) ([]biz.AdminTaskItem, int, error) {
	var total int
	if err := r.db.QueryRow(ctx, `
select coalesce(sum(expected_count), 0)
from (
  select case when (select execution_mode from cdp_tasks where id=$1)='pull' then greatest(count(tir.id),1) else greatest(coalesce(max(tir.seq), 0), 1) end as expected_count
  from cdp_task_items ti
  left join cdp_task_item_results tir on tir.task_id=$1 and tir.task_item_id=ti.id
  where ti.task_id=$1
  group by ti.id
) item_bounds
	`, taskID).Scan(&total); err != nil {
		return nil, 0, err
	}
	offset := (filter.Page - 1) * filter.PageSize
	rows, err := r.db.Query(ctx, `
with result_statuses as materialized (
  select task_item_id, seq, parse_status
  from cdp_task_item_results where task_id=$1
),
item_bounds as (
  select ti.id, ti.status, ti.result_status,
    greatest(coalesce(max(tir.seq), 0), 1) as expected_count
  from cdp_task_items ti
  left join result_statuses tir on tir.task_item_id=ti.id
  where ti.task_id=$1
  group by ti.id
),
expanded as (
  select ti.id, ti.status, expected.seq as result_seq,
    case
      when tir.parse_status='SUCCESS' then 'IMPORTED'
      when tir.parse_status is not null then tir.parse_status
      when ti.result_status='IMPORTED' then 'PENDING'
      else ti.result_status
    end as result_status
  from item_bounds ti
  cross join lateral (
 select generate_series(1,ti.expected_count) seq where (select execution_mode from cdp_tasks where id=$1)='push'
 union all select r.seq from cdp_task_item_results r where r.task_item_id=ti.id and (select execution_mode from cdp_tasks where id=$1)='pull'
 union all select 1 where (select execution_mode from cdp_tasks where id=$1)='pull' and not exists(select 1 from cdp_task_item_results r where r.task_item_id=ti.id)
 ) as expected
  left join result_statuses tir on tir.task_item_id=ti.id and tir.seq=expected.seq
),
page_items as materialized (
  select id, result_seq, result_status,
    case
      when result_status in ('FAILED','PARSE_FAILED','ERROR') then 0
      when result_status <> 'IMPORTED' or status <> 2 then 1
      else 2
    end as priority
  from expanded
  order by priority, id, result_seq
  limit $2 offset $3
)
select
	  ti.id,
	  ti.item_id,
	  ti.platform_code,
	  page.result_seq,
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
	  coalesce(tir.parse_error,''),
	  coalesce(tir.updated_at, ti.result_imported_at),
 coalesce(tir.validation_status,'UNKNOWN'),coalesce(tir.risk_items,'[]'::jsonb),coalesce(tir.validation_rule_version,''),tir.validated_at
from page_items page
join cdp_task_items ti on ti.id=page.id and ti.task_id=$1
left join cdp_task_item_results tir on tir.task_id=$1 and tir.task_item_id=page.id and tir.seq=page.result_seq
order by page.priority, page.id, page.result_seq
	`, taskID, filter.PageSize, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var items []biz.AdminTaskItem
	for rows.Next() {
		var item biz.AdminTaskItem
		var generatedAt sql.NullTime
		var resultImportedAt sql.NullTime
		var validatedAt *time.Time
		var riskJSON []byte
		if err := rows.Scan(
			&item.TaskItemID,
			&item.ItemID,
			&item.PlatformCode,
			&item.Seq,
			&item.Question,
			&item.ProviderStatus,
			&item.ResultStatus,
			&item.AnswerText,
			&item.ThinkingProcess,
			&generatedAt,
			&item.GeneratedAtText,
			&item.RecommendedJSON,
			&item.DeviceID,
			&item.RecordingURL,
			&item.ReferencesJSON,
			&item.ReferenceCount,
			&item.ParseStatus,
			&item.ParseError,
			&resultImportedAt,
			&item.AnswerValidation.Status, &riskJSON, &item.RuleVersion, &validatedAt,
		); err != nil {
			return nil, 0, err
		}
		if generatedAt.Valid {
			item.GeneratedAt = generatedAt.Time
		}
		if resultImportedAt.Valid {
			item.ResultImportedAt = resultImportedAt.Time
		}
		if err := json.Unmarshal(riskJSON, &item.RiskItems); err != nil {
			return nil, 0, err
		}
		if validatedAt != nil {
			item.ValidatedAt = *validatedAt
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}

func (r *AdminRepo) listAllTaskItems(ctx context.Context, taskID int64) ([]biz.AdminTaskItem, error) {
	rows, err := r.db.Query(ctx, `
with item_bounds as (
  select ti.*, greatest(coalesce(max(tir.seq), 0), 1) as expected_count
  from cdp_task_items ti
  left join cdp_task_item_results tir on tir.task_id=$1 and tir.task_item_id=ti.id
  where ti.task_id=$1
  group by ti.id
),
expanded as (
  select ti.*, expected.seq as result_seq
  from item_bounds ti
  cross join lateral (
 select generate_series(1,ti.expected_count) seq where (select execution_mode from cdp_tasks where id=$1)='push'
 union all select r.seq from cdp_task_item_results r where r.task_item_id=ti.id and (select execution_mode from cdp_tasks where id=$1)='pull'
 union all select 1 where (select execution_mode from cdp_tasks where id=$1)='pull' and not exists(select 1 from cdp_task_item_results r where r.task_item_id=ti.id)
 ) as expected
)
select
	  ti.id,
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
	  coalesce(tir.parse_error,''),
	  coalesce(tir.updated_at, ti.result_imported_at),
 coalesce(tir.validation_status,'UNKNOWN'),coalesce(tir.risk_items,'[]'::jsonb),coalesce(tir.validation_rule_version,''),tir.validated_at
from expanded ti
left join cdp_task_item_results tir on tir.task_id=$1 and tir.task_item_id=ti.id and tir.seq=ti.result_seq
order by
	  case
	    when (case when tir.parse_status='SUCCESS' then 'IMPORTED' when tir.parse_status is not null then tir.parse_status when ti.result_status='IMPORTED' then 'PENDING' else ti.result_status end) in ('FAILED','PARSE_FAILED','ERROR') then 0
	    when (case when tir.parse_status='SUCCESS' then 'IMPORTED' when tir.parse_status is not null then tir.parse_status when ti.result_status='IMPORTED' then 'PENDING' else ti.result_status end) <> 'IMPORTED' or ti.status <> 2 then 1
	    else 2
	  end asc,
	  ti.id asc,
	  ti.result_seq asc
	`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []biz.AdminTaskItem
	for rows.Next() {
		var item biz.AdminTaskItem
		var generatedAt sql.NullTime
		var resultImportedAt sql.NullTime
		var validatedAt *time.Time
		var riskJSON []byte
		if err := rows.Scan(
			&item.TaskItemID,
			&item.ItemID,
			&item.PlatformCode,
			&item.Seq,
			&item.Question,
			&item.ProviderStatus,
			&item.ResultStatus,
			&item.AnswerText,
			&item.ThinkingProcess,
			&generatedAt,
			&item.GeneratedAtText,
			&item.RecommendedJSON,
			&item.DeviceID,
			&item.RecordingURL,
			&item.ReferencesJSON,
			&item.ReferenceCount,
			&item.ParseStatus,
			&item.ParseError,
			&resultImportedAt,
			&item.AnswerValidation.Status, &riskJSON, &item.RuleVersion, &validatedAt,
		); err != nil {
			return nil, err
		}
		if generatedAt.Valid {
			item.GeneratedAt = generatedAt.Time
		}
		if resultImportedAt.Valid {
			item.ResultImportedAt = resultImportedAt.Time
		}
		if err := json.Unmarshal(riskJSON, &item.RiskItems); err != nil {
			return nil, err
		}
		if validatedAt != nil {
			item.ValidatedAt = *validatedAt
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *AdminRepo) listResultFiles(ctx context.Context, taskID int64, filter biz.AdminPaginationFilter) ([]biz.AdminResultFile, int, error) {
	var total int
	if err := r.db.QueryRow(ctx, `select count(*) from cdp_task_result_files where task_id=$1`, taskID).Scan(&total); err != nil {
		return nil, 0, err
	}
	offset := (filter.Page - 1) * filter.PageSize
	rows, err := r.db.Query(ctx, `
select id, source_type, coalesce(object_key,''), coalesce(file_url,''), coalesce(file_name,''),
  parse_status, row_count, valid_row_count, invalid_row_count, created_at
from cdp_task_result_files
where task_id=$1
order by created_at desc, id desc
limit $2 offset $3
`, taskID, filter.PageSize, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var files []biz.AdminResultFile
	for rows.Next() {
		var file biz.AdminResultFile
		if err := rows.Scan(&file.ID, &file.SourceType, &file.ObjectKey, &file.FileURL, &file.FileName, &file.ParseStatus,
			&file.RowCount, &file.ValidRowCount, &file.InvalidRowCount, &file.CreatedAt); err != nil {
			return nil, 0, err
		}
		files = append(files, file)
	}
	return files, total, rows.Err()
}

func (r *AdminRepo) listTaskEvents(ctx context.Context, taskID int64, filter biz.AdminPaginationFilter) ([]biz.AdminTaskEvent, int, error) {
	var total int
	if err := r.db.QueryRow(ctx, `select count(*) from cdp_task_events where task_id=$1`, taskID).Scan(&total); err != nil {
		return nil, 0, err
	}
	offset := (filter.Page - 1) * filter.PageSize
	rows, err := r.db.Query(ctx, `
select id, event_type, coalesce(payload,'{}'::jsonb), created_at
from cdp_task_events
where task_id=$1
order by created_at desc, id desc
limit $2 offset $3
`, taskID, filter.PageSize, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var events []biz.AdminTaskEvent
	for rows.Next() {
		var event biz.AdminTaskEvent
		if err := rows.Scan(&event.ID, &event.EventType, &event.Payload, &event.CreatedAt); err != nil {
			return nil, 0, err
		}
		events = append(events, event)
	}
	return events, total, rows.Err()
}

func (r *AdminRepo) listTaskCallbacks(ctx context.Context, taskID int64) ([]biz.AdminTaskCallback, error) {
	rows, err := r.db.Query(ctx, `
select
  id,
  target_url,
  coalesce(request_payload,'{}'::jsonb),
  response_status,
  coalesce(response_body,''),
  status,
  retry_count,
  coalesce(last_error,''),
  created_at,
  updated_at
from cdp_task_callbacks
where task_id=$1
order by created_at desc
limit 100
`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var callbacks []biz.AdminTaskCallback
	for rows.Next() {
		var callback biz.AdminTaskCallback
		var responseStatus sql.NullInt32
		if err := rows.Scan(
			&callback.ID,
			&callback.TargetURL,
			&callback.RequestPayload,
			&responseStatus,
			&callback.ResponseBody,
			&callback.Status,
			&callback.RetryCount,
			&callback.LastError,
			&callback.CreatedAt,
			&callback.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if responseStatus.Valid {
			callback.ResponseStatus = int(responseStatus.Int32)
		}
		callbacks = append(callbacks, callback)
	}
	return callbacks, rows.Err()
}

func (r *AdminRepo) scanPlatformTaskMetrics(ctx context.Context, where string, args []any, metrics *biz.AdminPlatformUserMetrics) error {
	var averageDuration float64
	var longestProcessing float64
	err := r.db.QueryRow(ctx, `
select
  count(*),
  count(*) filter (where t.status='DONE'),
  count(*) filter (where t.status in ('CREATED','SUBMITTED','PROVIDER_DONE','RESULT_IMPORTED','CALLBACK_PENDING')),
  count(*) filter (where t.status='FAILED'),
	  coalesce(avg(extract(epoch from (`+adminTaskExecutionEndedAtSQL+` - t.created_at))) filter (
	    where `+adminTaskExecutionEndedAtSQL+` is not null and `+adminTaskExecutionEndedAtSQL+` >= t.created_at
	  ), 0),
  coalesce(max(extract(epoch from (now() - t.created_at))) filter (
    where t.status in ('CREATED','SUBMITTED','PROVIDER_DONE','RESULT_IMPORTED','CALLBACK_PENDING')
  ), 0)
from cdp_tasks t
`+where, args...).Scan(
		&metrics.TaskMetrics.SubmittedTaskCount,
		&metrics.TaskMetrics.CompletedTaskCount,
		&metrics.TaskMetrics.ProcessingTaskCount,
		&metrics.TaskMetrics.FailedTaskCount,
		&averageDuration,
		&longestProcessing,
	)
	if err != nil {
		return err
	}
	metrics.TaskMetrics.AverageDurationSeconds = int(averageDuration + 0.5)
	metrics.TaskMetrics.LongestProcessingSeconds = int(longestProcessing + 0.5)
	terminalCount := metrics.TaskMetrics.CompletedTaskCount + metrics.TaskMetrics.FailedTaskCount
	if terminalCount > 0 {
		metrics.TaskMetrics.TaskSuccessRate = float64(metrics.TaskMetrics.CompletedTaskCount) / float64(terminalCount)
	}
	return nil
}

func (r *AdminRepo) scanPlatformQuestionMetrics(ctx context.Context, where string, args []any, metrics *biz.AdminPlatformUserMetrics) error {
	err := r.db.QueryRow(ctx, `
select
  coalesce(sum(item_stats.question_count), 0),
  coalesce(sum(coalesce(result_stats.success_answer_count, 0)), 0),
  coalesce(sum(item_stats.provider_done_question_count), 0),
  coalesce(sum(greatest(item_stats.question_count - coalesce(result_stats.success_answer_count, 0), 0)), 0),
  count(distinct item_stats.platform_code) filter (where item_stats.platform_code <> '')
from cdp_tasks t
left join lateral (
  select
    count(*) as question_count,
    count(*) filter (where ti.status=2) as provider_done_question_count,
    nullif(max(ti.platform_code), '') as platform_code
  from cdp_task_items ti
  where ti.task_id=t.id
) item_stats on true
left join lateral (
  select
    count(distinct tir.task_item_id) filter (where tir.parse_status='SUCCESS') as success_answer_count
  from cdp_task_item_results tir
  where tir.task_id=t.id
) result_stats on true
	`+where, args...).Scan(
		&metrics.QuestionMetrics.SubmittedQuestionCount,
		&metrics.QuestionMetrics.CompletedAnswerCount,
		&metrics.QuestionMetrics.ProviderDoneQuestionCount,
		&metrics.QuestionMetrics.FailedQuestionCount,
		&metrics.QuestionMetrics.ActiveQuestionPlatformCount,
	)
	if err != nil {
		return err
	}
	if metrics.QuestionMetrics.SubmittedQuestionCount > 0 {
		metrics.QuestionMetrics.AnswerCompletionRate = float64(metrics.QuestionMetrics.CompletedAnswerCount) / float64(metrics.QuestionMetrics.SubmittedQuestionCount)
	}
	if metrics.TaskMetrics.SubmittedTaskCount > 0 {
		metrics.QuestionMetrics.AverageQuestionsPerTask = float64(metrics.QuestionMetrics.SubmittedQuestionCount) / float64(metrics.TaskMetrics.SubmittedTaskCount)
	}
	return nil
}

func (r *AdminRepo) scanPlatformResultMetrics(ctx context.Context, where string, args []any, metrics *biz.AdminPlatformUserMetrics) error {
	err := r.db.QueryRow(ctx, `
select
  count(f.id),
  count(f.id) filter (where f.parse_status='SUCCESS'),
  count(f.id) filter (where f.parse_status not in ('SUCCESS','PENDING')),
  coalesce(sum(f.row_count), 0),
  coalesce(sum(f.valid_row_count), 0),
  coalesce(sum(f.invalid_row_count), 0)
from cdp_tasks t
left join cdp_task_result_files f on f.task_id=t.id
`+where, args...).Scan(
		&metrics.ResultMetrics.ResultFileCount,
		&metrics.ResultMetrics.ParsedFileCount,
		&metrics.ResultMetrics.ParseFailedFileCount,
		&metrics.ResultMetrics.RowCount,
		&metrics.ResultMetrics.ValidRowCount,
		&metrics.ResultMetrics.InvalidRowCount,
	)
	if err != nil {
		return err
	}
	if metrics.ResultMetrics.RowCount > 0 {
		metrics.ResultMetrics.ValidRowRate = float64(metrics.ResultMetrics.ValidRowCount) / float64(metrics.ResultMetrics.RowCount)
	}
	return nil
}

func (r *AdminRepo) scanPlatformCallbackMetrics(ctx context.Context, where string, args []any, metrics *biz.AdminPlatformUserMetrics) error {
	err := r.db.QueryRow(ctx, `
select
  count(cb.id),
  count(cb.id) filter (where cb.status='DONE'),
  count(cb.id) filter (where cb.status='FAILED')
from cdp_tasks t
left join cdp_task_callbacks cb on cb.task_id=t.id
`+where, args...).Scan(
		&metrics.CallbackMetrics.CallbackCount,
		&metrics.CallbackMetrics.CallbackSuccessCount,
		&metrics.CallbackMetrics.CallbackFailedCount,
	)
	if err != nil {
		return err
	}
	if metrics.CallbackMetrics.CallbackCount > 0 {
		metrics.CallbackMetrics.CallbackSuccessRate = float64(metrics.CallbackMetrics.CallbackSuccessCount) / float64(metrics.CallbackMetrics.CallbackCount)
	}
	return nil
}

func (r *AdminRepo) listPlatformStatusBreakdown(ctx context.Context, where string, args []any, total int) ([]biz.AdminMetricBreakdown, error) {
	rows, err := r.db.Query(ctx, `
select t.status, count(*)
from cdp_tasks t
`+where+`
group by t.status
order by count(*) desc, t.status asc
`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []biz.AdminMetricBreakdown
	for rows.Next() {
		var item biz.AdminMetricBreakdown
		if err := rows.Scan(&item.Label, &item.Value); err != nil {
			return nil, err
		}
		if total > 0 {
			item.Ratio = float64(item.Value) / float64(total)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *AdminRepo) listPlatformFailureReasons(ctx context.Context, where string, args []any, totalFailed int) ([]biz.AdminMetricBreakdown, error) {
	rows, err := r.db.Query(ctx, `
select coalesce(nullif(t.last_error,''), '未记录错误'), count(*)
from cdp_tasks t
`+where+`
  and t.status='FAILED'
group by coalesce(nullif(t.last_error,''), '未记录错误')
order by count(*) desc
limit 5
`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []biz.AdminMetricBreakdown
	for rows.Next() {
		var item biz.AdminMetricBreakdown
		if err := rows.Scan(&item.Label, &item.Value); err != nil {
			return nil, err
		}
		if totalFailed > 0 {
			item.Ratio = float64(item.Value) / float64(totalFailed)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *AdminRepo) listPlatformRecentTasks(ctx context.Context, where string, args []any) ([]biz.AdminTaskSummary, error) {
	queryArgs := append(append([]any{}, args...), 10)
	rows, err := r.db.Query(ctx, `
select
`+adminTaskSummarySelectSQL+`
from cdp_tasks t
`+adminTaskProgressJoinSQL+`
`+where+`
order by t.created_at desc
limit $`+strconv.Itoa(len(args)+1), queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []biz.AdminTaskSummary
	for rows.Next() {
		task, err := scanAdminTaskSummary(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (r *AdminRepo) listPlatformProviderDailyMetrics(ctx context.Context, where string, args []any) ([]biz.AdminProviderDailyMetric, error) {
	rows, err := r.db.Query(ctx, `
select
	  `+adminMetricDateSQL+` as metric_date,
	  t.provider_code,
	  count(distinct t.id) as task_count,
	  coalesce(sum(item_stats.question_count), 0) as question_count,
	  coalesce(sum(coalesce(result_stats.success_answer_count, 0)), 0) as success_answer_count,
	  coalesce(sum(greatest(item_stats.question_count - coalesce(result_stats.success_answer_count, 0), 0)), 0) as failed_answer_count
from cdp_tasks t
left join lateral (
  select count(*) as question_count
  from cdp_task_items ti
  where ti.task_id=t.id
) item_stats on true
left join lateral (
  select count(distinct tir.task_item_id) filter (where tir.parse_status='SUCCESS') as success_answer_count
  from cdp_task_item_results tir
  where tir.task_id=t.id
) result_stats on true
	`+where+`
group by metric_date, t.provider_code
order by metric_date desc, t.provider_code asc
`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []biz.AdminProviderDailyMetric
	for rows.Next() {
		var item biz.AdminProviderDailyMetric
		if err := rows.Scan(
			&item.MetricDate,
			&item.ProviderCode,
			&item.TaskCount,
			&item.QuestionCount,
			&item.SuccessAnswerCount,
			&item.FailedAnswerCount,
		); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *AdminRepo) listProviderDailyMetrics(ctx context.Context, where string, args []any) ([]biz.AdminProviderDailyMetric, error) {
	rows, err := r.db.Query(ctx, `
select
	  `+adminMetricDateSQL+` as metric_date,
	  t.provider_code,
	  count(distinct t.id) as task_count,
	  coalesce(sum(item_stats.question_count), 0) as question_count,
	  coalesce(sum(coalesce(result_stats.success_answer_count, 0)), 0) as success_answer_count,
	  coalesce(sum(greatest(item_stats.question_count - coalesce(result_stats.success_answer_count, 0), 0)), 0) as failed_answer_count
from cdp_tasks t
left join lateral (
  select count(*) as question_count
  from cdp_task_items ti
  where ti.task_id=t.id
) item_stats on true
left join lateral (
  select count(distinct tir.task_item_id) filter (where tir.parse_status='SUCCESS') as success_answer_count
  from cdp_task_item_results tir
  where tir.task_id=t.id
) result_stats on true
	`+where+`
group by metric_date, t.provider_code
order by metric_date desc
`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []biz.AdminProviderDailyMetric
	for rows.Next() {
		var item biz.AdminProviderDailyMetric
		if err := rows.Scan(
			&item.MetricDate,
			&item.ProviderCode,
			&item.TaskCount,
			&item.QuestionCount,
			&item.SuccessAnswerCount,
			&item.FailedAnswerCount,
		); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func scanAdminTaskSummary(scanner interface{ Scan(dest ...any) error }) (biz.AdminTaskSummary, error) {
	var summary biz.AdminTaskSummary
	var executionEndedAt sql.NullTime
	if err := scanner.Scan(
		&summary.TaskID,
		&summary.BatchNo,
		&summary.TaskName,
		&summary.Source,
		&summary.PlatformCode,
		&summary.ExternalBatchID,
		&summary.ProviderCode,
		&summary.Status,
		&summary.QuestionCount,
		&summary.CompletedCount,
		&summary.SuccessCount,
		&summary.FailedCount,
		&summary.ResultSuccessCount,
		&summary.ResultFailedCount,
		&summary.ResultMissingCount,
		&summary.CreatedAt,
		&summary.UpdatedAt,
		&executionEndedAt,
		&summary.SubmitFailed,
		&summary.DispatchLabels,
		&summary.ReassignedFromTaskID,
		&summary.ReassignedToTaskID,
		&summary.RiskCount, &summary.InvalidAnswerCount, &summary.RiskSummary, &summary.RiskStatsUpdatedAt, &summary.Device, &summary.ExecutionMode, &summary.ShardCount, &summary.CompletedShardCount, &summary.PendingSubmissionCount, &summary.FailedSubmissionCount,
	); err != nil {
		return biz.AdminTaskSummary{}, err
	}
	if executionEndedAt.Valid {
		summary.ExecutionEndedAt = executionEndedAt.Time
	}
	if summary.QuestionCount > 0 {
		summary.Progress = float64(summary.SuccessCount+summary.FailedCount) / float64(summary.QuestionCount)
	}
	return summary, nil
}

func scanPlatformUser(scanner interface{ Scan(dest ...any) error }) (biz.PlatformUser, error) {
	var user biz.PlatformUser
	var dispatchCreatedAt sql.NullTime
	var dispatchUpdatedAt sql.NullTime
	if err := scanner.Scan(&user.ID, &user.PlatformCode, &user.Name, &user.APIKey, &user.Status,
		&user.CallbackMode, &user.CallbackURL, &user.CallbackConfig, &user.CreatedAt, &user.UpdatedAt,
		&user.DispatchProfile.ID, &user.DispatchProfile.PlatformUserID, &user.DispatchProfile.Code, &user.DispatchProfile.Name,
		&user.DispatchProfile.Strategy, &user.DispatchProfile.Enabled, &user.DispatchProfile.Cursor, &user.DispatchProfile.LastProviderCode,
		&user.DispatchProfile.Config, &dispatchCreatedAt, &dispatchUpdatedAt); err != nil {
		return biz.PlatformUser{}, err
	}
	if dispatchCreatedAt.Valid {
		user.DispatchProfile.CreatedAt = dispatchCreatedAt.Time
	}
	if dispatchUpdatedAt.Valid {
		user.DispatchProfile.UpdatedAt = dispatchUpdatedAt.Time
	}
	user.DispatchProfile.ProviderCodes = dispatchProviderCodes(user.DispatchProfile.Config)
	user.DispatchProfile.FallbackSelectionPolicy = dispatchFallbackSelectionPolicy(user.DispatchProfile.Config, user.DispatchProfile.Strategy)
	user.DispatchProfile.Rules = dispatchRules(user.DispatchProfile.Config)
	return user, nil
}

func normalizeTaskFilter(filter *biz.AdminTaskFilter) {
	if filter.Page <= 0 {
		filter.Page = 1
	}
	if filter.PageSize <= 0 {
		filter.PageSize = 20
	}
	if filter.PageSize > 100 {
		filter.PageSize = 100
	}
	filter.ResultStatus = strings.TrimSpace(strings.ToUpper(filter.ResultStatus))
	filter.PlatformCode = strings.TrimSpace(filter.PlatformCode)
	filter.ProviderCode = strings.TrimSpace(filter.ProviderCode)
	filter.TaskName = strings.TrimSpace(filter.TaskName)
	filter.BatchNo = strings.TrimSpace(filter.BatchNo)
}

func normalizePlatformUserFilter(filter *biz.AdminPlatformUserFilter) {
	if filter.Page <= 0 {
		filter.Page = 1
	}
	if filter.PageSize <= 0 {
		filter.PageSize = 20
	}
	if filter.PageSize > 100 {
		filter.PageSize = 100
	}
	filter.Keyword = strings.TrimSpace(filter.Keyword)
	filter.Status = strings.TrimSpace(strings.ToUpper(filter.Status))
}

func normalizeTaskItemFilter(filter *biz.AdminTaskItemFilter) {
	if filter.Page <= 0 {
		filter.Page = 1
	}
	if filter.PageSize <= 0 {
		filter.PageSize = 20
	}
	if filter.PageSize > 100 {
		filter.PageSize = 100
	}
}

func normalizePaginationFilter(filter *biz.AdminPaginationFilter) {
	if filter.Page <= 0 {
		filter.Page = 1
	}
	if filter.PageSize <= 0 {
		filter.PageSize = 20
	}
	if filter.PageSize > 100 {
		filter.PageSize = 100
	}
}

func normalizePlatformUserInput(input *biz.AdminPlatformUserInput) {
	input.PlatformCode = strings.TrimSpace(input.PlatformCode)
	input.Name = strings.TrimSpace(input.Name)
	input.Status = strings.TrimSpace(strings.ToUpper(input.Status))
	input.CallbackMode = strings.TrimSpace(strings.ToUpper(input.CallbackMode))
	input.CallbackURL = strings.TrimSpace(input.CallbackURL)
	input.DispatchProfile.Code = strings.TrimSpace(input.DispatchProfile.Code)
	input.DispatchProfile.Name = strings.TrimSpace(input.DispatchProfile.Name)
	input.DispatchProfile.Strategy = biz.DispatchStrategy(strings.TrimSpace(strings.ToUpper(string(input.DispatchProfile.Strategy))))
	input.DispatchProfile.FallbackSelectionPolicy = biz.DispatchFallbackSelectionPolicy(strings.TrimSpace(strings.ToUpper(string(input.DispatchProfile.FallbackSelectionPolicy))))
	for i := range input.DispatchProfile.ProviderCodes {
		input.DispatchProfile.ProviderCodes[i] = strings.TrimSpace(input.DispatchProfile.ProviderCodes[i])
	}
	for i := range input.DispatchProfile.Rules {
		input.DispatchProfile.Rules[i].Code = strings.TrimSpace(input.DispatchProfile.Rules[i].Code)
		input.DispatchProfile.Rules[i].Name = strings.TrimSpace(input.DispatchProfile.Rules[i].Name)
		input.DispatchProfile.Rules[i].TaskNameRegex = strings.TrimSpace(input.DispatchProfile.Rules[i].TaskNameRegex)
		for j := range input.DispatchProfile.Rules[i].Labels {
			input.DispatchProfile.Rules[i].Labels[j] = strings.TrimSpace(input.DispatchProfile.Rules[i].Labels[j])
		}
		for j := range input.DispatchProfile.Rules[i].ProviderCodes {
			input.DispatchProfile.Rules[i].ProviderCodes[j] = strings.TrimSpace(input.DispatchProfile.Rules[i].ProviderCodes[j])
		}
	}
	if input.Status == "" {
		input.Status = "ACTIVE"
	}
	if input.CallbackMode == "" {
		input.CallbackMode = "DEFAULT_HTTP"
	}
	if input.DispatchProfile.Strategy == "" {
		input.DispatchProfile.Strategy = biz.DispatchStrategyRoundRobin
	}
	if input.DispatchProfile.FallbackSelectionPolicy == "" {
		input.DispatchProfile.FallbackSelectionPolicy = defaultDispatchFallbackSelectionPolicy(input.DispatchProfile.Strategy)
	}
}

func adminTaskWhere(filter biz.AdminTaskFilter) (string, []any) {
	clauses := []string{" where t.deleted_at is null"}
	if filter.HasRisk {
		clauses = append(clauses, " and t.risk_count>0")
	}
	var args []any
	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}
	if filter.BatchNo != "" {
		add(" and t.batch_no ilike '%%' || $%d || '%%'", filter.BatchNo)
	}
	if filter.TaskName != "" {
		if strings.Trim(filter.TaskName, "%") != "" {
			clauses = append(clauses, " and t.task_name <> ''")
		}
		add(" and t.task_name ilike '%%' || $%d || '%%'", filter.TaskName)
	}
	if filter.Source != "" {
		add(" and t.source = $%d", filter.Source)
	}
	if filter.PlatformCode != "" {
		add(" and t.platform_code = $%d", filter.PlatformCode)
	}
	if filter.ProviderCode != "" {
		add(" and t.provider_code = $%d", filter.ProviderCode)
	}
	if !filter.CreatedFrom.IsZero() {
		add(" and t.created_at >= $%d", filter.CreatedFrom)
	}
	if !filter.CreatedTo.IsZero() {
		add(" and t.created_at <= $%d", filter.CreatedTo)
	}
	switch filter.ResultStatus {
	case "", "ALL":
	case "PROCESSING":
		clauses = append(clauses, " and t.status in ('CREATED','SUBMITTED','PROVIDER_DONE','RESULT_IMPORTED','CALLBACK_PENDING')")
	case "DONE", "FAILED", "CREATED", "SUBMITTED", "PROVIDER_DONE", "RESULT_IMPORTED", "CALLBACK_PENDING":
		add(" and t.status = $%d", filter.ResultStatus)
	default:
		add(" and t.status = $%d", filter.ResultStatus)
	}
	return strings.Join(clauses, ""), args
}

func adminPlatformUserWhere(filter biz.AdminPlatformUserFilter) (string, []any) {
	clauses := []string{" where 1=1"}
	var args []any
	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}
	if filter.Keyword != "" {
		args = append(args, filter.Keyword)
		idx := len(args)
		clauses = append(clauses, fmt.Sprintf(" and (u.platform_code ilike '%%' || $%d || '%%' or u.name ilike '%%' || $%d || '%%')", idx, idx))
	}
	if filter.Status != "" && filter.Status != "ALL" {
		add(" and u.status = $%d", filter.Status)
	}
	return strings.Join(clauses, ""), args
}

func adminPlatformUserMetricWhere(platformUserID int64, platformCode string, filter biz.AdminPlatformUserMetricsFilter) (string, []any) {
	clauses := []string{" where (t.platform_user_id=$1 or t.platform_code=$2) and t.deleted_at is null and t.status <> 'REASSIGNED'"}
	args := []any{platformUserID, platformCode}
	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}
	if !filter.CreatedFrom.IsZero() {
		add(" and t.created_at >= $%d", filter.CreatedFrom)
	}
	if !filter.CreatedTo.IsZero() {
		add(" and t.created_at <= $%d", filter.CreatedTo)
	}
	return strings.Join(clauses, ""), args
}

func adminPlatformUserReconciliationWhere(platformUserID int64, platformCode string, filter biz.AdminPlatformUserReconciliationFilter) (string, []any) {
	clauses := []string{` where (t.platform_user_id=$1 or t.platform_code=$2)
  and t.deleted_at is null
  and exists (
    select 1
    from cdp_task_callbacks cb
    where cb.task_id=t.id
      and cb.status='DONE'
  )`}
	args := []any{platformUserID, platformCode}
	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}
	if !filter.CreatedFrom.IsZero() {
		add(" and t.created_at >= $%d", filter.CreatedFrom)
	}
	if !filter.CreatedTo.IsZero() {
		add(" and t.created_at <= $%d", filter.CreatedTo)
	}
	return strings.Join(clauses, ""), args
}

func adminProviderMetricWhere(providerCode string, filter biz.AdminProviderMetricsFilter) (string, []any) {
	clauses := []string{" where t.provider_code=$1 and t.deleted_at is null and t.status <> 'REASSIGNED'"}
	args := []any{providerCode}
	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}
	if !filter.CreatedFrom.IsZero() {
		add(" and t.created_at >= $%d", filter.CreatedFrom)
	}
	if !filter.CreatedTo.IsZero() {
		add(" and t.created_at <= $%d", filter.CreatedTo)
	}
	return strings.Join(clauses, ""), args
}

func newAdminAPIKey() (string, error) {
	random := make([]byte, 18)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "mck_" + hex.EncodeToString(random), nil
}

func mapPlatformUserWriteError(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "idx_platform_users_platform_code") || strings.Contains(err.Error(), "idx_platform_users_api_key") {
		return fmt.Errorf("%w: %v", biz.ErrPlatformUserConflict, err)
	}
	return err
}

func nullableAdminString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func cleanProviderCodes(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		code := strings.TrimSpace(value)
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		out = append(out, code)
	}
	return out
}

func dispatchConfigJSON(providerCodes []string, fallbackSelectionPolicy biz.DispatchFallbackSelectionPolicy, rules []biz.DispatchRuleInput) (json.RawMessage, error) {
	providers := make([]map[string]string, 0, len(providerCodes))
	for _, code := range cleanProviderCodes(providerCodes) {
		providers = append(providers, map[string]string{"code": code})
	}
	policy := dispatchFallbackSelectionPolicyValue(fallbackSelectionPolicy)
	cleanRules := make([]map[string]any, 0, len(rules))
	for _, rule := range cleanDispatchRuleInputs(rules) {
		cleanRules = append(cleanRules, map[string]any{
			"code":          rule.Code,
			"name":          rule.Name,
			"sortOrder":     rule.SortOrder,
			"taskNameRegex": rule.TaskNameRegex,
			"labels":        rule.Labels,
			"providerCodes": rule.ProviderCodes,
			"enabled":       rule.Enabled,
		})
	}
	body, err := json.Marshal(map[string]any{"providers": providers, "fallbackSelectionPolicy": policy, "rules": cleanRules})
	return json.RawMessage(body), err
}

func dispatchProviderCodes(config json.RawMessage) []string {
	var parsed struct {
		Providers []struct {
			Code string `json:"code"`
		} `json:"providers"`
	}
	if len(config) == 0 || json.Unmarshal(config, &parsed) != nil {
		return nil
	}
	out := make([]string, 0, len(parsed.Providers))
	for _, provider := range parsed.Providers {
		out = append(out, provider.Code)
	}
	return cleanProviderCodes(out)
}

func dispatchFallbackSelectionPolicy(config json.RawMessage, strategy biz.DispatchStrategy) biz.DispatchFallbackSelectionPolicy {
	var parsed struct {
		FallbackSelectionPolicy string `json:"fallbackSelectionPolicy"`
	}
	if len(config) == 0 || json.Unmarshal(config, &parsed) != nil {
		return defaultDispatchFallbackSelectionPolicy(strategy)
	}
	if strings.TrimSpace(parsed.FallbackSelectionPolicy) == "" {
		return defaultDispatchFallbackSelectionPolicy(strategy)
	}
	return dispatchFallbackSelectionPolicyValue(biz.DispatchFallbackSelectionPolicy(parsed.FallbackSelectionPolicy))
}

func defaultDispatchFallbackSelectionPolicy(strategy biz.DispatchStrategy) biz.DispatchFallbackSelectionPolicy {
	if strategy == biz.DispatchStrategyRoundRobin {
		return biz.DispatchFallbackSelectionPolicyRoundRobin
	}
	return biz.DispatchFallbackSelectionPolicyLowestUsageRate
}

func dispatchFallbackSelectionPolicyValue(policy biz.DispatchFallbackSelectionPolicy) biz.DispatchFallbackSelectionPolicy {
	switch biz.DispatchFallbackSelectionPolicy(strings.TrimSpace(strings.ToUpper(string(policy)))) {
	case biz.DispatchFallbackSelectionPolicyRoundRobin:
		return biz.DispatchFallbackSelectionPolicyRoundRobin
	case biz.DispatchFallbackSelectionPolicyLowestUsageRate:
		return biz.DispatchFallbackSelectionPolicyLowestUsageRate
	default:
		return biz.DispatchFallbackSelectionPolicyLowestUsageRate
	}
}

func dispatchRules(config json.RawMessage) []biz.DispatchRule {
	var parsed struct {
		Rules []struct {
			Code          string   `json:"code"`
			Name          string   `json:"name"`
			SortOrder     int      `json:"sortOrder"`
			TaskNameRegex string   `json:"taskNameRegex"`
			Labels        []string `json:"labels"`
			ProviderCodes []string `json:"providerCodes"`
			Enabled       *bool    `json:"enabled"`
		} `json:"rules"`
	}
	if len(config) == 0 || json.Unmarshal(config, &parsed) != nil {
		return nil
	}
	out := make([]biz.DispatchRule, 0, len(parsed.Rules))
	for _, rule := range parsed.Rules {
		enabled := true
		if rule.Enabled != nil {
			enabled = *rule.Enabled
		}
		out = append(out, biz.DispatchRule{
			Code:          strings.TrimSpace(rule.Code),
			Name:          strings.TrimSpace(rule.Name),
			SortOrder:     rule.SortOrder,
			TaskNameRegex: strings.TrimSpace(rule.TaskNameRegex),
			Labels:        cleanStringValues(rule.Labels),
			ProviderCodes: cleanProviderCodes(rule.ProviderCodes),
			Enabled:       enabled,
		})
	}
	return out
}

func cleanDispatchRuleInputs(rules []biz.DispatchRuleInput) []biz.DispatchRuleInput {
	out := make([]biz.DispatchRuleInput, 0, len(rules))
	usedCodes := map[string]bool{}
	for _, rule := range rules {
		code := strings.TrimSpace(rule.Code)
		if code != "" {
			usedCodes[code] = true
		}
	}
	for _, rule := range rules {
		rule.Code = strings.TrimSpace(rule.Code)
		rule.Name = strings.TrimSpace(rule.Name)
		rule.TaskNameRegex = strings.TrimSpace(rule.TaskNameRegex)
		rule.Labels = cleanStringValues(rule.Labels)
		rule.ProviderCodes = cleanProviderCodes(rule.ProviderCodes)
		if rule.Code == "" && rule.TaskNameRegex == "" && len(rule.ProviderCodes) == 0 {
			continue
		}
		if rule.Code == "" {
			for n := len(out) + 1; ; n++ {
				code := fmt.Sprintf("rule_%d", n)
				if !usedCodes[code] {
					rule.Code = code
					usedCodes[code] = true
					break
				}
			}
		}
		out = append(out, rule)
	}
	return out
}

func cleanStringValues(values []string) []string {
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
