package data

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func migrationQueryJSON(t *testing.T, ctx context.Context, db *pgxpool.Pool, query string) string {
	t.Helper()
	var raw []byte
	if err := db.QueryRow(ctx, query).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	normalized, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(normalized)
}

func TestMigrationReplayPreservesDataWithoutForeignKeys(t *testing.T) {
	db := openQueryOptimizationTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err := db.Exec(ctx, `
insert into cdp_tasks(id,batch_no,provider_code,status,raw_request,job_id,external_batch_id,provider_external_batch_id,execution_mode,risk_stats_updated_at)
values (1,'pull-clean','test','DONE','{}','job_pull','job_pull',null,'pull','2026-01-01'),
 (2,'pull-polluted','test','DONE','{}','job_bad','job_bad','job_bad','pull','2026-01-01'),
 (3,'push-real','test','DONE','{}','job_push','job_push','vendor-real','push','2026-01-01'),
 (4,'push-pending','test','CREATED','{}','job_pending','job_pending',null,'push',null);
insert into cdp_task_items(id,task_id,item_id,platform_code,seq,question,raw_item) values(1,1,'q','doubao',1,'question','{}');
insert into cdp_task_shards(id,task_id,provider_code,channel,request_id,request_hash,item_count) values(1,1,'test','doubao','claim','hash',1);
insert into cdp_task_result_submissions(id,task_id,shard_id,request_id,request_hash,body) values(1,1,1,'result','hash','{}');
insert into cdp_task_item_results(task_id,task_item_id,batch_no,item_id,cleaned_payload,parse_status,source_submission_id,validation_status)
values(1,1,'pull-clean','q','{"executionStatus":"FAILED"}','EXECUTION_FAILED',1,'SKIPPED');
update cdp_result_validation_rules set keywords='["keep"]',disabled_provider_codes='["legacy"]',risk_callback_allowed_provider_codes='["chosen"]',version=42;
-- Simulate every FK from the previous Pull schema, plus an older legacy FK.
alter table cdp_task_shards add constraint cdp_task_shards_id_task_id_key unique(id,task_id);
alter table cdp_task_shards add constraint previous_task_fk foreign key(task_id) references cdp_tasks(id);
alter table cdp_task_shards add constraint cdp_shards_finished_fk foreign key(finished_submission_id) references cdp_task_result_submissions(id);
alter table cdp_task_result_submissions add constraint previous_submission_task_fk foreign key(task_id) references cdp_tasks(id);
alter table cdp_task_result_submissions add constraint previous_shard_fk foreign key(shard_id,task_id) references cdp_task_shards(id,task_id);
alter table cdp_task_items add constraint previous_item_task_fk foreign key(task_id) references cdp_tasks(id);
alter table cdp_task_items add constraint previous_item_shard_fk foreign key(shard_id) references cdp_task_shards(id);
alter table cdp_task_item_results add constraint previous_source_fk foreign key(source_submission_id) references cdp_task_result_submissions(id);
-- Startup must preserve retired fields/table, even if they still hold data.
alter table cdp_task_items add column result_data jsonb,add column result_row_count integer;
update cdp_task_items set result_data='{"keep":true}',result_row_count=9;
create table cdp_result_fetch_attempts(id integer, evidence text);
insert into cdp_result_fetch_attempts values(1,'keep');
-- Constraint names are not globally unique: the target table must be checked.
alter table cdp_task_item_results drop constraint result_exactly_one_source;
create table unrelated_constraint_name(v integer constraint result_exactly_one_source check(v>0));
`)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join("..", "..", "migrations")
	if err = Migrate(ctx, db, dir); err != nil {
		t.Fatal(err)
	}
	var fks int
	if err = db.QueryRow(ctx, `select count(*) from pg_constraint c join pg_class r on r.oid=c.conrelid where c.contype='f' and r.relnamespace='public'::regnamespace and r.relname like 'cdp_%'`).Scan(&fks); err != nil {
		t.Fatal(err)
	}
	if fks != 0 {
		t.Fatalf("foreign keys remain: %d", fks)
	}
	var providerIDs string
	if err = db.QueryRow(ctx, `select string_agg(coalesce(provider_external_batch_id,'NULL'),',' order by id) from cdp_tasks`).Scan(&providerIDs); err != nil {
		t.Fatal(err)
	}
	if providerIDs != "NULL,NULL,vendor-real,NULL" {
		t.Fatalf("provider identifiers: %s", providerIDs)
	}
	var valid bool
	if err = db.QueryRow(ctx, `select exists(select 1 from pg_constraint where conrelid='cdp_task_item_results'::regclass and conname='result_exactly_one_source' and contype='c')`).Scan(&valid); err != nil {
		t.Fatal(err)
	}
	if !valid {
		t.Fatal("unrelated same-name constraint suppressed result source check")
	}
	if err = db.QueryRow(ctx, `select result_data='{"keep":true}'::jsonb and result_row_count=9 from cdp_task_items where id=1`).Scan(&valid); err != nil || !valid {
		t.Fatalf("legacy columns lost data: %v", err)
	}
	if err = db.QueryRow(ctx, `select evidence='keep' from cdp_result_fetch_attempts where id=1`).Scan(&valid); err != nil || !valid {
		t.Fatalf("legacy table lost data: %v", err)
	}
	const snapshot = `select jsonb_build_object(
 'tasks',(select jsonb_agg(to_jsonb(t) order by id) from cdp_tasks t),
 'rules',(select to_jsonb(r) from cdp_result_validation_rules r),
 'constraints',(select jsonb_agg(jsonb_build_array(conrelid::regclass::text,conname,pg_get_constraintdef(oid)) order by conrelid,conname) from pg_constraint where connamespace='public'::regnamespace),
 'results',(select jsonb_agg(to_jsonb(r) order by id) from cdp_task_item_results r))`
	before := migrationQueryJSON(t, ctx, db, snapshot)
	if err = Migrate(ctx, db, dir); err != nil {
		t.Fatal(err)
	}
	if after := migrationQueryJSON(t, ctx, db, snapshot); after != before {
		t.Fatal("migration replay changed data, validation policy, timestamps or constraints")
	}
	var riskTime time.Time
	if err = db.QueryRow(ctx, `select risk_stats_updated_at from cdp_tasks where id=1`).Scan(&riskTime); err != nil {
		t.Fatal(err)
	}
	if riskTime.Year() != 2026 || riskTime.Month() != time.January || riskTime.Day() != 1 {
		t.Fatal("Pull validation timestamp was fabricated during startup")
	}
}

