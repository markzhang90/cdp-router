package data

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/jackc/pgx/v5"
)

// Each finalization reads current answers once, never adds historical counts.
func aggregateTaskValidation(ctx context.Context, tx pgx.Tx, taskID int64) error {
	var riskCount, invalid, unknown, answers, pending int
	var summary []byte
	err := tx.QueryRow(ctx, `
with answers as materialized (
 select validation_status,risk_items from cdp_task_item_results where task_id=$1
), codes as (
 select code,count(*) as n from (
  select x.code from answers a
  cross join lateral (select distinct value->>'code' as code from jsonb_array_elements(a.risk_items)) x
 ) hits group by code
)
select count(*) filter (where validation_status in ('RISK','INVALID')),
 count(*) filter (where validation_status='INVALID'),
 count(*) filter (where validation_status not in ('PASS','RISK','INVALID')),count(*),
 (select count(*) from cdp_task_result_files where task_id=$1 and parse_status<>'SUCCESS'),
 coalesce((select jsonb_object_agg(code,n) from codes),'{}'::jsonb)
from answers`, taskID).Scan(&riskCount, &invalid, &unknown, &answers, &pending, &summary)
	if err != nil {
		return fmt.Errorf("aggregate answer risks: %w", err)
	}
	ready := answers > 0 && unknown == 0 && pending == 0
	tag, err := tx.Exec(ctx, `update cdp_tasks set risk_count=$2,invalid_answer_count=$3,risk_summary=$4,
 risk_stats_updated_at=case when $5 then coalesce(risk_stats_updated_at,now()) else null end
 where id=$1 and (risk_count<>$2 or invalid_answer_count<>$3 or risk_summary<>$4::jsonb or (risk_stats_updated_at is not null)<>$5)`, taskID, riskCount, invalid, summary, ready)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 && ready {
		payload, _ := json.Marshal(map[string]any{"riskCount": riskCount, "invalidAnswerCount": invalid, "riskSummary": json.RawMessage(summary)})
		_, err = tx.Exec(ctx, `insert into cdp_task_events(task_id,event_type,payload) values ($1,'RESULT_VALIDATED',$2)`, taskID, payload)
	}
	return err
}

// A small optional repository capability keeps legacy unit-only repositories
// independent of the persistence locking implementation.
func (r *TaskRepo) ListTasksWithPendingResultFiles(ctx context.Context, limit int) ([]*biz.Task, error) {
	rows, err := r.dbFor(ctx).Query(ctx, taskSelectSQL()+` where deleted_at is null and status in ('PROVIDER_DONE','RESULT_IMPORTED','CALLBACK_PENDING','DONE')
 and (exists(select 1 from cdp_task_result_files f where f.task_id=cdp_tasks.id and f.parse_status<>'SUCCESS')
 or (risk_stats_updated_at is null and exists(select 1 from cdp_task_item_results i where i.task_id=cdp_tasks.id and i.validation_status<>'UNKNOWN')))
 order by updated_at,id limit $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*biz.Task
	for rows.Next() {
		t, err := r.scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// The legacy partial-delivery operation keeps its task state, but still must
// publish the current answer risks after importing files.
func (r *TaskRepo) RefreshTaskValidation(ctx context.Context, taskID int64) error {
	return r.WithTaskLock(ctx, taskID, func(ctx context.Context) error {
		tx, err := r.dbFor(ctx).Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if err = aggregateTaskValidation(ctx, tx, taskID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
}
