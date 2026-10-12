package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/xuri/excelize/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/GitMarkZhang/cdp-router/internal/callback"
	"github.com/GitMarkZhang/cdp-router/internal/cleaner"
	"github.com/GitMarkZhang/cdp-router/internal/config"
	"github.com/GitMarkZhang/cdp-router/internal/data"
	"github.com/GitMarkZhang/cdp-router/internal/httpapi"
	"github.com/GitMarkZhang/cdp-router/internal/pkg/ossutil"
	"github.com/GitMarkZhang/cdp-router/internal/provider"
	"github.com/GitMarkZhang/cdp-router/internal/service"
)

func TestPullHTTPWithRealPersistence(t *testing.T) {
	t.Run("default", func(t *testing.T) { testPullHTTP(t, false) })
	t.Run("yangshi", func(t *testing.T) { testPullHTTP(t, true) })
}
func testPullHTTP(t *testing.T, yangshi bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cfg, err := loadLocalE2EConfig(t)
	if err != nil {
		t.Fatal(err)
	}
	if err = data.EnsureDatabase(ctx, cfg.Postgres); err != nil {
		t.Fatal(err)
	}
	db, err := data.Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = data.Migrate(ctx, db, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	// Startup migration replay must remain safe for existing data.
	if err = data.Migrate(ctx, db, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	redis, cleanup := openRealRedisForTest(t, ctx, cfg)
	defer cleanup()
	defer redis.Close()
	var callbacks, pushes, uploads atomic.Int32
	var upstreamURL string
	var uploaded atomic.Value
	var lastPushDevice atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/upload-url") {
			json.NewEncoder(w).Encode(map[string]any{"status": 0, "data": map[string]any{"uploadUrl": upstreamURL + "/upload", "method": "PUT", "bucket": "local", "key": "result.xlsx"}})
			return
		}
		if r.URL.Path == "/upload" {
			b, _ := io.ReadAll(r.Body)
			uploaded.Store(b)
			uploads.Add(1)
			return
		}
		callbacks.Add(1)
		io.WriteString(w, `{"accepted":true}`)
	}))
	defer upstream.Close()
	upstreamURL = upstream.URL
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pushes.Add(1)
		var request struct {
			Device string `json:"device"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		lastPushDevice.Store(request.Device)
		io.WriteString(w, `{"code":0,"data":{"externalBatchId":"push-test","acceptedCount":1}}`)
	}))
	defer downstream.Close()
	cfg.Provider = map[string]config.ProviderConfig{
		"push":  {Code: "push", Token: "push-token", SubmitURL: downstream.URL, Enabled: true, QPSLimit: 100, ConcurrencyLimit: 10},
		"pull":  {Code: "pull", Token: "pull-token", InteractionMode: "pull", SupportedDevices: []string{"mobile", "web"}, DeviceChannels: map[string][]string{"mobile": {"doubao", "qianwen"}}, Enabled: true, QPSLimit: 1000, ConcurrencyLimit: 20},
		"pull2": {Code: "pull2", Token: "pull2-token", InteractionMode: "pull", SupportedDevices: []string{"mobile"}, DeviceChannels: map[string][]string{"mobile": {"doubao", "qianwen"}}, Enabled: true, QPSLimit: 1000, ConcurrencyLimit: 20},
	}
	cfg.Provider["mobile_push"] = config.ProviderConfig{Code: "mobile_push", Token: "mobile-push-token", SubmitURL: downstream.URL, SupportedDevices: []string{"mobile"}, DeviceChannels: map[string][]string{"mobile": {"doubao"}}, Enabled: true, QPSLimit: 100, ConcurrencyLimit: 10}
	setDefaultProviderCode(cfg, "push")
	if yangshi {
		user := cfg.PlatformUsers["mainland"]
		user.CallbackMode = "YANGSHI"
		raw, _ := json.Marshal(map[string]string{"baseURL": upstream.URL, "providerCode": "local", "providerToken": "local"})
		user.CallbackConfig = string(raw)
		cfg.PlatformUsers["mainland"] = user
	}
	users := syncPlatformUsers(t, ctx, db, cfg)
	if _, err = db.Exec(ctx, `update cdp_dispatch_profiles set config='{"providers":[{"code":"push"},{"code":"pull"}]}'::jsonb`); err != nil {
		t.Fatal(err)
	}
	repo := data.NewTaskRepo(db)
	pullRepo := data.NewPullRepo(db)
	registry := provider.NewRegistry(cfg.Provider)
	cache := biz.NewValidationRuleCache(repo, time.Minute)
	if err = cache.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	store, err := ossutil.New(cfg.OSS)
	if err != nil {
		t.Fatal(err)
	}
	tasks := biz.NewTaskUsecase(repo, registry, registry, store, cleaner.NewExcelParser(), callback.NewClient(time.Second, time.Second, cfg.PlatformUsers, store), biz.TaskUsecaseOptions{PublicBaseURL: cfg.Server.PublicBaseURL, ValidationRules: cache, CallbackMaxTry: 3})
	pull := biz.NewPullUsecase(pullRepo, cache)
	ts := service.NewTaskService(tasks, registry, users, cfg.OSS.Bucket)
	admin := service.NewAdminService(cfg.Admin, redis, data.NewAdminRepo(db), tasks, cfg.Provider)
	server := httptest.NewServer(httpapi.New(ts, admin, service.NewPullService(pull, ts, cfg.Provider)).Handler())
	defer server.Close()
	decode := func(resp *http.Response, want int) map[string]any {
		t.Helper()
		body := readBody(resp)
		if resp.StatusCode != want {
			t.Fatalf("HTTP %d want %d: %s", resp.StatusCode, want, body)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	apiKey := cfg.PlatformUsers["mainland"].APIKey
	create := func(batch, device string, channels ...string) *biz.Task {
		t.Helper()
		items := []map[string]any{}
		for i, c := range channels {
			items = append(items, map[string]any{"itemId": fmt.Sprintf("q%d", i+1), "platformCode": c, "seq": i + 1, "question": "请回答这个问题"})
		}
		decode(postJSONWithAPIKey(t, server.URL+"/api/v1/tasks", apiKey, map[string]any{"batchNo": batch, "device": device, "bizDate": time.Now().Format("2006-01-02"), "callbackUrl": upstream.URL, "items": items}), 200)
		task, err := repo.GetTaskByBatchNo(ctx, batch)
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	claim := func(request, channel string, limit int) map[string]any {
		t.Helper()
		return decode(postJSON(t, server.URL+"/external/gettask", "pull-token", map[string]any{"requestId": request, "device": "mobile", "channel": channel, "limit": limit}), 200)["data"].(map[string]any)
	}
	submit := func(request, batch, shard string, finished bool, results ...map[string]any) map[string]any {
		t.Helper()
		return decode(postJSON(t, server.URL+"/external/taskresult", "pull-token", map[string]any{"requestId": request, "batchNo": batch, "shardId": shard, "finished": finished, "results": results}), 202)["data"].(map[string]any)
	}
	drain := func() {
		t.Helper()
		for i := 0; i < 30; i++ {
			ok, err := pull.ProcessOne(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				return
			}
		}
		t.Fatal("consumer did not drain")
	}
	answer := func(item, text string, seq int) map[string]any {
		return map[string]any{"itemId": item, "answerSeq": seq, "executionStatus": "SUCCESS", "answerText": strings.Repeat(text, 25), "references": []map[string]any{{"url": "https://example.org/source", "title": "来源"}}}
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRow(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// Authentication and empty polling produce no claim history.
	resp := postJSON(t, server.URL+"/external/gettask", "bad", map[string]any{})
	if resp.StatusCode != 401 {
		t.Fatal(resp.StatusCode)
	}
	resp.Body.Close()
	empty := claim("empty", "doubao", 100)
	if len(empty["items"].([]any)) != 0 || count(`select count(*) from cdp_task_shards`) != 0 {
		t.Fatal("empty poll persisted a shard")
	}
	pushTask := create("legacy", "", "doubao")
	if pushTask.ExecutionMode != "push" || pushTask.Device != "web" || pushes.Load() != 1 || lastPushDevice.Load() != "" {
		t.Fatalf("legacy defaults: %+v", pushTask)
	}
	task := create("mobile-a", "mobile", "doubao", "doubao", "qianwen")
	create("mobile-b", "mobile", "doubao")
	if task.ExecutionMode != "pull" || task.Status != biz.TaskStatusSubmitted || pushes.Load() != 1 {
		t.Fatalf("pull submitted to push adapter: %+v", task)
	}
	first := claim("empty", "doubao", 100)
	shard := first["shardId"].(string)
	if first["device"] != "mobile" || first["batchNo"] != "mobile-a" || len(first["items"].([]any)) != 2 {
		t.Fatalf("cross task claim: %v", first)
	}
	if claim("empty", "doubao", 100)["shardId"] != shard {
		t.Fatal("claim replay changed shard")
	}
	decode(postJSON(t, server.URL+"/external/gettask", "pull-token", map[string]any{"requestId": "empty", "device": "mobile", "channel": "doubao", "limit": 1}), 409)
	decode(postJSON(t, server.URL+"/external/taskresult", "pull2-token", map[string]any{"requestId": "wrong", "batchNo": "mobile-a", "shardId": shard, "finished": true}), 404)
	decode(postJSON(t, server.URL+"/external/taskresult", "pull-token", map[string]any{"requestId": "wrong-item", "batchNo": "mobile-a", "shardId": shard, "results": []any{answer("q3", "完整", 1)}}), 400)
	submitted := submit("partial", "mobile-a", shard, true, answer("q1", "第一轮完整回答", 3))
	if count(`select count(*) from cdp_task_item_results where task_id=$1`, task.ID) != 0 {
		t.Fatal("result processed synchronously")
	}
	if submit("partial", "mobile-a", shard, true, answer("q1", "第一轮完整回答", 3))["submissionId"] != submitted["submissionId"] {
		t.Fatal("submission replay changed ID")
	}
	drain()
	progress := decode(getWithAPIKey(t, fmt.Sprintf("%s/api/v1/tasks/%d/progress", server.URL, task.ID), apiKey), 200)["data"].(map[string]any)
	if progress["device"] != "mobile" || progress["completed"] != float64(1) || progress["completedShards"] != float64(0) {
		t.Fatalf("premature completion: %v", progress)
	}
	partialRows, err := repo.ListTaskItemResults(ctx, task.ID)
	if err != nil || len(partialRows) != 1 || partialRows[0].Seq != 3 {
		t.Fatalf("unsubmitted answers must not appear in results: %+v %v", partialRows, err)
	}
	submit("failed", "mobile-a", shard, false, map[string]any{"itemId": "q2", "answerSeq": 1, "executionStatus": "FAILED", "errorCode": "UNAVAILABLE"})
	drain()
	if count(`select count(*) from cdp_tasks where id=$1 and status='SUBMITTED'`, task.ID) != 1 {
		t.Fatal("unclaimed channel completed task")
	}
	second := claim("qianwen", "qianwen", 100)
	submit("remaining", "mobile-a", second["shardId"].(string), true, answer("q3", "第三个问题完整回答", 1))
	drain()
	if err = tasks.DeliverPendingCallbacks(ctx, 20); err != nil {
		t.Fatal(err)
	}
	if callbacks.Load() != 1 || count(`select count(*) from cdp_tasks where id=$1 and status='DONE'`, task.ID) != 1 {
		t.Fatalf("completion callback=%d", callbacks.Load())
	}
	if yangshi {
		if uploads.Load() != 1 {
			t.Fatalf("yangshi uploads=%d", uploads.Load())
		}
		book, err := excelize.OpenReader(bytes.NewReader(uploaded.Load().([]byte)))
		if err != nil {
			t.Fatal(err)
		}
		rows, err := book.GetRows("AI回答")
		book.Close()
		if err != nil || len(rows) != 4 || rows[1][1] != "3" {
			t.Fatalf("yangshi sparse workbook: %v %v", rows, err)
		}
	}
	// Sparse answerSeq is retained; there are exactly three rows, no phantom rounds.
	items, err := repo.ListTaskItemResults(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0].Seq != 3 {
		t.Fatalf("sparse results: %+v", items)
	}
	submit("correction", "mobile-a", shard, false, answer("q1", "修正后的完整回答", 3))
	drain()
	if err = tasks.DeliverPendingCallbacks(ctx, 20); err != nil {
		t.Fatal(err)
	}
	if callbacks.Load() != 1 {
		t.Fatal("DONE correction sent another callback")
	}
	if count(`select count(*) from cdp_task_item_results where task_id=$1`, task.ID) != 3 {
		t.Fatal("correction added rows")
	}
	// Admin uses real Redis authentication and sees actual sparse rows and Pull state.
	auth := adminLogin(t, server.URL, cfg)
	detail := decode(adminGet(t, fmt.Sprintf("%s/api/admin/v1/tasks/%d", server.URL, task.ID), auth), 200)["data"].(map[string]any)
	if detail["itemTotal"] != float64(3) || detail["task"].(map[string]any)["executionMode"] != "pull" {
		t.Fatalf("admin detail mismatch: %v", detail)
	}
	// A leased old request can replay after a newer request without overwriting it.
	older := submit("older", "mobile-a", shard, false, answer("q1", "较旧回答", 3))
	lease, err := pullRepo.Lease(ctx)
	if err != nil || lease == nil {
		t.Fatalf("lease: %v", err)
	}
	newer := submit("newer", "mobile-a", shard, false, answer("q1", "最新回答", 3))
	drain()
	if _, err = db.Exec(ctx, `update cdp_task_result_submissions set lease_until=now()-interval '1 second' where id=$1`, older["submissionId"]); err != nil {
		t.Fatal(err)
	}
	drain()
	if count(`select count(*) from cdp_task_item_results where task_id=$1 and item_id='q1' and seq=3 and source_submission_id=$2`, task.ID, newer["submissionId"]) != 1 {
		t.Fatal("stale replay overwrote newer answer")
	}
	// Unresolved service failure blocks completion; a newer accepted correction supersedes every key.
	failed := submit("service-failed", "mobile-a", shard, true, answer("q1", "服务端待写回答", 3))
	if _, err = db.Exec(ctx, `update cdp_task_result_submissions set status='FAILED',last_error='simulated service interruption' where id=$1`, failed["submissionId"]); err != nil {
		t.Fatal(err)
	}
	submit("repair-failed", "mobile-a", shard, false, answer("q1", "服务端恢复后的回答", 3))
	drain()
	if count(`select count(*) from cdp_task_result_submissions where id=$1 and status='SUPERSEDED'`, failed["submissionId"]) != 1 {
		t.Fatal("failed request not superseded")
	}
	decode(postJSON(t, server.URL+"/external/taskresult", "pull-token", map[string]any{"requestId": "newer", "batchNo": "mobile-a", "shardId": shard, "finished": true}), 409)
	// Partial write failure retains the first row and safely replays after the lease retry.
	if _, err = db.Exec(ctx, `create function pull_test_failure() returns trigger language plpgsql as $$ begin if NEW.seq=7 then raise exception 'temporary local failure'; end if; return NEW; end $$; create trigger pull_test_failure before insert or update on cdp_task_item_results for each row execute function pull_test_failure()`); err != nil {
		t.Fatal(err)
	}
	partial := submit("partial-crash", "mobile-a", shard, false, answer("q1", "保留已写入的轮次", 6), answer("q2", "待恢复写入的轮次", 7))
	if _, err = pull.ProcessOne(ctx); err == nil {
		t.Fatal("injected database failure was not reported")
	}
	if count(`select count(*) from cdp_task_item_results where task_id=$1 and seq=6`, task.ID) != 1 || count(`select count(*) from cdp_task_result_submissions where id=$1 and status='RETRY_WAIT'`, partial["submissionId"]) != 1 {
		t.Fatal("partial writes not retained for retry")
	}
	if _, err = db.Exec(ctx, `drop trigger pull_test_failure on cdp_task_item_results;drop function pull_test_failure()`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `update cdp_task_result_submissions set next_retry_at=now() where id=$1`, partial["submissionId"]); err != nil {
		t.Fatal(err)
	}
	drain()
	successResult := decode(getWithAPIKey(t, server.URL+"/api/v1/external-tasks/"+task.JobID+"/result?successOnly=true", apiKey), 200)["data"].(map[string]any)
	if successResult["successCount"] != float64(3) || len(successResult["items"].([]any)) != 4 {
		t.Fatalf("successOnly must count questions and retain actual rounds: %v", successResult)
	}
	// A second upstream cannot query this task, including through its public job ID.
	if _, err = db.Exec(ctx, `insert into platform_users(platform_code,name,api_key,status,callback_mode,callback_config) values('other','Other','other-key','ACTIVE','DEFAULT_HTTP','{}')`); err != nil {
		t.Fatal(err)
	}
	decode(getWithAPIKey(t, fmt.Sprintf("%s/api/v1/tasks/%d/progress", server.URL, task.ID), "other-key"), 404)
	// A startup replay must not invalidate explicit execution failures in Pull.
	if err = data.Migrate(ctx, db, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	if count(`select count(*) from cdp_tasks where id=$1 and risk_stats_updated_at is not null`, task.ID) != 1 {
		t.Fatal("migration invalidated pull failure validation")
	}
	if count(`select count(*) from cdp_tasks where execution_mode='pull' and provider_external_batch_id is not null`) != 0 {
		t.Fatal("migration fabricated a Pull supplier task ID")
	}
	if count(`select count(*) from cdp_tasks where id=$1 and provider_external_batch_id='push-test'`, pushTask.ID) != 1 {
		t.Fatal("migration changed the real Push supplier task ID")
	}
	if count(`select count(*) from pg_constraint where contype='f' and connamespace='public'::regnamespace`) != 0 {
		t.Fatal("business schema must not contain physical foreign keys")
	}
	// Claim another task concurrently; exactly one new shard can own its single item.
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			result := claim(fmt.Sprintf("concurrent-%d", i), "doubao", 1)
			if len(result["items"].([]any)) > 0 {
				ids <- result["shardId"].(string)
			}
		}(i)
	}
	wg.Wait()
	close(ids)
	if len(ids) != 1 {
		t.Fatalf("duplicate claim count=%d", len(ids))
	}
	var oldShard string
	for id := range ids {
		oldShard = id
	}
	old := submit("before-reassign", "mobile-b", oldShard, true, answer("q1", "旧供应商完整回答", 1))
	_ = old
	result := decode(adminPostJSON(t, server.URL+"/api/admin/v1/tasks/reassign", auth, map[string]any{"batchNo": "mobile-b", "providerCode": "pull2", "reason": "test"}), 200)
	_ = result
	drain()
	if count(`select count(*) from cdp_task_result_submissions where request_id='before-reassign' and status='REJECTED_CLOSED'`) != 1 {
		t.Fatal("closed task submission not rejected")
	}
	decode(postJSON(t, server.URL+"/external/taskresult", "pull-token", map[string]any{"requestId": "late", "batchNo": "mobile-b", "shardId": oldShard, "finished": true}), 400)
	reassigned := decode(postJSON(t, server.URL+"/external/gettask", "pull2-token", map[string]any{"requestId": "target", "device": "mobile", "channel": "doubao", "limit": 100}), 200)["data"].(map[string]any)
	if reassigned["batchNo"] != "mobile-b" || reassigned["shardId"] == oldShard {
		t.Fatalf("reassigned identity: %v", reassigned)
	}
	var targetBatch string
	if err = db.QueryRow(ctx, `select batch_no from cdp_tasks where reassigned_from_task_id=(select id from cdp_tasks where batch_no='mobile-b')`).Scan(&targetBatch); err != nil {
		t.Fatal(err)
	}
	decode(adminPostJSON(t, server.URL+"/api/admin/v1/tasks/reassign", auth, map[string]any{"batchNo": targetBatch, "providerCode": "pull", "reason": "second"}), 400)
	// Explicit execution failures complete and export even when there are no successful answers.
	allFailed := create("all-failed", "mobile", "qianwen")
	f := claim("all-failed", "qianwen", 100)
	submit("all-failed", "all-failed", f["shardId"].(string), false, map[string]any{"itemId": "q1", "answerSeq": 5, "executionStatus": "FAILED", "errorMessage": "channel unavailable"})
	submit("all-failed-finish", "all-failed", f["shardId"].(string), true)
	drain()
	if err = tasks.DeliverPendingCallbacks(ctx, 20); err != nil {
		t.Fatal(err)
	}
	if count(`select count(*) from cdp_tasks where id=$1 and status='DONE' and success_count=0 and failed_count=1`, allFailed.ID) != 1 {
		t.Fatal("all failures did not complete")
	}
	if callbacks.Load() != 2 {
		t.Fatalf("all failures callback count=%d", callbacks.Load())
	}
	if yangshi {
		if uploads.Load() != 2 {
			t.Fatalf("failed yangshi uploads=%d", uploads.Load())
		}
		book, err := excelize.OpenReader(bytes.NewReader(uploaded.Load().([]byte)))
		if err != nil {
			t.Fatal(err)
		}
		rows, err := book.GetRows("AI回答")
		book.Close()
		if err != nil || len(rows) != 2 || rows[1][1] != "5" {
			t.Fatalf("failed yangshi workbook: %v %v", rows, err)
		}
	}
	// Legacy file/status scans exclude Pull even with a limit of one.
	scanned, err := repo.ListSubmittedTasksForProgress(ctx, 1, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range scanned {
		if v.ExecutionMode != "push" {
			t.Fatal("pull leaked into push scan")
		}
	}
	// Device and interaction mode are independent: mobile Push and web Pull both work.
	if _, err = db.Exec(ctx, `update cdp_dispatch_profiles set config='{"providers":[{"code":"mobile_push"}]}'::jsonb`); err != nil {
		t.Fatal(err)
	}
	mobilePush := create("mobile-push", "mobile", "doubao")
	if mobilePush.ExecutionMode != "push" || lastPushDevice.Load() != "mobile" || pushes.Load() != 2 {
		t.Fatal("mobile push payload or routing incorrect")
	}
	if _, err = db.Exec(ctx, `update cdp_dispatch_profiles set config='{"providers":[{"code":"pull"}]}'::jsonb`); err != nil {
		t.Fatal(err)
	}
	webPull := create("web-pull", "web", "doubao")
	webClaim := decode(postJSON(t, server.URL+"/external/gettask", "pull-token", map[string]any{"requestId": "web-pull", "device": "web", "channel": "doubao", "limit": 100}), 200)["data"].(map[string]any)
	if webPull.ExecutionMode != "pull" || webClaim["device"] != "web" || webClaim["batchNo"] != "web-pull" {
		t.Fatal("web pull routing incorrect")
	}

	// Whole-task reassignment can cross interaction modes in either direction.
	decode(adminPostJSON(t, server.URL+"/api/admin/v1/tasks/reassign", auth, map[string]any{"batchNo": "legacy", "providerCode": "pull", "reason": "switch to polling"}), 200)
	movedWeb := decode(postJSON(t, server.URL+"/external/gettask", "pull-token", map[string]any{"requestId": "moved-web", "device": "web", "channel": "doubao", "limit": 100}), 200)["data"].(map[string]any)
	if movedWeb["batchNo"] != "legacy" {
		t.Fatal("push to pull reassignment lost business batch")
	}
	create("pull-to-push", "mobile", "doubao")
	decode(adminPostJSON(t, server.URL+"/api/admin/v1/tasks/reassign", auth, map[string]any{"batchNo": "pull-to-push", "providerCode": "mobile_push", "reason": "switch to push"}), 200)
	if pushes.Load() != 3 || lastPushDevice.Load() != "mobile" {
		t.Fatal("pull to push reassignment did not submit mobile payload")
	}

}
