-- Additive single-instance startup migration; preserves historical and Push results.
begin;
set local lock_timeout = '5s';
set local statement_timeout = '120s';

alter table cdp_task_item_results
  add column if not exists product_cards jsonb not null default '[]'::jsonb;

commit;
