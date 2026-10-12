package data

import (
	"context"
	"encoding/json"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
)

func (r *TaskRepo) ListInitialValidationTasks(ctx context.Context, limit int, taskID int64) ([]int64, error) {
	rows, err := r.db.Query(ctx, `select id from cdp_tasks t where execution_mode='push' and deleted_at is null and status not in ('DONE','REASSIGNED') and ($2=0 or id=$2) and exists(select 1 from cdp_task_item_results a where a.task_id=t.id and a.validation_status in ('UNKNOWN','SKIPPED')) order by id limit $1`, limit, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (r *TaskRepo) SaveInitialAnswerValidation(ctx context.Context, id int64, updates []biz.InitialAnswerValidation) error {
	return r.WithTaskLock(ctx, id, func(ctx context.Context) error {
		tx, err := r.dbFor(ctx).Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		var count int64
		for _, a := range updates {
			raw, err := json.Marshal(a.RiskItems)
			if err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, `update cdp_task_item_results set validation_status=$4,risk_items=$5,validation_rule_version=$6,validated_at=$7 where task_id=$1 and item_id=$2 and seq=$3 and validation_status in ('UNKNOWN','SKIPPED')`, id, a.ItemID, a.Seq, a.Status, raw, a.RuleVersion, a.ValidatedAt)
			if err != nil {
				return err
			}
			count += tag.RowsAffected()
		}
		if count > 0 {
			if _, err = tx.Exec(ctx, `update cdp_tasks set risk_stats_updated_at=null where id=$1`, id); err != nil {
				return err
			}
			payload, _ := json.Marshal(map[string]any{"answerCount": count, "triggerType": "INITIAL_VALIDATION_MIGRATION"})
			if _, err = tx.Exec(ctx, `insert into cdp_task_events(task_id,event_type,payload) values($1,'RESULT_VALIDATION_INITIALIZED',$2)`, id, payload); err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	})
}

func (r *TaskRepo) ListSkippedValidationTasks(ctx context.Context, limit int) ([]int64, error) {
	rows, err := r.db.Query(ctx, `select id from cdp_tasks t where execution_mode='push' and deleted_at is null and status<>'REASSIGNED' and exists(select 1 from cdp_task_item_results a where a.task_id=t.id and a.validation_status='SKIPPED') order by id limit $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