func TestMigrationUpgradesLegacyProviderColumnOnce(t *testing.T) {
	db := openQueryOptimizationTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err := db.Exec(ctx, `
alter table cdp_tasks drop column provider_external_batch_id;
insert into cdp_tasks(id,batch_no,provider_code,status,raw_request,job_id,external_batch_id)
values(1,'old-provider','test','DONE','{}','job_old','vendor-old'),
 (2,'old-gateway','test','CREATED','{}','job_new','job_new');
`)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join("..", "..", "migrations")
	if err = Migrate(ctx, db, dir); err != nil {
		t.Fatal(err)
	}
	var ids string
	if err = db.QueryRow(ctx, `select string_agg(coalesce(provider_external_batch_id,'NULL'),',' order by id) from cdp_tasks`).Scan(&ids); err != nil {
		t.Fatal(err)
	}
	if ids != "vendor-old,NULL" {
		t.Fatalf("legacy conversion: %s", ids)
	}
	// A later NULL with a non-gateway external ID still must not trigger another backfill.
	if _, err = db.Exec(ctx, `update cdp_tasks set external_batch_id='unconfirmed-id' where id=2`); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, db, dir); err != nil {
		t.Fatal(err)
	}
	var empty bool
	if err = db.QueryRow(ctx, `select provider_external_batch_id is null from cdp_tasks where id=2`).Scan(&empty); err != nil || !empty {
		t.Fatalf("legacy conversion replayed: %v", err)
	}
}

func TestMigrationLockTimeoutRollsBackFile(t *testing.T) {
	db := openQueryOptimizationTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// 000005 adds device before it tries to lock cdp_task_items. Its entire file
	// must roll back if that later DDL cannot acquire the lock.
	if _, err := db.Exec(ctx, `alter table cdp_tasks drop column device`); err != nil {
		t.Fatal(err)
	}
	blocker, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(ctx, `lock table cdp_task_items in access exclusive mode`); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000005_mobile_pull.sql"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = os.WriteFile(filepath.Join(dir, "000005_mobile_pull.sql"), content, 0600); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err = Migrate(ctx, db, dir)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("expected lock timeout, got %v", err)
	}
	if time.Since(started) > 10*time.Second {
		t.Fatal("lock wait exceeded startup limit")
	}
	if err = blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err = db.QueryRow(ctx, `select exists(select 1 from pg_attribute where attrelid='cdp_tasks'::regclass and attname='device' and not attisdropped)`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("partially applied migration: device column survived rollback")
	}
	// Startup can be retried after the lock is released.
	if err = Migrate(ctx, db, dir); err != nil {
		t.Fatal(err)
	}
	var timeout string
	if err = db.QueryRow(ctx, `show lock_timeout`).Scan(&timeout); err != nil {
		t.Fatal(err)
	}
	if timeout != "0" {
		t.Fatalf("migration timeout leaked to pooled business connection: %s", timeout)
	}
}
