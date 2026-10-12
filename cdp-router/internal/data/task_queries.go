package data

// taskItemStatsSQL aggregates each result once per task, including all result
// sequences. taskID is an internal SQL expression, never user-provided SQL.
func taskItemStatsSQL(taskID string) string {
	return `select
  count(*)::int as item_count,
  count(*) filter (where ti.status=2)::int as provider_done_count,
  count(*) filter (where ti.result_status='IMPORTED')::int as imported_count,
  count(*) filter (where results.has_success)::int as result_success_count,
  count(*) filter (where not results.has_success and results.has_failure)::int as result_failed_count
from cdp_task_items ti
left join (
  select task_item_id,
    bool_or(case when source_submission_id is not null then cleaned_payload->>'executionStatus'='SUCCESS' else parse_status='SUCCESS' end) as has_success,
    bool_or(case when source_submission_id is not null then cleaned_payload->>'executionStatus'='FAILED' else parse_status <> 'SUCCESS' end) as has_failure
  from cdp_task_item_results
  where task_id=` + taskID + `
  group by task_item_id
) results on results.task_item_id=ti.id
where ti.task_id=` + taskID
}
