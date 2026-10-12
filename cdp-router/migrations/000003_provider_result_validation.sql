-- Single-instance startup migration; replay-safe and atomic per file.
begin;
set local lock_timeout = '5s';
set local statement_timeout = '120s';

alter table cdp_result_validation_rules add column if not exists disabled_provider_codes jsonb not null default '[]'::jsonb;

commit;
