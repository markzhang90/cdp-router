create table if not exists platform_users (
  id bigserial primary key,
  platform_code varchar(64) not null,
  name varchar(128) not null,
  api_key varchar(256) not null,
  status varchar(32) not null default 'ACTIVE',
  default_provider_code varchar(64) not null,
  callback_mode varchar(64) not null default 'DEFAULT_HTTP',
  callback_url text,
  callback_config jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create extension if not exists pg_trgm;

create unique index if not exists idx_platform_users_platform_code on platform_users(platform_code);
create unique index if not exists idx_platform_users_api_key on platform_users(api_key);
create index if not exists idx_platform_users_status on platform_users(status);

create table if not exists cdp_tasks (
  id bigserial primary key,
  job_id varchar(64),
  platform_user_id bigint,
  platform_code varchar(64),
  batch_no varchar(128) not null,
  provider_code varchar(64) not null,
  gateway_code varchar(64),
  source varchar(128),
  external_batch_id varchar(128),
  provider_external_batch_id varchar(128),
  biz_date date,
  status varchar(32) not null,
  callback_url text,
  provider_callback_url text,
  raw_request jsonb not null,
  provider_request jsonb,
  provider_response jsonb,
  result_payload jsonb,
  result_object_key text,
  result_file_name text,
  total_count int not null default 0,
  success_count int not null default 0,
  failed_count int not null default 0,
  last_error text,
  submitted_at timestamptz,
  provider_finished_at timestamptz,
  result_imported_at timestamptz,
  callback_sent_at timestamptz,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  unique (batch_no)
);

create index if not exists idx_cdp_tasks_status_created_at on cdp_tasks(status, created_at);
create index if not exists idx_cdp_tasks_provider_created_at on cdp_tasks(provider_code, created_at);
create index if not exists idx_cdp_tasks_external_batch_id on cdp_tasks(external_batch_id) where external_batch_id is not null;
create index if not exists idx_cdp_tasks_status_updated_at on cdp_tasks(status, updated_at);
create index if not exists idx_cdp_tasks_provider_status_created_at on cdp_tasks(provider_code, status, created_at);
create index if not exists idx_cdp_tasks_submitted_progress
  on cdp_tasks(status, submitted_at, id)
  where provider_external_batch_id is not null;
create index if not exists idx_cdp_tasks_status_due_at
  on cdp_tasks(status, (coalesce(submitted_at, created_at)), id);

alter table cdp_tasks add column if not exists job_id varchar(64);
alter table cdp_tasks add column if not exists platform_user_id bigint;
alter table cdp_tasks add column if not exists platform_code varchar(64);
alter table cdp_tasks add column if not exists provider_external_batch_id varchar(128);
alter table cdp_tasks add column if not exists result_row_count int not null default 0;
alter table cdp_tasks add column if not exists source varchar(128);
update cdp_tasks set source = gateway_code where source is null and gateway_code is not null;
update cdp_tasks set provider_external_batch_id = external_batch_id where provider_external_batch_id is null and external_batch_id is not null;
update cdp_tasks set job_id = 'job_' || id::text where job_id is null;

create unique index if not exists idx_cdp_tasks_job_id on cdp_tasks(job_id) where job_id is not null;
create index if not exists idx_cdp_tasks_platform_user_created_at on cdp_tasks(platform_user_id, created_at) where platform_user_id is not null;
create index if not exists idx_cdp_tasks_platform_code_created_at on cdp_tasks(platform_code, created_at) where platform_code is not null;
create index if not exists idx_cdp_tasks_provider_external_batch_id on cdp_tasks(provider_external_batch_id) where provider_external_batch_id is not null;
create index if not exists idx_cdp_tasks_source_created_at on cdp_tasks(source, created_at) where source is not null;
create index if not exists idx_cdp_tasks_batch_no_pattern on cdp_tasks(batch_no text_pattern_ops);
create index if not exists idx_cdp_tasks_batch_no_trgm on cdp_tasks using gin (batch_no gin_trgm_ops);

create table if not exists cdp_task_items (
  id bigserial primary key,
  task_id bigint not null,
  item_id varchar(128) not null,
  platform_code varchar(64) not null,
  seq int not null,
  question text not null,
  deepthink boolean not null default false,
  web_search boolean not null default false,
  raw_item jsonb not null,
  question_id varchar(128),
  status int not null default 0,
  result_status varchar(32) not null default 'PENDING',
  result_imported_at timestamptz,
  updated_at timestamptz not null default now(),
  created_at timestamptz not null default now(),
  unique (task_id, item_id)
);

alter table cdp_task_items add column if not exists question_id varchar(128);
alter table cdp_task_items add column if not exists status int not null default 0;
alter table cdp_task_items add column if not exists result_status varchar(32) not null default 'PENDING';
alter table cdp_task_items add column if not exists result_imported_at timestamptz;
alter table cdp_task_items add column if not exists updated_at timestamptz not null default now();
alter table cdp_task_items drop column if exists result_data;
alter table cdp_task_items drop column if exists result_row_count;

create index if not exists idx_cdp_task_items_question_id on cdp_task_items(task_id, question_id);
create index if not exists idx_cdp_task_items_task_status on cdp_task_items(task_id, status);
create index if not exists idx_cdp_task_items_task_result_status on cdp_task_items(task_id, result_status);
create index if not exists idx_cdp_task_items_task_seq on cdp_task_items(task_id, seq);
create index if not exists idx_cdp_task_items_detail_priority on cdp_task_items(
  task_id,
  (
    case
      when result_status in ('FAILED','PARSE_FAILED','ERROR') then 0
      when result_status <> 'IMPORTED' or status <> 2 then 1
      else 2
    end
  ),
  seq,
  id
);

create table if not exists cdp_task_events (
  id bigserial primary key,
  task_id bigint not null,
  event_type varchar(64) not null,
  payload jsonb,
  created_at timestamptz not null default now()
);

create index if not exists idx_cdp_task_events_task_id on cdp_task_events(task_id, created_at);
create index if not exists idx_cdp_task_events_type_created_at on cdp_task_events(event_type, created_at);

drop table if exists cdp_result_fetch_attempts;

create table if not exists cdp_task_result_files (
  id bigserial primary key,
  task_id bigint not null,
  batch_no varchar(128) not null,
  external_batch_id varchar(128) not null,
  source_type varchar(32) not null,
  object_key text,
  file_url text,
  file_name varchar(255),
  checksum varchar(128),
  parse_status varchar(32) not null default 'PENDING',
  row_count int not null default 0,
  valid_row_count int not null default 0,
  invalid_row_count int not null default 0,
  last_error text,
  fetched_at timestamptz,
  parsed_at timestamptz,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create unique index if not exists idx_cdp_task_result_files_object_key
  on cdp_task_result_files(batch_no, object_key)
  where object_key is not null;

create unique index if not exists idx_cdp_task_result_files_file_url
  on cdp_task_result_files(batch_no, file_url)
  where file_url is not null;

create index if not exists idx_cdp_task_result_files_task_id on cdp_task_result_files(task_id, created_at);
create index if not exists idx_cdp_task_result_files_batch_no on cdp_task_result_files(batch_no, created_at);
create index if not exists idx_cdp_task_result_files_upload_pending
  on cdp_task_result_files(task_id, source_type, parse_status, created_at)
  where object_key is not null;

create table if not exists cdp_task_item_results (
  id bigserial primary key,
  task_id bigint not null,
  task_item_id bigint not null,
  batch_no varchar(128) not null,
  item_id varchar(128) not null,
  platform_code varchar(64),
  question text,
  answer_text text,
  thinking_process text,
  generated_at timestamptz,
  generated_at_text varchar(64),
  recommended_questions jsonb not null default '[]'::jsonb,
  device_id varchar(128),
  recording_url text,
  references_json jsonb not null default '[]'::jsonb,
  cleaned_payload jsonb not null,
  parse_status varchar(32) not null default 'SUCCESS',
  parse_error text,
  result_file_id bigint not null,
  seq int not null default 1,
  answer_sheet_row_index int,
  reference_count int not null default 0,
  row_hash varchar(128),
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

alter table cdp_task_item_results add column if not exists seq int not null default 1;
alter table cdp_task_item_results drop constraint if exists cdp_task_item_results_task_item_id_key;

create unique index if not exists idx_cdp_task_item_results_task_item_seq on cdp_task_item_results(task_item_id, seq);
create index if not exists idx_cdp_task_item_results_task_id_seq on cdp_task_item_results(task_id, item_id, seq);
create index if not exists idx_cdp_task_item_results_batch_no_seq on cdp_task_item_results(batch_no, item_id, seq);
create index if not exists idx_cdp_task_item_results_result_file_id
  on cdp_task_item_results(result_file_id, task_item_id, seq);

create table if not exists cdp_task_result_rows (
  id bigserial primary key,
  task_id bigint not null,
  batch_no varchar(128) not null,
  external_batch_id varchar(128) not null,
  result_file_id bigint not null,
  sheet_name varchar(128) not null,
  row_index int not null,
  item_id varchar(128),
  seq int not null default 0,
  row_data jsonb not null,
  row_hash varchar(128),
  parse_error text,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  unique (result_file_id, sheet_name, row_index)
);

alter table cdp_task_result_rows add column if not exists seq int not null default 0;

create index if not exists idx_cdp_task_result_rows_task_id on cdp_task_result_rows(task_id, row_index);
create index if not exists idx_cdp_task_result_rows_batch_no on cdp_task_result_rows(batch_no, row_index);
create index if not exists idx_cdp_task_result_rows_task_item_seq on cdp_task_result_rows(task_id, item_id, seq) where item_id is not null;

create table if not exists cdp_task_callbacks (
  id bigserial primary key,
  task_id bigint not null,
  target_url text not null,
  request_payload jsonb,
  response_status int,
  response_body text,
  status varchar(32) not null,
  retry_count int not null default 0,
  last_error text,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create index if not exists idx_cdp_task_callbacks_status on cdp_task_callbacks(status, created_at);
create index if not exists idx_cdp_task_callbacks_task_id on cdp_task_callbacks(task_id, created_at);
create index if not exists idx_cdp_task_callbacks_task_status on cdp_task_callbacks(task_id, status);

do $$
declare
  constraint_record record;
begin
  for constraint_record in
    select conrelid::regclass::text as table_name, conname
    from pg_constraint
    where contype = 'f'
      and conrelid::regclass::text in (
        'cdp_task_items',
        'cdp_task_events',
        'cdp_task_result_files',
        'cdp_task_item_results',
        'cdp_task_result_rows',
        'cdp_task_callbacks'
      )
  loop
    execute format('alter table %s drop constraint if exists %I', constraint_record.table_name, constraint_record.conname);
  end loop;
end $$;

create table if not exists cdp_admin_audit_logs (
  id bigserial primary key,
  user_id varchar(128) not null,
  username varchar(128) not null,
  role varchar(64) not null,
  action varchar(64) not null,
  resource_type varchar(64) not null,
  resource_id varchar(128) not null,
  request_id varchar(128),
  payload jsonb,
  created_at timestamptz not null default now()
);

create index if not exists idx_cdp_admin_audit_logs_resource
  on cdp_admin_audit_logs(resource_type, resource_id, created_at);

create index if not exists idx_cdp_admin_audit_logs_user
  on cdp_admin_audit_logs(user_id, created_at);
