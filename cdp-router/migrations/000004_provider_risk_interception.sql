-- Migrations are replayed at startup: transfer the old switch exactly once.
do $$
begin
 if not exists (select 1 from information_schema.columns where table_schema=current_schema() and table_name='cdp_result_validation_rules' and column_name='risk_callback_allowed_provider_codes') then
  alter table cdp_result_validation_rules add column risk_callback_allowed_provider_codes jsonb not null default '[]'::jsonb;
  update cdp_result_validation_rules set risk_callback_allowed_provider_codes=disabled_provider_codes,version=version+1;
 end if;
end $$;
create index if not exists idx_cdp_answers_skipped_validation on cdp_task_item_results(task_id) where validation_status='SKIPPED';

-- Legacy bypassed answers require validation before any callback can be sent.
update cdp_tasks t set risk_stats_updated_at=null
where risk_stats_updated_at is not null and exists (
 select 1 from cdp_task_item_results r where r.task_id=t.id and r.validation_status='SKIPPED'
);
