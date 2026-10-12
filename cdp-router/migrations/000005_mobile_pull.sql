-- Single-instance startup migration; replay-safe and atomic per file.
begin;
set local lock_timeout = '5s';
set local statement_timeout = '120s';

alter table cdp_tasks add column if not exists device varchar(16) not null default 'web' check (device in ('web','mobile'));
alter table cdp_tasks add column if not exists execution_mode varchar(16) not null default 'push' check (execution_mode in ('push','pull'));
create table if not exists cdp_task_shards (
 id bigserial primary key, task_id bigint not null,
 provider_code varchar(64) not null, channel varchar(64) not null,
 request_id varchar(128) not null, request_hash varchar(64) not null,
 item_count integer not null check(item_count>0),
 status varchar(16) not null default 'ASSIGNED' check(status in ('ASSIGNED','RECEIVING','COMPLETED')),
 finished_submission_id bigint, created_at timestamptz not null default now(), updated_at timestamptz not null default now(), completed_at timestamptz,
 unique(provider_code,request_id)
);
create table if not exists cdp_task_result_submissions (
 id bigserial primary key, task_id bigint not null, shard_id bigint not null,
 request_id varchar(128) not null, request_hash varchar(64) not null, body jsonb not null,
 status varchar(24) not null default 'ACCEPTED' check(status in ('ACCEPTED','PROCESSING','APPLIED','RETRY_WAIT','FAILED','REJECTED_CLOSED','SUPERSEDED')),
 retry_count integer not null default 0, next_retry_at timestamptz not null default now(),
 lease_token varchar(64), lease_until timestamptz, last_error text, error_details jsonb,
 created_at timestamptz not null default now(), updated_at timestamptz not null default now(), processed_at timestamptz,
 unique(shard_id,request_id)
);
alter table cdp_task_items add column if not exists shard_id bigint;
alter table cdp_task_item_results add column if not exists source_submission_id bigint;
alter table cdp_task_item_results alter column result_file_id drop not null;
do $$ begin if not exists(select 1 from pg_constraint where conrelid='cdp_task_item_results'::regclass and conname='result_exactly_one_source') then alter table cdp_task_item_results add constraint result_exactly_one_source check ((result_file_id is null) <> (source_submission_id is null)); end if; end $$;
create index if not exists cdp_items_unclaimed on cdp_task_items(task_id,platform_code,id) where shard_id is null;
create index if not exists cdp_items_shard on cdp_task_items(shard_id,id);
create index if not exists cdp_pull_candidates on cdp_tasks(provider_code,execution_mode,device,status,created_at,id) where deleted_at is null;
create index if not exists cdp_shards_task_status on cdp_task_shards(task_id,status);
create index if not exists cdp_submissions_pending on cdp_task_result_submissions(status,next_retry_at,id);
create index if not exists cdp_submissions_lease on cdp_task_result_submissions(lease_until) where status='PROCESSING';
create index if not exists cdp_submissions_task_status on cdp_task_result_submissions(task_id,status);
-- Relations are checked by the usecase/repository, not physical foreign keys.
-- Remove constraints from previous trial deployments as well as fresh installs.
do $$
declare
 constraint_record record;
begin
 for constraint_record in
  select conrelid::regclass::text as table_name,conname from pg_constraint
  where contype='f' and conrelid in (
   'cdp_task_shards'::regclass, 'cdp_task_result_submissions'::regclass,
   'cdp_task_items'::regclass, 'cdp_task_item_results'::regclass
  )
 loop
  execute format('alter table %s drop constraint %I',constraint_record.table_name,constraint_record.conname);
 end loop;
 -- The former composite foreign key required this otherwise redundant key.
 if exists (select 1 from pg_constraint where conrelid='cdp_task_shards'::regclass and conname='cdp_task_shards_id_task_id_key') then
  alter table cdp_task_shards drop constraint cdp_task_shards_id_task_id_key;
 end if;
end $$;

-- Pull never receives a supplier-created task identifier. Repair only Pull;
-- genuine Push supplier identifiers must be retained.
update cdp_tasks set provider_external_batch_id=null
where execution_mode='pull' and provider_external_batch_id is not null;

commit;
