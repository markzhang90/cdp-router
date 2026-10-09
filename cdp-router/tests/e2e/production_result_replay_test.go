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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/GitMarkZhang/cdp-router/internal/callback"
	"github.com/GitMarkZhang/cdp-router/internal/cleaner"
	"github.com/GitMarkZhang/cdp-router/internal/config"
	"github.com/GitMarkZhang/cdp-router/internal/data"
	"github.com/GitMarkZhang/cdp-router/internal/httpapi"
	"github.com/GitMarkZhang/cdp-router/internal/pkg/ossutil"
	"github.com/GitMarkZhang/cdp-router/internal/provider"
	"github.com/GitMarkZhang/cdp-router/internal/service"
	"github.com/xuri/excelize/v2"
)

// Replay explicitly downloaded production evidence without contacting production.
// Fixtures (including answer text) stay outside git. All network boundaries used
// by this test are local; PostgreSQL and Redis are real and isolated per run.
func TestProductionResultReplayWithRealPersistence(t *testing.T) {
	dir := os.Getenv("CDP_RESULT_REPLAY_DIR")
	if dir == "" {
		t.Skip("set CDP_RESULT_REPLAY_DIR to a locally downloaded fixture directory")
	}
	var fixture struct {
		SourceTaskID int `json:"sourceTaskId"`
		Items        []struct {
			ItemID         string `json:"itemId"`
			PlatformCode   string `json:"platformCode"`
			Seq            int    `json:"seq"`
			Question       string `json:"question"`
			AnswerText     string `json:"answerText"`
			ReferenceCount int    `json:"referenceCount"`
		} `json:"items"`
		Files []struct {
			LocalFile string `json:"localFile"`
			SHA256    string `json:"sha256"`
		} `json:"files"`
		ExpectedBaseline struct {
			AnswerCount  int            `json:"answerCount"`
			RiskCount    int            `json:"riskCount"`
			InvalidCount int            `json:"invalidCount"`
			RiskCodes    map[string]int `json:"riskCodes"`
		} `json:"expectedBaseline"`
	}
	read := func(path string) []byte {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if err := json.Unmarshal(read(filepath.Join(dir, "fixture.json")), &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Files) != 1 || len(fixture.Items) == 0 || fixture.ExpectedBaseline.RiskCount == 0 {
		t.Fatal("replay requires one workbook and known risky answers")
	}
	if filepath.Base(fixture.Files[0].LocalFile) != fixture.Files[0].LocalFile {
		t.Fatal("fixture file must be a basename")
	}
	original := read(filepath.Join(dir, fixture.Files[0].LocalFile))
	if fmt.Sprintf("%x", sha256.Sum256(original)) != fixture.Files[0].SHA256 {
		t.Fatal("original workbook checksum mismatch")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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
	redisClient, cleanup := openRealRedisForTest(t, ctx, cfg)
	defer cleanup()
	defer redisClient.Close()

	var callbacks, uploadRequests, uploads atomic.Int32
	var failCallback atomic.Bool
	var callbackBody, uploadedBody atomic.Value
	var upstreamURL string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/upload-url"):
			uploadRequests.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 0, "data": map[string]any{"uploadUrl": upstreamURL + "/upload", "method": "PUT", "bucket": "local", "key": "replay.xlsx"}})
		case r.URL.Path == "/upload":
			uploads.Add(1)
			body, _ := io.ReadAll(r.Body)
			uploadedBody.Store(body)
		case r.URL.Path == "/notify":
			callbacks.Add(1)
			body, _ := io.ReadAll(r.Body)
			callbackBody.Store(body)
			if failCallback.Load() {
				http.Error(w, "injected local failure", 500)
				return
			}
			_, _ = io.WriteString(w, `{"status":0,"data":{"accepted":true}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	upstreamURL = upstream.URL
	var mu sync.Mutex
	files := map[string][]byte{"/original.xlsx": original}
	urls := []string{"/original.xlsx"}
	downloads := map[string]int{}
	questions := make([]map[string]any, 0, len(fixture.Items))
	items := make([]map[string]any, 0, len(fixture.Items))
	for _, item := range fixture.Items {
		questions = append(questions, map[string]any{"questionId": item.ItemID, "status": 2})
		items = append(items, map[string]any{"itemId": item.ItemID, "platformCode": item.PlatformCode, "seq": item.Seq, "question": item.Question})
	}
	var providerURL string
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/submit":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"externalBatchId": "replay-local", "acceptedCount": len(items)}})
		case strings.HasPrefix(r.URL.Path, "/status/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "success": true, "data": map[string]any{"externalBatchId": "replay-local", "totalCount": len(items), "successCount": len(items), "failedCount": 0, "questions": questions}})
		case strings.HasPrefix(r.URL.Path, "/files/"):
			mu.Lock()
			out := make([]string, 0, len(urls))
			for _, u := range urls {
				out = append(out, providerURL+u)
			}
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "success": true, "data": map[string]any{"externalBatchId": "replay-local", "fileUrls": out}})
		default:
			mu.Lock()
			body, ok := files[r.URL.Path]
			if ok {
				downloads[r.URL.Path]++
			}
			mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(body)
		}
	}))
	defer downstream.Close()
	providerURL = downstream.URL
	p := cfg.Provider["liuandata"]
	p.SubmitURL, p.StatusURL, p.FilesURL = providerURL+"/submit", providerURL+"/status/{externalBatchId}", providerURL+"/files/{externalBatchId}"
	cfg.Provider = map[string]config.ProviderConfig{"liuandata": p}
	user := cfg.PlatformUsers["mainland"]
	user.PlatformCode, user.DefaultProviderCode, user.CallbackMode = "yangshi", "liuandata", "YANGSHI"
	cbConfig, _ := json.Marshal(map[string]string{"baseURL": upstream.URL, "providerCode": "local", "providerToken": "local"})
	user.CallbackConfig = string(cbConfig)
	cfg.PlatformUsers = map[string]config.PlatformUserConfig{"yangshi": user}
	repo := data.NewTaskRepo(db)
	registry := provider.NewRegistry(cfg.Provider)
	platformUsers := syncPlatformUsers(t, ctx, db, cfg)
	store, err := ossutil.New(cfg.OSS)
	if err != nil {
		t.Fatal(err)
	}
	cache := biz.NewValidationRuleCache(repo, 20*time.Millisecond)
	if err = cache.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	cacheCtx, stopCache := context.WithCancel(ctx)
	cacheDone := make(chan struct{})
	go func() { defer close(cacheDone); cache.Run(cacheCtx) }()
	defer func() { stopCache(); <-cacheDone }()
	usecase := biz.NewTaskUsecase(repo, registry, registry, store, cleaner.NewExcelParser(), callback.NewClient(5*time.Second, 5*time.Second, cfg.PlatformUsers, store), biz.TaskUsecaseOptions{PublicBaseURL: cfg.Server.PublicBaseURL, ValidationRules: cache, CallbackMaxTry: 3})
	admin := service.NewAdminService(cfg.Admin, redisClient, data.NewAdminRepo(db), usecase, cfg.Provider)
	server := httptest.NewServer(httpapi.New(service.NewTaskService(usecase, registry, platformUsers, cfg.OSS.Bucket), admin).Handler())
	defer server.Close()
	auth := adminLogin(t, server.URL, cfg)
	decode := func(resp *http.Response) map[string]any {
		t.Helper()
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("HTTP %d: %s", resp.StatusCode, b)
		}
		var body map[string]any
		if err := json.Unmarshal(b, &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	batch := fmt.Sprintf("LOCAL-REPLAY-%d-%d", fixture.SourceTaskID, time.Now().UnixNano())
	decode(postJSONWithAPIKey(t, server.URL+"/api/v1/tasks", user.APIKey, map[string]any{"batchNo": batch, "bizDate": time.Now().Format("2006-01-02"), "callbackUrl": upstream.URL + "/notify", "items": items}))
	task, err := repo.GetTaskByBatchNo(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	taskURL := fmt.Sprintf("%s/api/admin/v1/tasks/%d", server.URL, task.ID)
	syncFiles := func() {
		t.Helper()
		decode(adminPostJSON(t, taskURL+"/check-result", auth, map[string]any{"force": true, "notifyCallback": true}))
	}
	detail := func() map[string]any {
		t.Helper()
		return decode(adminGet(t, taskURL+"?itemPageSize=100&filePageSize=100", auth))["data"].(map[string]any)
	}
	writeEvidence := func(name string, v any) {
		t.Helper()
		b, e := json.MarshalIndent(v, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(dir, name), b, 0600); e != nil {
			t.Fatal(e)
		}
	}

	syncFiles()
	baseline := detail()
	writeEvidence("local-baseline.json", baseline)
	task, err = repo.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected := fixture.ExpectedBaseline
	if task.Status != biz.TaskStatusResultImported || task.RiskStatsUpdatedAt == nil || task.RiskCount != expected.RiskCount || task.InvalidAnswerCount != expected.InvalidCount {
		t.Fatalf("baseline: status=%s risk=%d invalid=%d", task.Status, task.RiskCount, task.InvalidAnswerCount)
	}
	var answerCount, unknownCount int
	if err = db.QueryRow(ctx, `select count(*),count(*) filter (where validation_status='UNKNOWN' or validated_at is null) from cdp_task_item_results where task_id=$1`, task.ID).Scan(&answerCount, &unknownCount); err != nil {
		t.Fatal(err)
	}
	if answerCount != expected.AnswerCount || unknownCount != 0 {
		t.Fatalf("answers=%d unknown=%d", answerCount, unknownCount)
	}
	var summary map[string]int
	if err = json.Unmarshal([]byte(baseline["task"].(map[string]any)["riskSummaryJson"].(string)), &summary); err != nil {
		t.Fatal(err)
	}
	if len(summary) != len(expected.RiskCodes) {
		t.Fatal("risk summary has unexpected categories")
	}
	for code, count := range expected.RiskCodes {
		if summary[code] != count {
			t.Fatalf("risk %s=%d want %d", code, summary[code], count)
		}
	}
	byID := map[string]map[string]any{}
	for _, raw := range baseline["items"].([]any) {
		item := raw.(map[string]any)
		byID[fmt.Sprintf("%s/%v", item["itemId"], item["seq"])] = item
	}
	for _, source := range fixture.Items {
		item := byID[fmt.Sprintf("%s/%d", source.ItemID, source.Seq)]
		if item == nil || item["answerText"] != source.AnswerText || int(item["referenceCount"].(float64)) != source.ReferenceCount {
			t.Fatalf("source answer/reference mismatch for %s", source.ItemID)
		}
		noReferenceRisk := false
		for _, raw := range item["riskItems"].([]any) {
			if raw.(map[string]any)["code"] == biz.RiskAnswerNoReference {
				noReferenceRisk = true
			}
		}
		if noReferenceRisk != (source.ReferenceCount <= 0) {
			t.Fatalf("reference count risk mismatch for %s: count=%d risk=%v", source.ItemID, source.ReferenceCount, noReferenceRisk)
		}
		if item["validationStatus"] == "RISK" || item["validationStatus"] == "INVALID" {
			if len(item["riskItems"].([]any)) == 0 {
				t.Fatal("risky detail lacks risk tags")
			}
		}
	}
	list := decode(adminGet(t, server.URL+"/api/admin/v1/tasks?hasRisk=true", auth))["data"].(map[string]any)
	if list["total"] != float64(1) {
		t.Fatal("risk filter did not return replay task")
	}
	retry := func(body map[string]any) bool {
		t.Helper()
		return decode(adminPostJSON(t, taskURL+"/retry-callback", auth, body))["data"].(map[string]any)["delivered"].(bool)
	}
	if err = usecase.DeliverPendingCallbacks(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if retry(map[string]any{}) || retry(map[string]any{"force": true}) || callbacks.Load() != 0 || uploadRequests.Load() != 0 {
		t.Fatal("risky original bypassed automatic callback gate")
	}
	beforeStats := *task.RiskStatsUpdatedAt
	syncFiles()
	task, err = repo.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	originalDownloads := downloads["/original.xlsx"]
	mu.Unlock()
	if originalDownloads != 1 || task.RiskCount != expected.RiskCount || !task.RiskStatsUpdatedAt.Equal(beforeStats) {
		t.Fatal("duplicate import downloaded or rewrote statistics")
	}
	t.Logf("source task %d: %d answers unchanged; risk=%d invalid=%d; automatic callbacks/uploads=0; duplicate imports skipped", fixture.SourceTaskID, answerCount, task.RiskCount, task.InvalidAnswerCount)

	// Select a literal CJK keyword from authentic text, without altering the file.
	var keyword string
	for _, source := range fixture.Items {
		var run []rune
		for _, ch := range source.AnswerText {
			if unicode.Is(unicode.Han, ch) {
				run = append(run, ch)
			} else {
				run = nil
			}
			if len(run) == 4 {
				keyword = string(run)
				break
			}
		}
		if keyword != "" {
			break
		}
	}
	if keyword == "" {
		t.Fatal("fixture has no four-character CJK keyword")
	}
	wantKeywordHits := 0
	for _, source := range fixture.Items {
		if strings.Contains(source.AnswerText, keyword) {
			wantKeywordHits++
		}
	}
	rulesURL := server.URL + "/api/admin/v1/result-validation-rules"
	rules := decode(adminGet(t, rulesURL, auth))["data"].(map[string]any)
	saved := decode(adminPutJSON(t, rulesURL, auth, map[string]any{"expectedVersion": rules["version"], "keywords": []string{keyword}}))["data"].(map[string]any)
	// Run, rather than explicit Refresh, must pick up the saved configuration.
	time.Sleep(250 * time.Millisecond)
	mu.Lock()
	files["/new-version.xlsx"] = original
	urls = append(urls, "/new-version.xlsx")
	mu.Unlock()
	syncFiles()
	var keywordHits, versions int
	if err = db.QueryRow(ctx, `select count(*) filter (where risk_items @> '[{"code":"ANSWER_KEYWORD_MATCH"}]'), count(*) filter (where validation_rule_version=$2) from cdp_task_item_results where task_id=$1`, task.ID, saved["version"]).Scan(&keywordHits, &versions); err != nil {
		t.Fatal(err)
	}
	if keywordHits != wantKeywordHits || versions != answerCount {
		t.Fatalf("timer cache keyword hits=%d want=%d versions=%d", keywordHits, wantKeywordHits, versions)
	}
	writeEvidence("local-keyword.json", detail())

	// A local-only replacement exercises the short-answer rule using the same
	// original workbook structure. Original evidence remains byte-for-byte intact.
	book, err := excelize.OpenReader(bytes.NewReader(original))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := book.GetRows("AI回答")
	if err != nil {
		t.Fatal(err)
	}
	answerCol := -1
	for i, header := range rows[0] {
		if header == "AI回答" || header == "answer" || header == "answerText" || header == "answer_text" {
			answerCol = i
			break
		}
	}
	if answerCol < 0 {
		t.Fatal("answer column not found")
	}
	cell, _ := excelize.CoordinatesToCellName(answerCol+1, 2)
	if err = book.SetCellValue("AI回答", cell, "短回答"); err != nil {
		t.Fatal(err)
	}
	modified, err := book.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	_ = book.Close()
	mu.Lock()
	files["/short-version.xlsx"] = modified.Bytes()
	urls = append(urls, "/short-version.xlsx")
	mu.Unlock()
	syncFiles()
	var shortHits, currentAnswers int
	if err = db.QueryRow(ctx, `select count(*) filter (where risk_items @> '[{"code":"ANSWER_TOO_SHORT"}]' and validation_status='INVALID' and answer_text='短回答'),count(*) from cdp_task_item_results where task_id=$1`, task.ID).Scan(&shortHits, &currentAnswers); err != nil {
		t.Fatal(err)
	}
	if shortHits != 1 || currentAnswers != answerCount {
		t.Fatal("new file did not replace/validate short answer")
	}
	writeEvidence("local-short-answer.json", detail())
	if err = usecase.DeliverPendingCallbacks(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if callbacks.Load() != 0 || uploadRequests.Load() != 0 {
		t.Fatal("new risky version auto-delivered")
	}
	failCallback.Store(true)
	if retry(map[string]any{"ignoreRisk": true}) || callbacks.Load() != 1 {
		t.Fatal("manual failure was not attempted once")
	}
	if err = usecase.DeliverPendingCallbacks(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if callbacks.Load() != 1 || uploadRequests.Load() != 1 {
		t.Fatal("manual failure enabled automatic retry")
	}
	failCallback.Store(false)
	if !retry(map[string]any{"ignoreRisk": true}) || callbacks.Load() != 2 || uploads.Load() != 2 {
		t.Fatal("manual Yangshi callback failed")
	}
	out, err := cleaner.NewExcelParser().Parse(ctx, "delivered.xlsx", uploadedBody.Load().([]byte))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.ItemResults) != answerCount {
		t.Fatalf("manual delivery dropped answers: %d", len(out.ItemResults))
	}
	foundShort := false
	for _, result := range out.ItemResults {
		if result.AnswerText == "短回答" {
			foundShort = true
		}
	}
	if !foundShort {
		t.Fatal("manual delivery removed invalid answer")
	}
	for _, field := range []string{"riskCount", "riskItems", "validationStatus", "ignoreRisk"} {
		if bytes.Contains(callbackBody.Load().([]byte), []byte(field)) {
			t.Fatalf("internal field leaked: %s", field)
		}
	}
	writeEvidence("local-final.json", detail())
	if err = usecase.DeliverPendingCallbacks(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if callbacks.Load() != 2 {
		t.Fatal("completed task sent duplicate callback")
	}
	mu.Lock()
	for path, count := range downloads {
		if count != 1 {
			t.Errorf("file %s downloaded %d times", path, count)
		}
	}
	mu.Unlock()
	var auditCount int
	if err = db.QueryRow(ctx, `select count(*) from cdp_admin_audit_logs where action='UPDATE_RESULT_VALIDATION_RULES'`).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatal("missing rules audit")
	}
	writeEvidence("replay-summary.json", map[string]any{"sourceTaskId": fixture.SourceTaskID, "sourceSHA256": fixture.Files[0].SHA256, "baselineAnswers": answerCount, "baselineRiskCount": expected.RiskCount, "baselineInvalidCount": expected.InvalidCount, "baselineRiskCodes": expected.RiskCodes, "keywordHits": keywordHits, "shortAnswerHits": shortHits, "automaticCallbacks": 0, "manualCallbackAttempts": callbacks.Load(), "localYangshiUploads": uploads.Load(), "checks": []string{"original answers preserved", "all answers validated", "task and answer risk API", "automatic callback gate", "idempotent import", "timer keyword refresh", "new file replacement", "short answer invalid", "failed manual callback remains gated", "forced Yangshi upload and notify", "no risk metadata in notify", "no duplicate callback", "rules audit"}})
	t.Logf("PASS: source=%d answers=%d missing-reference=%d keyword=%d short=%d auto-callbacks=0 manual-attempts=%d", fixture.SourceTaskID, answerCount, expected.RiskCodes[biz.RiskAnswerNoReference], keywordHits, shortHits, callbacks.Load())
}
