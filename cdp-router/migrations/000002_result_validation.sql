-- Single-instance startup migration; replay-safe and atomic per file.
begin;
set local lock_timeout = '5s';
set local statement_timeout = '120s';

-- Idempotent: this project's migrator executes all SQL files on startup.
alter table cdp_task_item_results
  add column if not exists validation_status varchar(16) not null default 'UNKNOWN',
  add column if not exists risk_items jsonb not null default '[]',
  add column if not exists validation_rule_version text not null default '',
  add column if not exists validated_at timestamptz;
alter table cdp_tasks
  add column if not exists risk_count integer not null default 0,
  add column if not exists invalid_answer_count integer not null default 0,
  add column if not exists risk_summary jsonb not null default '{}',
  add column if not exists risk_stats_updated_at timestamptz;
create table if not exists cdp_result_validation_rules (
  id integer primary key check (id = 1),
  version bigint not null default 1,
  keywords jsonb not null default '[]',
  updated_by text not null default '',
  updated_at timestamptz not null default now()
);
insert into cdp_result_validation_rules (id) values (1) on conflict do nothing;

commit;
