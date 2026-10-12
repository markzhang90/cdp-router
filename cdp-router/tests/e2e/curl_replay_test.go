package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	"github.com/GitMarkZhang/cdp-router/internal/worker"
	"github.com/xuri/excelize/v2"
)

type curlReplayAnswer struct {
	ItemID          string           `json:"itemId"`
	PlatformCode    string           `json:"platformCode"`
	Question        string           `json:"question"`
	AnswerText      string           `json:"answerText"`
	ThinkingProcess string           `json:"thinkingProcess"`
	References      []map[string]any `json:"references"`
}

// Input is an explicitly downloaded, private fixture. This test never connects
// to production. All gateway operations use the real curl executable; only the
// provider and upstream boundaries are mocked. Persistence and workers are real.
func TestCurlProductionSampleReplay(t *testing.T) {
	path := os.Getenv("CDP_CURL_REPLAY_FIXTURE")
	if path == "" {
		t.Skip("set CDP_CURL_REPLAY_FIXTURE to a locally downloaded question/answer fixture")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Items []curlReplayAnswer `json:"items"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Items) < 4 {
		t.Fatal("requires at least four real question/answer pairs")
	}
	seen := map[string]bool{}
	for _, a := range fixture.Items {
		if a.ItemID == "" || a.Question == "" || len([]rune(a.AnswerText)) < 20 || len(a.References) == 0 || seen[a.ItemID] {
			t.Fatal("fixture must contain unique questions and complete answers with references")
		}
		seen[a.ItemID] = true
	}
	for _, mode := range []string{"default", "yangshi"} {
		t.Run(mode, func(t *testing.T) { runCurlReplay(t, fixture.Items, mode, fmt.Sprintf("%x", sha256.Sum256(raw))) })
	}
}

func runCurlReplay(t *testing.T, samples []curlReplayAnswer, mode, digest string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	artifactRoot := os.Getenv("CDP_CURL_REPLAY_OUTPUT")
	if artifactRoot == "" {
		artifactRoot = t.TempDir()
	}
	artifacts, err := filepath.Abs(filepath.Join(artifactRoot, mode))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(artifacts, 0700); err != nil {
		t.Fatal(err)
	}
	save := func(name string, v any) {
		t.Helper()
		b, e := json.MarshalIndent(v, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(artifacts, name), b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	retainedDatabase := os.Getenv("CDP_CURL_REPLAY_DATABASE")
	objectDir := ""
	if retainedDatabase != "" {
		if artifactRoot == "" || os.Getenv("CDP_CURL_REPLAY_OUTPUT") == "" {
			t.Fatal("retained replay requires CDP_CURL_REPLAY_OUTPUT for durable artifacts")
		}
		objectDir = filepath.Join(artifacts, "objects")
	}
	cfg, err := loadLocalReplayConfig(t, retainedDatabase, objectDir)
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
	if retainedDatabase != "" {
		var exists bool
		if err = db.QueryRow(ctx, `select to_regclass('public.cdp_tasks') is not null`).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			var active int
			if err = db.QueryRow(ctx, `select count(*) from cdp_tasks where status not in ('DONE','FAILED','REASSIGNED','CANCELED')`).Scan(&active); err != nil {
				t.Fatal(err)
			}
			if active != 0 {
				t.Fatal("dev contains unfinished tasks; refusing to let replay workers process existing work")
			}
		}
	}
	if err = data.Migrate(ctx, db, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	redis, cleanup := openRealRedisForTest(t, ctx, cfg)
	defer cleanup()
	defer redis.Close()
	var mu sync.Mutex
	pushes := []map[string]any{}
	notifications := [][]byte{}
	uploads := [][]byte{}
	var upstreamURL string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/upload-url") {
			json.NewEncoder(w).Encode(map[string]any{"status": 0, "data": map[string]any{"uploadUrl": upstreamURL + "/upload", "method": "PUT", "bucket": "local", "key": "result.xlsx"}})
			return
		}
		body, e := io.ReadAll(r.Body)
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/upload" {
			uploads = append(uploads, body)
		} else {
			notifications = append(notifications, body)
		}
		io.WriteString(w, `{"status":0,"accepted":true}`)
	}))
	defer upstream.Close()
	upstreamURL = upstream.URL
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/submit" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Provider-Token") != "local-push-token" {
			http.Error(w, "bad token", 401)
			return
		}
		var body map[string]any
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		mu.Lock()
		pushes = append(pushes, body)
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"externalBatchId": "local-push-job", "acceptedCount": len(samples)}})
	}))
	defer downstream.Close()
	channels := []string{}
	channelSet := map[string]bool{}
	byID := map[string]curlReplayAnswer{}
	for _, a := range samples {
		byID[a.ItemID] = a
		if !channelSet[a.PlatformCode] {
			channels = append(channels, a.PlatformCode)
			channelSet[a.PlatformCode] = true
		}
	}
	sort.Strings(channels)
	cfg.Provider = map[string]config.ProviderConfig{
		"legacy": {Code: "legacy", Token: "local-push-token", SubmitURL: downstream.URL + "/submit", Enabled: true, QPSLimit: 100, ConcurrencyLimit: 10, ResultMode: "callback_upload"},
		"mobile": {Code: "mobile", Token: "local-pull-token", InteractionMode: "pull", SupportedDevices: []string{"mobile"}, DeviceChannels: map[string][]string{"mobile": channels}, Enabled: true, QPSLimit: 100, ConcurrencyLimit: 10},
	}
	runID := fmt.Sprintf("curl_%s_%d", mode, time.Now().UnixNano())
	legacyBatch, mobileBatch := runID+"_legacy", runID+"_mobile"
	user := cfg.PlatformUsers["mainland"]
	user.PlatformCode, user.Name, user.APIKey = runID, "Curl replay "+mode, "local-"+runID
	user.DefaultProviderCode = "legacy"
	user.CallbackURL = upstream.URL + "/notify"
	if mode == "yangshi" {
		user.CallbackMode = "YANGSHI"
		b, _ := json.Marshal(map[string]string{"baseURL": upstream.URL, "providerCode": "local", "providerToken": "local"})
		user.CallbackConfig = string(b)
	}
	cfg.PlatformUsers = map[string]config.PlatformUserConfig{user.PlatformCode: user}
	users := syncPlatformUsers(t, ctx, db, cfg)
	if _, err = db.Exec(ctx, `update cdp_dispatch_profiles set config='{"providers":[{"code":"legacy"},{"code":"mobile"}]}'::jsonb where platform_user_id=(select id from platform_users where platform_code=$1)`, user.PlatformCode); err != nil {
		t.Fatal(err)
	}
	repo := data.NewTaskRepo(db)
	registry := provider.NewRegistry(cfg.Provider)
	rules := biz.NewValidationRuleCache(repo, time.Second)
	if err = rules.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	store, err := ossutil.New(cfg.OSS)
	if err != nil {
		t.Fatal(err)
	}
	listenerServer := httptest.NewUnstartedServer(nil)
	cfg.Server.PublicBaseURL = "http://" + listenerServer.Listener.Addr().String()
	tasks := biz.NewTaskUsecase(repo, registry, registry, store, cleaner.NewExcelParser(), callback.NewClient(time.Second, time.Second, cfg.PlatformUsers, store), biz.TaskUsecaseOptions{PublicBaseURL: cfg.Server.PublicBaseURL, ValidationRules: rules, CallbackMaxTry: 3, OSSExpire: time.Hour})
	pull := biz.NewPullUsecase(data.NewPullRepo(db), rules)
	ts := service.NewTaskService(tasks, registry, users, cfg.OSS.Bucket)
	admin := service.NewAdminService(cfg.Admin, redis, data.NewAdminRepo(db), tasks, cfg.Provider)
	listenerServer.Config.Handler = httpapi.New(ts, admin, service.NewPullService(pull, ts, cfg.Provider)).Handler()
	listenerServer.Start()
	defer listenerServer.Close()
	client := newCurlReplayClient(t, ctx, artifacts)
	request := func(name, method, path, token string, body any, want int) map[string]any {
		t.Helper()
		if (path == "/external/gettask" || path == "/external/taskresult") && body != nil {
			original := body.(map[string]any)
			copied := make(map[string]any, len(original))
			for k, v := range original {
				copied[k] = v
			}
			if id, ok := copied["requestId"].(string); ok {
				copied["requestId"] = runID + "_" + id
			}
			body = copied
		}
		return client.json(name, method, listenerServer.URL+path, token, body, want)
	}
	wait := func(fn func() bool) {
		t.Helper()
		for !fn() {
			select {
			case <-ctx.Done():
				t.Fatal("timed out waiting for asynchronous state")
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
	taskFor := func(batch string) *biz.Task {
		t.Helper()
		v, e := repo.GetTaskByBatchNo(ctx, batch)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	questionItems := []map[string]any{}
	for _, a := range samples {
		questionItems = append(questionItems, map[string]any{"itemId": a.ItemID, "platformCode": a.PlatformCode, "question": a.Question, "seq": 1})
	}
	create := func(batch, device string) map[string]any {
		t.Helper()
		body := map[string]any{"batchNo": batch, "providerCode": "ignored-request-provider", "bizDate": time.Now().Format("2006-01-02"), "callbackUrl": upstream.URL + "/notify", "items": questionItems}
		if device != "" {
			body["device"] = device
		}
		out := request("create-"+batch, "POST", "/api/v1/tasks", user.APIKey, body, 200)
		repeated := request("recreate-"+batch, "POST", "/api/v1/tasks", user.APIKey, body, 200)
		if repeated["externalBatchId"] != out["externalBatchId"] {
			t.Fatal("creation replay changed job ID")
		}
		return out
	}
	claimBody := map[string]any{"requestId": "empty-before-create", "device": "mobile", "channel": channels[0], "limit": 2}
	empty := request("empty-poll", "POST", "/external/gettask", "local-pull-token", claimBody, 200)
	if len(empty["items"].([]any)) != 0 {
		t.Fatal("empty poll returned work")
	}
	request("bad-token", "POST", "/external/gettask", "invalid-token", claimBody, 401)
	legacyJob := create(legacyBatch, "")
	legacy := taskFor(legacyBatch)
	if legacy.Device != "web" || legacy.ExecutionMode != "push" || legacy.ProviderCode != "legacy" {
		t.Fatalf("legacy routing: %+v", legacy)
	}
	mobileJob := create(mobileBatch, "mobile")
	mobile := taskFor(mobileBatch)
	if mobile.Device != "mobile" || mobile.ExecutionMode != "pull" || mobile.ProviderCode != "mobile" {
		t.Fatalf("mobile routing: %+v", mobile)
	}
	mu.Lock()
	pushSnapshot := append([]map[string]any(nil), pushes...)
	mu.Unlock()
	if len(pushSnapshot) != 1 || pushSnapshot[0]["device"] != nil {
		t.Fatal("legacy payload changed or Pull leaked into Push")
	}
	save("downstream-push.json", pushSnapshot)
	sent := pushSnapshot[0]["items"].([]any)
	if len(sent) != len(samples) {
		t.Fatal("legacy questions missing")
	}
	for i, v := range sent {
		q := v.(map[string]any)
		if q["platformCode"] != samples[i].PlatformCode || q["question"] != samples[i].Question {
			t.Fatal("legacy question/channel altered")
		}
	}
	// Start the actual workers, rather than driving usecases directly in the test.
	workerCtx, stopWorkers := context.WithCancel(ctx)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() { defer workers.Done(); worker.New(tasks).Run(workerCtx) }()
	go func() { defer workers.Done(); worker.NewPull(pull, 2).Run(workerCtx) }()
	defer func() { stopWorkers(); workers.Wait() }()
	// Old suppliers still request a signed upload URL, PUT Excel, then notify.
	workbook := curlReplayWorkbook(t, samples)
	excelPath := filepath.Join(artifacts, "legacy-results.xlsx")
	if err = os.WriteFile(excelPath, workbook, 0600); err != nil {
		t.Fatal(err)
	}
	upload := request("push-upload-url", "POST", "/external/oss/legacy/upload-url", "local-push-token", map[string]any{"batchNo": legacy.BatchNo, "fileName": "legacy-results.xlsx"}, 200)
	client.raw("push-upload", "PUT", upload["uploadUrl"].(string), "", excelPath, 200)
	callbackBody := map[string]any{"batchNo": legacy.BatchNo, "externalBatchId": "local-push-job", "status": "DONE", "totalCount": len(samples), "successCount": len(samples), "failedCount": 0}
	request("push-complete", "POST", "/external/callback/legacy", "local-push-token", callbackBody, 200)
	request("push-complete-replay", "POST", "/external/callback/legacy", "local-push-token", callbackBody, 200)
	answer := func(a curlReplayAnswer) map[string]any {
		return map[string]any{"itemId": a.ItemID, "answerSeq": 1, "executionStatus": "SUCCESS", "answerText": a.AnswerText, "thinkingProcess": a.ThinkingProcess, "references": a.References}
	}
	shardCount := 0
	claimed := map[string]bool{}
	firstShard := ""
	firstAnswer := curlReplayAnswer{}
	for _, channel := range channels {
		for batch := 0; ; batch++ {
			id := fmt.Sprintf("claim-%s-%d", channel, batch)
			body := map[string]any{"requestId": id, "device": "mobile", "channel": channel, "limit": 2}
			shard := request(id, "POST", "/external/gettask", "local-pull-token", body, 200)
			items := shard["items"].([]any)
			if len(items) == 0 {
				break
			}
			shardCount++
			if shard["batchNo"] != mobile.BatchNo || shard["device"] != "mobile" || shard["channel"] != channel {
				t.Fatal("claim crossed task/device/channel")
			}
			repeated := request(id+"-replay", "POST", "/external/gettask", "local-pull-token", body, 200)
			if repeated["shardId"] != shard["shardId"] {
				t.Fatal("claim replay changed shard")
			}
			results := []map[string]any{}
			for _, v := range items {
				item := v.(map[string]any)
				key := item["itemId"].(string)
				if claimed[key] {
					t.Fatal("question claimed twice")
				}
				claimed[key] = true
				a := byID[key]
				if item["question"] != a.Question || item["platformCode"] != a.PlatformCode {
					t.Fatal("claimed source data altered")
				}
				results = append(results, answer(a))
			}
			if firstShard == "" {
				firstShard = shard["shardId"].(string)
				firstAnswer = byID[items[0].(map[string]any)["itemId"].(string)]
				// Record one answer before the shard is finished. Progress must derive from persisted rows.
				partial := map[string]any{"requestId": "partial", "batchNo": mobile.BatchNo, "shardId": firstShard, "finished": false, "results": results[:1]}
				request("partial-result", "POST", "/external/taskresult", "local-pull-token", partial, 202)
				wait(func() bool {
					p := request("partial-progress", "GET", fmt.Sprintf("/api/v1/tasks/%d/progress", mobile.ID), user.APIKey, nil, 200)
					return p["completed"] == float64(1) && p["completedShards"] == float64(0)
				})
				if taskFor(mobile.BatchNo).Status != biz.TaskStatusSubmitted {
					t.Fatal("partial result completed task")
				}
			}
			submission := map[string]any{"requestId": id + "-result", "batchNo": mobile.BatchNo, "shardId": shard["shardId"], "finished": true, "results": results}
			accepted := request(id+"-result", "POST", "/external/taskresult", "local-pull-token", submission, 202)
			duplicate := request(id+"-result-replay", "POST", "/external/taskresult", "local-pull-token", submission, 202)
			if accepted["submissionId"] != duplicate["submissionId"] {
				t.Fatal("result replay duplicated submission")
			}
		}
	}
	if len(claimed) != len(samples) {
		t.Fatal("not all source questions claimed")
	}
	wait(func() bool {
		return taskFor(legacy.BatchNo).Status == biz.TaskStatusDone && taskFor(mobile.BatchNo).Status == biz.TaskStatusDone
	})
	for _, entry := range []struct {
		task *biz.Task
		job  map[string]any
	}{{legacy, legacyJob}, {mobile, mobileJob}} {
		result := request("final-result-"+entry.task.BatchNo, "GET", "/api/v1/external-tasks/"+entry.job["externalBatchId"].(string)+"/result", user.APIKey, nil, 200)
		rows := result["items"].([]any)
		if len(rows) != len(samples) {
			t.Fatalf("result count %d want %d", len(rows), len(samples))
		}
		for _, v := range rows {
			r := v.(map[string]any)
			expected := byID[r["itemId"].(string)]
			if r["answerText"] != expected.AnswerText || r["question"] != expected.Question {
				t.Fatal("HTTP result differs from production sample")
			}
		}
		dbRows, e := repo.ListTaskItemResults(ctx, entry.task.ID)
		if e != nil {
			t.Fatal(e)
		}
		if len(dbRows) != len(samples) {
			t.Fatal("database row count mismatch")
		}
		for _, r := range dbRows {
			if r.AnswerText != byID[r.ItemID].AnswerText {
				t.Fatal("persisted answer differs from source")
			}
		}
	}
	finalProgress := request("final-progress", "GET", "/api/v1/external-tasks/"+mobileJob["externalBatchId"].(string)+"/progress", user.APIKey, nil, 200)
	if finalProgress["completed"] != float64(len(samples)) || finalProgress["completedShards"] != float64(shardCount) || finalProgress["pendingSubmissions"] != float64(0) {
		t.Fatalf("bad final progress: %v", finalProgress)
	}
	// A new submission can overwrite one answer after DONE without redelivering.
	corrected := answer(firstAnswer)
	corrected["answerText"] = firstAnswer.AnswerText + "\n本地覆盖验证。"
	correction := request("single-answer-correction", "POST", "/external/taskresult", "local-pull-token", map[string]any{"requestId": "correction", "batchNo": mobile.BatchNo, "shardId": firstShard, "finished": true, "results": []any{corrected}}, 202)
	wait(func() bool {
		var status string
		if e := db.QueryRow(ctx, `select status from cdp_task_result_submissions where id=$1`, correction["submissionId"]).Scan(&status); e != nil {
			t.Fatal(e)
		}
		return status == "APPLIED"
	})
	var rowCount, correctCount, pullFiles int
	if err = db.QueryRow(ctx, `select count(*),count(*) filter(where item_id=$2 and answer_text=$3) from cdp_task_item_results where task_id=$1`, mobile.ID, firstAnswer.ItemID, corrected["answerText"]).Scan(&rowCount, &correctCount); err != nil {
		t.Fatal(err)
	}
	if rowCount != len(samples) || correctCount != 1 {
		t.Fatal("single answer overwrite duplicated or missed row")
	}
	if err = db.QueryRow(ctx, `select count(*) from cdp_task_result_files where task_id=$1`, mobile.ID).Scan(&pullFiles); err != nil {
		t.Fatal(err)
	}
	if pullFiles != 0 {
		t.Fatal("Pull unexpectedly used result files")
	}
	// Observe another callback worker tick after correction.
	select {
	case <-time.After(6 * time.Second):
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	mu.Lock()
	callbackBodies := append([][]byte(nil), notifications...)
	uploadBodies := append([][]byte(nil), uploads...)
	pushCount := len(pushes)
	mu.Unlock()
	if len(callbackBodies) != 2 || pushCount != 1 {
		t.Fatalf("callbacks=%d pushes=%d", len(callbackBodies), pushCount)
	}
	for i, b := range callbackBodies {
		if err = os.WriteFile(filepath.Join(artifacts, fmt.Sprintf("callback-%d.json", i+1)), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if mode == "yangshi" {
		if len(uploadBodies) != 2 {
			t.Fatalf("Yangshi upload count=%d", len(uploadBodies))
		}
		for i, b := range uploadBodies {
			if err = os.WriteFile(filepath.Join(artifacts, fmt.Sprintf("yangshi-export-%d.xlsx", i+1)), b, 0600); err != nil {
				t.Fatal(err)
			}
			book, e := excelize.OpenReader(bytes.NewReader(b))
			if e != nil {
				t.Fatal(e)
			}
			rows, e := book.GetRows("AI回答")
			book.Close()
			if e != nil || len(rows) != len(samples)+1 {
				t.Fatal("Yangshi export lost answers")
			}
		}
	} else if len(uploadBodies) != 0 {
		t.Fatal("default callback unexpectedly uploaded workbook")
	}
	if taskFor(mobile.BatchNo).Status != biz.TaskStatusDone {
		t.Fatal("correction reopened completed task")
	}
	save("summary.json", map[string]any{"database": cfg.Postgres.Database, "retained": retainedDatabase != "", "platformCode": user.PlatformCode, "legacyBatchNo": legacy.BatchNo, "pullBatchNo": mobile.BatchNo, "fixtureSHA256": digest, "samples": len(samples), "channels": channels, "legacyTaskId": legacy.ID, "pullTaskId": mobile.ID, "legacyState": taskFor(legacy.BatchNo).Status, "pullState": taskFor(mobile.BatchNo).Status, "shards": shardCount, "pushDeliveries": pushCount, "callbacks": len(callbackBodies), "yangshiUploads": len(uploadBodies), "pullResultFiles": pullFiles, "correctedRows": correctCount, "httpRequests": client.count, "result": "PASS"})
	t.Logf("curl replay passed: mode=%s samples=%d shards=%d callbacks=%d artifacts=%s", mode, len(samples), shardCount, len(callbackBodies), artifacts)
}

type curlReplayClient struct {
	t     *testing.T
	ctx   context.Context
	dir   string
	count int
}

func newCurlReplayClient(t *testing.T, ctx context.Context, dir string) *curlReplayClient {
	t.Helper()
	if _, e := exec.LookPath("curl"); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "requests.sh"), []byte("# Recorded curl requests against temporary local endpoints.\n"), 0600); e != nil {
		t.Fatal(e)
	}
	return &curlReplayClient{t: t, ctx: ctx, dir: dir}
}
func (c *curlReplayClient) json(name, method, target, token string, body any, want int) map[string]any {
	c.t.Helper()
	input := ""
	if body != nil {
		b, e := json.MarshalIndent(body, "", "  ")
		if e != nil {
			c.t.Fatal(e)
		}
		input = filepath.Join(c.dir, fmt.Sprintf("%03d-%s.request.json", c.count+1, name))
		if e = os.WriteFile(input, b, 0600); e != nil {
			c.t.Fatal(e)
		}
	}
	b := c.raw(name, method, target, token, input, want)
	if len(b) == 0 && want >= 400 {
		return nil
	}
	var envelope map[string]any
	if e := json.Unmarshal(b, &envelope); e != nil {
		c.t.Fatal(e)
	}
	if data, ok := envelope["data"].(map[string]any); ok {
		return data
	}
	return envelope
}
func (c *curlReplayClient) raw(name, method, target, token, input string, want int) []byte {
	c.t.Helper()
	u, e := url.Parse(target)
	if e != nil || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") {
		c.t.Fatal("curl replay requires loopback HTTP")
	}
	c.count++
	output := filepath.Join(c.dir, fmt.Sprintf("%03d-%s.response", c.count, name))
	args := []string{"--silent", "--show-error", "--noproxy", "*", "--proto", "=http", "--max-time", "20", "--request", method, "--output", output, "--write-out", "%{http_code}"}
	if token != "" {
		args = append(args, "--header", "X-Provider-Token: "+token)
	}
	if input != "" {
		args = append(args, "--data-binary", "@"+input)
		if strings.HasSuffix(input, ".json") {
			args = append(args, "--header", "Content-Type: application/json")
		}
	}
	args = append(args, target)
	command := []string{"curl"}
	for _, a := range args {
		command = append(command, "'"+strings.ReplaceAll(a, "'", "'\"'\"'")+"'")
	}
	transcript, e := os.OpenFile(filepath.Join(c.dir, "requests.sh"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		c.t.Fatal(e)
	}
	fmt.Fprintln(transcript, strings.Join(command, " "))
	transcript.Close()
	status, e := exec.CommandContext(c.ctx, "curl", args...).CombinedOutput()
	if e != nil {
		c.t.Fatalf("curl %s: %v %s", name, e, status)
	}
	b, e := os.ReadFile(output)
	if e != nil {
		c.t.Fatal(e)
	}
	if strings.TrimSpace(string(status)) != strconv.Itoa(want) {
		c.t.Fatalf("curl %s HTTP %s want %d: %s", name, status, want, b)
	}
	return b
}
func curlReplayWorkbook(t *testing.T, samples []curlReplayAnswer) []byte {
	t.Helper()
	book := excelize.NewFile()
	defer book.Close()
	if e := book.SetSheetName("Sheet1", "AI回答"); e != nil {
		t.Fatal(e)
	}
	if _, e := book.NewSheet("溯源信息"); e != nil {
		t.Fatal(e)
	}
	put := func(sheet string, row int, v []any) {
		t.Helper()
		if e := book.SetSheetRow(sheet, fmt.Sprintf("A%d", row), &v); e != nil {
			t.Fatal(e)
		}
	}
	put("AI回答", 1, []any{"itemId", "问题", "平台", "深度思考过程", "AI回答"})
	put("溯源信息", 1, []any{"itemId", "url", "标题", "内容", "发布日期", "网站名称", "网站权威性等级", "网站分类", "作者", "作者分类", "作者身份类型"})
	refRow := 2
	for i, a := range samples {
		put("AI回答", i+2, []any{a.ItemID, a.Question, a.PlatformCode, a.ThinkingProcess, a.AnswerText})
		for _, r := range a.References {
			put("溯源信息", refRow, []any{a.ItemID, r["url"], r["title"], r["content"], r["publishedAtText"], r["siteName"], r["authorityLevel"], r["siteCategory"], r["author"], r["authorCategory"], r["authorIdentityType"]})
			refRow++
		}
	}
	b, e := book.WriteToBuffer()
	if e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
