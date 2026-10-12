-- Single-instance startup migration; replay-safe and atomic per file.
begin;
set local lock_timeout = '5s';
set local statement_timeout = '120s';

-- Migrations are replayed at startup: transfer the old switch exactly once.
do $$
begin
 if not exists (select 1 from information_schema.columns where table_schema=current_schema() and table_name='cdp_result_validation_rules' and column_name='risk_callback_allowed_provider_codes') then
  alter table cdp_result_validation_rules add column risk_callback_allowed_provider_codes jsonb not null default '[]'::jsonb;
  update cdp_result_validation_rules set risk_callback_allowed_provider_codes=disabled_provider_codes,version=version+1;
 end if;
end $$;
create index if not exists idx_cdp_answers_skipped_validation on cdp_task_item_results(task_id) where validation_status='SKIPPED';

-- Only old Push bypasses invalidate risk statistics. The execution_mode column
-- is introduced by 000005, so the first upgrade also supports its absence.
do $$
begin
 if exists (select 1 from pg_attribute where attrelid='cdp_tasks'::regclass and attname='execution_mode' and not attisdropped) then
  update cdp_tasks t set risk_stats_updated_at=null
  where execution_mode='push' and risk_stats_updated_at is not null and exists (
   select 1 from cdp_task_item_results r where r.task_id=t.id and r.validation_status='SKIPPED'
  );
 else
  update cdp_tasks t set risk_stats_updated_at=null
  where risk_stats_updated_at is not null and exists (
   select 1 from cdp_task_item_results r where r.task_id=t.id and r.validation_status='SKIPPED'
  );
 end if;
end $$;

commit;
