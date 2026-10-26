package data

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PullRepo struct{ tasks *TaskRepo }

func NewPullRepo(db *pgxpool.Pool) *PullRepo { return &PullRepo{tasks: NewTaskRepo(db)} }
func (r *PullRepo) WithTaskLock(ctx context.Context, id int64, fn func(context.Context) error) error {
	return r.tasks.WithTaskLock(ctx, id, fn)
}
func (r *PullRepo) Active(ctx context.Context, id int64) error {
	var active bool
	err := r.tasks.dbFor(ctx).QueryRow(ctx, `select execution_mode='pull' and status not in ('FAILED','REASSIGNED') and deleted_at is null from cdp_tasks where id=$1`, id).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.ErrTaskNotFound
	}
	if err != nil {
		return err
	}
	if !active {
		return biz.ErrTaskClosed
	}
	return nil
}
func (r *PullRepo) Claim(ctx context.Context, provider string, in biz.PullClaimInput, hash string) (biz.PullShard, error) {
	empty := biz.PullShard{Items: []biz.TaskItem{}}
	tx, err := r.tasks.dbFor(ctx).Begin(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback(ctx)
	// Successful request replays serialize before inspecting the shard unique key.
	if _, err = tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1,17))`, provider+"/"+in.RequestID); err != nil {
		return empty, err
	}
	var shardID, taskID int64
	var storedHash string
	err = tx.QueryRow(ctx, `select id,task_id,request_hash from cdp_task_shards where provider_code=$1 and request_id=$2`, provider, in.RequestID).Scan(&shardID, &taskID, &storedHash)
	if err == nil {
		if storedHash != hash {
			return empty, biz.ErrPullConflict
		}
		var valid bool
		if err = tx.QueryRow(ctx, `select status='SUBMITTED' and deleted_at is null from cdp_tasks where id=$1 for share`, taskID).Scan(&valid); err != nil {
			return empty, err
		}
		if !valid {
			return empty, biz.ErrTaskClosed
		}
		out, err := readShard(ctx, tx, shardID)
		if err != nil {
			return empty, err
		}
		return out, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return empty, err
	}
	err = tx.QueryRow(ctx, `select t.id from cdp_tasks t where t.provider_code=$1 and t.execution_mode='pull' and t.device=$2 and t.status='SUBMITTED' and t.deleted_at is null
 and exists(select 1 from cdp_task_items i where i.task_id=t.id and i.platform_code=$3 and i.shard_id is null)
 order by t.created_at,t.id limit 1 for update of t skip locked`, provider, in.Device, in.Channel).Scan(&taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return empty, tx.Commit(ctx)
	}
	if err != nil {
		return empty, err
	}
	rows, err := tx.Query(ctx, `select id from cdp_task_items where task_id=$1 and platform_code=$2 and shard_id is null order by id limit $3 for update skip locked`, taskID, in.Channel, in.Limit)
	if err != nil {
		return empty, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return empty, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return empty, err
	}
	if len(ids) == 0 {
		return empty, tx.Commit(ctx)
	}
	err = tx.QueryRow(ctx, `insert into cdp_task_shards(task_id,provider_code,channel,request_id,request_hash,item_count) values($1,$2,$3,$4,$5,$6) returning id`, taskID, provider, in.Channel, in.RequestID, hash, len(ids)).Scan(&shardID)
	if err != nil {
		return empty, err
	}
	if _, err = tx.Exec(ctx, `update cdp_task_items set shard_id=$1 where id=any($2)`, shardID, ids); err != nil {
		return empty, err
	}
	if _, err = tx.Exec(ctx, `insert into cdp_task_events(task_id,event_type,payload) values($1,'PULL_CLAIMED',jsonb_build_object('shardId',$2::bigint,'count',$3::int))`, taskID, shardID, len(ids)); err != nil {
		return empty, err
	}
	out, err := readShard(ctx, tx, shardID)
	if err != nil {
		return empty, err
	}
	return out, tx.Commit(ctx)
}
func readShard(ctx context.Context, tx pgx.Tx, id int64) (biz.PullShard, error) {
	out := biz.PullShard{ID: id, Items: []biz.TaskItem{}}
	err := tx.QueryRow(ctx, `select s.task_id,coalesce(nullif(t.callback_batch_no,''),t.batch_no),t.device,s.channel,s.status from cdp_task_shards s join cdp_tasks t on t.id=s.task_id where s.id=$1`, id).Scan(&out.TaskID, &out.BatchNo, &out.Device, &out.Channel, &out.Status)
	if err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `select item_id,platform_code,seq,question,deepthink,web_search from cdp_task_items where shard_id=$1 order by id`, id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var i biz.TaskItem
		if err = rows.Scan(&i.ItemID, &i.PlatformCode, &i.Seq, &i.Question, &i.Deepthink, &i.WebSearch); err != nil {
			return out, err
		}
		out.Items = append(out.Items, i)
	}
	return out, rows.Err()
}
func (r *PullRepo) Accept(ctx context.Context, provider string, in biz.PullSubmissionInput, hash string) (int64, error) {
	var taskID, id int64
	err := r.tasks.dbFor(ctx).QueryRow(ctx, `select task_id from cdp_task_shards where id=$1 and provider_code=$2`, in.ShardID, provider).Scan(&taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, biz.ErrTaskNotFound
	}
	if err != nil {
		return 0, err
	}
	err = r.WithTaskLock(ctx, taskID, func(ctx context.Context) error {
		if err := r.Active(ctx, taskID); err != nil {
			return err
		}
		tx, err := r.tasks.dbFor(ctx).Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		var batch string
		if err = tx.QueryRow(ctx, `select coalesce(nullif(callback_batch_no,''),batch_no) from cdp_tasks where id=$1`, taskID).Scan(&batch); err != nil {
			return err
		}
		if batch != in.BatchNo {
			return biz.ErrInvalidTaskRequest
		}
		var oldHash string
		err = tx.QueryRow(ctx, `select id,request_hash from cdp_task_result_submissions where shard_id=$1 and request_id=$2`, in.ShardID, in.RequestID).Scan(&id, &oldHash)
		if err == nil {
			if oldHash != hash {
				return biz.ErrPullConflict
			}
			return tx.Commit(ctx)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		itemIDs := []string{}
		for _, a := range in.Results {
			itemIDs = append(itemIDs, a.ItemID)
		}
		var invalid bool
		if err = tx.QueryRow(ctx, `select exists(select 1 from unnest($1::text[]) x(item_id) where not exists(select 1 from cdp_task_items i where i.task_id=$2 and i.shard_id=$3 and i.item_id=x.item_id))`, itemIDs, taskID, in.ShardID).Scan(&invalid); err != nil {
			return err
		}
		if invalid {
			return biz.ErrInvalidTaskRequest
		}
		body, _ := json.Marshal(in)
		if err = tx.QueryRow(ctx, `insert into cdp_task_result_submissions(task_id,shard_id,request_id,request_hash,body) values($1,$2,$3,$4,$5) returning id`, taskID, in.ShardID, in.RequestID, hash, body).Scan(&id); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `update cdp_task_shards set status='RECEIVING',updated_at=now(),completed_at=null where id=$1`, in.ShardID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `insert into cdp_task_events(task_id,event_type,payload) values($1,'PULL_RESULT_ACCEPTED',jsonb_build_object('submissionId',$2::bigint))`, taskID, id); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
	return id, err
}
func (r *PullRepo) Lease(ctx context.Context) (*biz.PullSubmission, error) {
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(tokenBytes)
	var sub biz.PullSubmission
	var body []byte
	err := r.tasks.dbFor(ctx).QueryRow(ctx, `with candidate as (
 select id from cdp_task_result_submissions where (status in ('ACCEPTED','RETRY_WAIT') and next_retry_at<=now()) or (status='PROCESSING' and lease_until<now()) order by id limit 1 for update skip locked
 ) update cdp_task_result_submissions s set status='PROCESSING',lease_token=$1,lease_until=now()+interval '11 minutes',updated_at=now()
 from candidate c where s.id=c.id returning s.id,s.task_id,s.shard_id,s.body`, token).Scan(&sub.ID, &sub.TaskID, &sub.ShardID, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sub.LeaseToken = token
	if err = json.Unmarshal(body, &sub.Input); err != nil {
		return nil, err
	}
	return &sub, nil
}
func (r *PullRepo) ApplyAnswer(ctx context.Context, sub *biz.PullSubmission, a biz.PullAnswer, result biz.TaskItemResult) error {
	risks, _ := json.Marshal(result.RiskItems)
	cards := result.ProductCards
	if cards == nil {
		cards = []biz.ProductCard{}
	}
	_, err := r.tasks.dbFor(ctx).Exec(ctx, `insert into cdp_task_item_results(task_id,task_item_id,batch_no,item_id,platform_code,question,seq,answer_text,thinking_process,generated_at,generated_at_text,recommended_questions,device_id,recording_url,references_json,reference_count,cleaned_payload,parse_status,parse_error,source_submission_id,validation_status,risk_items,validation_rule_version,validated_at,product_cards)
 select i.task_id,i.id,t.batch_no,i.item_id,i.platform_code,i.question,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$3,$17,$18,$19,$20,coalesce($23::jsonb,'[]'::jsonb)
 from cdp_task_items i join cdp_tasks t on t.id=i.task_id
 where i.task_id=$1 and i.shard_id=$2 and i.item_id=$21 and t.status not in ('REASSIGNED','FAILED')
 and exists(select 1 from cdp_task_result_submissions where id=$3 and status='PROCESSING' and lease_token=$22)
 on conflict(task_item_id,seq) do update set
 product_cards=excluded.product_cards,answer_text=excluded.answer_text,thinking_process=excluded.thinking_process,generated_at=excluded.generated_at,generated_at_text=excluded.generated_at_text,recommended_questions=excluded.recommended_questions,device_id=excluded.device_id,recording_url=excluded.recording_url,references_json=excluded.references_json,reference_count=excluded.reference_count,cleaned_payload=excluded.cleaned_payload,parse_status=excluded.parse_status,parse_error=excluded.parse_error,source_submission_id=excluded.source_submission_id,validation_status=excluded.validation_status,risk_items=excluded.risk_items,validation_rule_version=excluded.validation_rule_version,validated_at=excluded.validated_at,updated_at=now()
 where cdp_task_item_results.source_submission_id < excluded.source_submission_id`, sub.TaskID, sub.ShardID, sub.ID, result.Seq, result.AnswerText, result.ThinkingProcess, nullableTime(result.GeneratedAt), result.GeneratedAtText, result.RecommendedQuestions, result.DeviceID, result.RecordingURL, result.ReferencesJSON, result.ReferenceCount, result.CleanedPayload, result.ParseStatus, result.ParseError, result.Status, risks, result.RuleVersion, result.ValidatedAt, a.ItemID, sub.LeaseToken, cards)
	return err
}
func (r *PullRepo) Finish(ctx context.Context, sub *biz.PullSubmission) error {
	tx, err := r.tasks.dbFor(ctx).Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `update cdp_task_result_submissions set status='APPLIED',processed_at=now(),updated_at=now(),lease_token=null,lease_until=null,last_error=null where id=$1 and lease_token=$2 and status='PROCESSING'`, sub.ID, sub.LeaseToken)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("submission lease lost")
	}
	if sub.Input.Finished {
		if _, err = tx.Exec(ctx, `update cdp_task_shards set finished_submission_id=greatest(coalesce(finished_submission_id,0),$2),updated_at=now() where id=$1`, sub.ShardID, sub.ID); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `insert into cdp_task_events(task_id,event_type,payload) values($1,'PULL_RESULT_APPLIED',jsonb_build_object('submissionId',$2::bigint))`, sub.TaskID, sub.ID); err != nil {
		return err
	}
	if err = r.reconcileTx(ctx, tx, sub.TaskID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *PullRepo) Retry(ctx context.Context, sub *biz.PullSubmission, cause error) error {
	closed := errors.Is(cause, biz.ErrTaskClosed) || errors.Is(cause, biz.ErrTaskNotFound)
	// A completion transaction failure leaves the lease recoverable even after partial answer writes.
	_, err := r.tasks.dbFor(ctx).Exec(ctx, `update cdp_task_result_submissions set status=case when $4 then 'REJECTED_CLOSED' when retry_count>=4 then 'FAILED' else 'RETRY_WAIT' end,retry_count=retry_count+1,next_retry_at=now()+interval '5 seconds'*power(2,least(retry_count,6)),lease_token=null,lease_until=null,last_error=$3,updated_at=now() where id=$1 and lease_token=$2`, sub.ID, sub.LeaseToken, cause.Error(), closed)
	if err != nil {
		return err
	}
	return r.tasks.AddEvent(ctx, sub.TaskID, "PULL_RESULT_FAILED", map[string]any{"submissionId": sub.ID, "error": cause.Error()})
}
func (r *PullRepo) Reconcile(ctx context.Context, id int64) error {
	return r.WithTaskLock(ctx, id, func(ctx context.Context) error {
		tx, err := r.tasks.dbFor(ctx).Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if err = r.reconcileTx(ctx, tx, id); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
}

func (r *PullRepo) reconcileTx(ctx context.Context, tx pgx.Tx, id int64) error {
	// Failed submissions cease blocking when every key was already written or
	// replaced by a newer persisted source. Partial successful writes remain valid.
	// Their finished declaration is preserved before superseding them.
	if _, err := tx.Exec(ctx, `with superseded as (
 update cdp_task_result_submissions s set status='SUPERSEDED',updated_at=now(),processed_at=now()
 where s.task_id=$1 and s.status='FAILED' and not exists(
 select 1 from jsonb_array_elements(s.body->'results') a where not exists(
 select 1 from cdp_task_item_results r where r.task_id=s.task_id and r.item_id=a->>'itemId' and r.seq=(a->>'answerSeq')::int and r.source_submission_id>=s.id))
 returning shard_id,id,body
 ) update cdp_task_shards sh set finished_submission_id=greatest(coalesce(sh.finished_submission_id,0),s.id)
 from (select shard_id,max(id) id from superseded where (body->>'finished')::boolean group by shard_id) s where sh.id=s.shard_id`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update cdp_task_items i set status=2,result_status='IMPORTED',updated_at=now()
 where i.task_id=$1 and exists(select 1 from cdp_task_item_results r where r.task_item_id=i.id and r.cleaned_payload->>'executionStatus' in ('SUCCESS','FAILED')) and (i.status<>2 or i.result_status<>'IMPORTED')`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update cdp_task_shards s set status='COMPLETED',completed_at=coalesce(completed_at,now()),updated_at=now()
 where s.task_id=$1 and s.finished_submission_id is not null
 and not exists(select 1 from cdp_task_items i where i.shard_id=s.id and not exists(select 1 from cdp_task_item_results r where r.task_item_id=i.id and r.cleaned_payload->>'executionStatus' in ('SUCCESS','FAILED')))
 and not exists(select 1 from cdp_task_result_submissions x where x.shard_id=s.id and x.status not in ('APPLIED','SUPERSEDED')) and s.status<>'COMPLETED'`, id); err != nil {
		return err
	}
	var total, success, failed, rows, risk, invalid int
	var ready bool
	var status biz.TaskStatus
	var provider string
	if err := tx.QueryRow(ctx, `select t.status,t.provider_code,stats.item_count,stats.result_success_count,stats.result_failed_count,
 (select count(*) from cdp_task_item_results where task_id=t.id),
 (select count(*) from cdp_task_item_results where task_id=t.id and validation_status in ('RISK','INVALID')),
 (select count(*) from cdp_task_item_results where task_id=t.id and validation_status='INVALID'),
 not exists(select 1 from cdp_task_items where task_id=t.id and shard_id is null)
 and exists(select 1 from cdp_task_shards where task_id=t.id)
 and not exists(select 1 from cdp_task_shards where task_id=t.id and status<>'COMPLETED')
 and not exists(select 1 from cdp_task_result_submissions where task_id=t.id and status not in ('APPLIED','SUPERSEDED'))
 from cdp_tasks t cross join lateral (`+pullItemStatsSQL("t.id")+`) stats where t.id=$1 for update of t`, id).Scan(&status, &provider, &total, &success, &failed, &rows, &risk, &invalid, &ready); err != nil {
		return err
	}
	next := biz.PullCompletionStatus(status, ready && success+failed == total, risk == 0 || biz.AllowsRiskCallback(ctx, provider))
	if _, err := tx.Exec(ctx, `update cdp_tasks set success_count=$2,failed_count=$3,total_count=$4,result_row_count=$5,risk_count=$6,invalid_answer_count=$7,
 risk_stats_updated_at=now(),risk_summary=coalesce((select jsonb_object_agg(code,n) from (select v->>'code' code,count(*) n from cdp_task_item_results r cross join lateral jsonb_array_elements(r.risk_items) v where r.task_id=$1 group by v->>'code') counts),'{}'::jsonb),
 status=$8,result_imported_at=case when $9 then coalesce(result_imported_at,now()) else result_imported_at end,updated_at=now() where id=$1 and status not in ('REASSIGNED','FAILED')`, id, success, failed, total, rows, risk, invalid, next, ready); err != nil {
		return err
	}
	if next != status {
		if _, err := tx.Exec(ctx, `insert into cdp_task_events(task_id,event_type,payload) values($1,'PULL_COMPLETED',jsonb_build_object('status',$2::text))`, id, next); err != nil {
			return err
		}
	}
	return nil
}
func pullItemStatsSQL(taskID string) string {
	return `select count(*)::int item_count,count(*) filter(where r.success)::int result_success_count,count(*) filter(where not r.success and r.failed)::int result_failed_count from cdp_task_items i left join (
 select task_item_id,bool_or(cleaned_payload->>'executionStatus'='SUCCESS') success,bool_or(cleaned_payload->>'executionStatus'='FAILED') failed from cdp_task_item_results where task_id=` + taskID + ` group by task_item_id) r on r.task_item_id=i.id where i.task_id=` + taskID
}
func (r *PullRepo) Progress(ctx context.Context, id int64) (biz.PullProgress, error) {
	var out biz.PullProgress
	err := r.tasks.dbFor(ctx).QueryRow(ctx, `select t.id,t.device,t.execution_mode,t.status,stats.item_count,stats.result_success_count,stats.result_failed_count,
 (select count(*) from cdp_task_items where task_id=t.id and shard_id is null),
 (select count(*) from cdp_task_shards where task_id=t.id),
 (select count(*) from cdp_task_shards where task_id=t.id and status='COMPLETED'),
 (select count(*) from cdp_task_result_submissions where task_id=t.id and status in ('ACCEPTED','PROCESSING','RETRY_WAIT')),
 (select count(*) from cdp_task_result_submissions where task_id=t.id and status='FAILED')
 from cdp_tasks t cross join lateral (`+taskItemStatsSQL("t.id")+`) stats where t.id=$1 and t.deleted_at is null`, id).Scan(&out.TaskID, &out.Device, &out.ExecutionMode, &out.Status, &out.Total, &out.Success, &out.Failed, &out.Unclaimed, &out.Shards, &out.CompletedShards, &out.PendingSubmissions, &out.FailedSubmissions)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, biz.ErrTaskNotFound
	}
	out.Completed = out.Success + out.Failed
	if out.ExecutionMode != "pull" {
		out.Unclaimed = 0
	}
	return out, err
}

func (r *PullRepo) callbackReadiness(ctx context.Context, id int64) (biz.CallbackReadiness, error) {
	var out biz.CallbackReadiness
	err := r.tasks.dbFor(ctx).QueryRow(ctx, `select stats.item_count,stats.result_success_count,stats.result_failed_count,t.risk_count,t.risk_stats_updated_at is not null,
 t.status in ('RESULT_IMPORTED','CALLBACK_PENDING','DONE')
 and not exists(select 1 from cdp_task_items where task_id=t.id and shard_id is null)
 and not exists(select 1 from cdp_task_shards where task_id=t.id and status<>'COMPLETED')
 and not exists(select 1 from cdp_task_result_submissions where task_id=t.id and status not in ('APPLIED','SUPERSEDED'))
 from cdp_tasks t cross join lateral (`+pullItemStatsSQL("t.id")+`) stats where t.id=$1`, id).Scan(&out.TotalCount, &out.SuccessCount, &out.FailedCount, &out.RiskCount, &out.RiskStatsReady, &out.ProviderDone)
	out.TaskItemCount = out.TotalCount
	out.ImportedCount = out.SuccessCount + out.FailedCount
	out.Ready = out.ProviderDone && out.TotalCount > 0 && out.ImportedCount == out.TotalCount
	out.Reason = callbackReadinessReason(out)
	return out, err
}

// Revisit risk-blocked deliveries when the existing provider risk policy changes.
func (r *PullRepo) ReconcileReady(ctx context.Context) error {
	rows, err := r.tasks.dbFor(ctx).Query(ctx, `select id from cdp_tasks where execution_mode='pull' and status='RESULT_IMPORTED' and deleted_at is null and (risk_count=0 or provider_code=any($1::text[])) order by id limit 20`, biz.RiskCallbackProviderCodes(ctx))
	if err != nil {
		return err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = r.Reconcile(ctx, id); err != nil {
			return err
		}
	}
	return nil
}
