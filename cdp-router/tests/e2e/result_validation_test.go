package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/xuri/excelize/v2"
)

func TestResultValidationHTTPWithRealPersistence(t *testing.T) {
	t.Run("normal", func(t *testing.T) { testResultValidationFlow(t, false) })
	t.Run("yangshi", func(t *testing.T) { testResultValidationFlow(t, true) })
}
func testResultValidationFlow(t *testing.T, yangshi bool) {
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
	var calls atomic.Int32
	var fail atomic.Bool
	var callbackBody atomic.Value
	var uploadRequests, uploads atomic.Int32
	var uploadedBody atomic.Value
	var upstreamURL string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/upload-url") {
			uploadRequests.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 0, "data": map[string]any{"uploadUrl": upstreamURL + "/upload", "method": "PUT", "bucket": "local", "key": "result.xlsx"}})
			return
		}
		if r.URL.Path == "/upload" {
			uploads.Add(1)
			body, _ := io.ReadAll(r.Body)
			uploadedBody.Store(body)
			return
		}
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		callbackBody.Store(string(body))
		if fail.Load() {
			http.Error(w, "retry manually", 500)
			return
		}
		_, _ = io.WriteString(w, `{"accepted":true}`)
	}))
	defer upstream.Close()
	upstreamURL = upstream.URL
	safe := strings.Repeat("完整的回答", 5)
	workbook := func(answer string, refs bool) []byte {
		book, e := excelize.OpenReader(bytes.NewReader(resultWorkbook(t)))
		if e != nil {
			t.Fatal(e)
		}
		defer book.Close()
		if e = book.SetCellValue("AI回答", "E2", answer); e != nil {
			t.Fatal(e)
		}
		if !refs {
			if e = book.DeleteSheet("溯源信息"); e != nil {
				t.Fatal(e)
			}
		}
		buf, e := book.WriteToBuffer()
		if e != nil {
			t.Fatal(e)
		}
		return buf.Bytes()
	}
	files := map[string][]byte{"/short.xlsx": workbook("短回答", false), "/keyword.xlsx": workbook(safe+"违禁词", true), "/safe.xlsx": workbook(safe, true), "/late.xlsx": workbook(safe+"最新", true)}
	files["/disabled.xlsx"] = workbook("违禁词", false)
	files["/reenabled.xlsx"] = workbook("违禁词", false)
	var mu sync.Mutex
	urls := []string{"/short.xlsx"}
	downloads := map[string]int{}
	var providerURL string
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/submit":
			_, _ = io.WriteString(w, `{"code":0,"data":{"externalBatchId":"validation-provider","acceptedCount":1}}`)
		case strings.HasPrefix(r.URL.Path, "/status/"):
			_, _ = io.WriteString(w, `{"code":200,"success":true,"data":{"externalBatchId":"validation-provider","totalCount":1,"successCount":1,"failedCount":0,"questions":[{"questionId":"item-1","status":2}]}}`)
		case strings.HasPrefix(r.URL.Path, "/files/"):
			mu.Lock()
			out := make([]string, 0, len(urls))
			for _, u := range urls {
				out = append(out, providerURL+u)
			}
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "success": true, "data": map[string]any{"externalBatchId": "validation-provider", "fileUrls": out}})
		default:
			mu.Lock()
			content, ok := files[r.URL.Path]
			if ok {
				downloads[r.URL.Path]++
			}
			mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(content)
		}
	}))
	defer downstream.Close()
	providerURL = downstream.URL
	p := cfg.Provider["vendor_a"]
	p.SubmitURL = providerURL + "/submit"
	p.StatusURL = providerURL + "/status/{externalBatchId}"
	p.FilesURL = providerURL + "/files/{externalBatchId}"
	cfg.Provider = map[string]config.ProviderConfig{"vendor_a": p}
	setDefaultProviderCode(cfg, "vendor_a")
	otherProvider := p
	otherProvider.Code, otherProvider.DisplayName = "vendor_b", "vendor_b"
	cfg.Provider["vendor_b"] = otherProvider
	repo := data.NewTaskRepo(db)
	registry := provider.NewRegistry(cfg.Provider)
	platformUsers := syncPlatformUsers(t, ctx, db, cfg)
	store, err := ossutil.New(cfg.OSS)
	if err != nil {
		t.Fatal(err)
	}
	callbackUsers := map[string]config.PlatformUserConfig{}
	for code, user := range cfg.PlatformUsers {
		callbackUsers[code] = user
	}
	if yangshi {
		user := callbackUsers["mainland"]
		user.CallbackMode = "YANGSHI"
		raw, _ := json.Marshal(map[string]string{"baseURL": upstream.URL, "providerCode": "local", "providerToken": "local"})
		user.CallbackConfig = string(raw)
		callbackUsers["mainland"] = user
	}
	cache := biz.NewValidationRuleCache(repo, time.Minute)
	if err = cache.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	usecase := biz.NewTaskUsecase(repo, registry, registry, store, cleaner.NewExcelParser(), callback.NewClient(time.Second, time.Second, callbackUsers, store), biz.TaskUsecaseOptions{PublicBaseURL: cfg.Server.PublicBaseURL, ValidationRules: cache, CallbackMaxTry: 3})
	admin := service.NewAdminService(cfg.Admin, redisClient, data.NewAdminRepo(db), usecase, cfg.Provider)
	server := httptest.NewServer(httpapi.New(service.NewTaskService(usecase, registry, platformUsers, cfg.OSS.Bucket), admin).Handler())
	defer server.Close()
	auth := adminLogin(t, server.URL, cfg)
	decode := func(resp *http.Response, status int) map[string]any {
		t.Helper()
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != status {
			t.Fatalf("status=%d want %d body=%s", resp.StatusCode, status, b)
		}
		var body map[string]any
		if e := json.Unmarshal(b, &body); e != nil {
			t.Fatal(e)
		}
		return body
	}
	rulesURL := server.URL + "/api/admin/v1/result-validation-rules"
	getRules := decode(adminGet(t, rulesURL, auth), 200)["data"].(map[string]any)
	saved := decode(adminPutJSON(t, rulesURL, auth, map[string]any{"expectedVersion": getRules["version"], "keywords": []string{"违禁词"}}), 200)["data"].(map[string]any)
	decode(adminPutJSON(t, rulesURL, auth, map[string]any{"expectedVersion": getRules["version"], "keywords": []string{"过期"}}), 409)
	badAuth := auth
	badAuth.CSRF = "bad"
	decode(adminPutJSON(t, rulesURL, badAuth, map[string]any{"expectedVersion": saved["version"], "keywords": []string{}}), 403)
	if err = cache.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	decode(postJSONWithAPIKey(t, server.URL+"/api/v1/tasks", cfg.PlatformUsers["mainland"].APIKey, map[string]any{"batchNo": "validation-flow", "bizDate": time.Now().Format("2006-01-02"), "callbackUrl": upstream.URL, "items": []map[string]any{{"itemId": "item-1", "platformCode": "yuanbao", "seq": 1, "question": "问题"}}}), 200)
	task, err := repo.GetTaskByBatchNo(ctx, "validation-flow")
	if err != nil {
		t.Fatal(err)
	}
	taskURL := fmt.Sprintf("%s/api/admin/v1/tasks/%d", server.URL, task.ID)
	syncFiles := func() {
		t.Helper()
		decode(adminPostJSON(t, taskURL+"/check-result", auth, map[string]any{"force": true, "notifyCallback": true}), 200)
	}
	checkTask := func(risks, invalid int, status biz.TaskStatus) {
		t.Helper()
		task, err = repo.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if task.RiskCount != risks || task.InvalidAnswerCount != invalid || task.Status != status || task.RiskStatsUpdatedAt == nil {
			t.Fatalf("unexpected task risks=%d invalid=%d status=%s stats=%v", task.RiskCount, task.InvalidAnswerCount, task.Status, task.RiskStatsUpdatedAt)
		}
	}
	// Roles are exercised through real HTTP logins and Redis sessions.
	for _, role := range []string{"viewer", "operator"} {
		roleCfg := *cfg
		roleCfg.Admin.Bootstrap.Role = role
		roleCfg.Admin.Bootstrap.Username = "local-" + role
		roleAdmin := service.NewAdminService(roleCfg.Admin, redisClient, data.NewAdminRepo(db), usecase, cfg.Provider)
		roleServer := httptest.NewServer(httpapi.New(service.NewTaskService(usecase, registry, platformUsers, cfg.OSS.Bucket), roleAdmin).Handler())
		roleAuth := adminLogin(t, roleServer.URL, &roleCfg)
		roleServer.Close()
		decode(adminGet(t, rulesURL, roleAuth), 200)
		decode(adminPutJSON(t, rulesURL, roleAuth, map[string]any{"expectedVersion": saved["version"], "keywords": []string{}}), 403)
		decode(adminPutJSON(t, rulesURL+"/providers/vendor_a", roleAuth, map[string]any{"expectedVersion": saved["version"], "riskInterceptionEnabled": false}), 403)
		if role == "viewer" {
			decode(adminPostJSON(t, taskURL+"/retry-callback", roleAuth, map[string]any{"ignoreRisk": true}), 403)
		}
	}
	syncFiles()
	checkTask(1, 1, biz.TaskStatusResultImported)
	detail := decode(adminGet(t, taskURL, auth), 200)["data"].(map[string]any)
	item := detail["items"].([]any)[0].(map[string]any)
	if item["validationStatus"] != "INVALID" || len(item["riskItems"].([]any)) != 2 {
		t.Fatalf("risk detail=%v", item)
	}
	list := decode(adminGet(t, server.URL+"/api/admin/v1/tasks?hasRisk=true", auth), 200)["data"].(map[string]any)
	if fmt.Sprint(list["total"]) != "1" {
		t.Fatalf("risk filter=%v", list)
	}
	retry := func(payload map[string]any) bool {
		t.Helper()
		body := decode(adminPostJSON(t, taskURL+"/retry-callback", auth, payload), 200)["data"].(map[string]any)
		sent, _ := body["delivered"].(bool)
		return sent
	}
	if err = usecase.DeliverPendingCallbacks(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if retry(map[string]any{}) || retry(map[string]any{"force": true}) || calls.Load() != 0 {
		t.Fatal("automatic/ordinary callbacks bypassed risks")
	}
	decode(adminPostJSON(t, taskURL+"/force-import-callback", auth, map[string]any{"reason": "legacy entry"}), 200)
	if calls.Load() != 0 || uploadRequests.Load() != 0 || uploads.Load() != 0 {
		t.Fatal("risk gate must block before requesting upload URL or uploading")
	}
	fail.Store(true)
	if retry(map[string]any{"ignoreRisk": true}) || calls.Load() != 1 {
		t.Fatal("manual failed callback not attempted exactly once")
	}
	if err = usecase.DeliverPendingCallbacks(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || (yangshi && uploadRequests.Load() != 1) {
		t.Fatal("failed manual override authorized automatic retry")
	}
	fail.Store(false)
	if !retry(map[string]any{"ignoreRisk": true}) || calls.Load() != 2 {
		t.Fatal("manual override failed")
	}
	checkTask(1, 1, biz.TaskStatusDone)
	if yangshi {
		if uploadRequests.Load() != 2 || uploads.Load() != 2 {
			t.Fatal("manual Yangshi path did not upload normally")
		}
		book, e := excelize.OpenReader(bytes.NewReader(uploadedBody.Load().([]byte)))
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, sheet := range book.GetSheetList() {
			rows, e := book.GetRows(sheet)
			if e != nil {
				t.Fatal(e)
			}
			for _, row := range rows {
				for _, cell := range row {
					if cell == "短回答" {
						found = true
					}
					if cell == "validationStatus" || cell == "riskItems" {
						t.Fatal("internal quality fields leaked to workbook")
					}
				}
			}
		}
		_ = book.Close()
		if !found {
			t.Fatal("risky answer was filtered from forced Yangshi workbook")
		}
	}
	var callbackJSON map[string]any
	if err = json.Unmarshal([]byte(callbackBody.Load().(string)), &callbackJSON); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"riskCount", "riskSummary", "ignoreRisk", "validationStatus"} {
		if _, ok := callbackJSON[key]; ok {
			t.Fatalf("internal field leaked: %s", key)
		}
	}

	mu.Lock()
	urls = []string{"/short.xlsx?Signature=new&Expires=2", "/keyword.xlsx"}
	mu.Unlock()
	syncFiles()
	checkTask(1, 1, biz.TaskStatusDone)
	var ruleVersion, answer string
	if err = db.QueryRow(ctx, `select validation_rule_version,answer_text from cdp_task_item_results where task_id=$1`, task.ID).Scan(&ruleVersion, &answer); err != nil {
		t.Fatal(err)
	}
	if ruleVersion != saved["version"] || answer != safe+"违禁词" {
		t.Fatalf("keyword/overwrite mismatch %q %q", ruleVersion, answer)
	}
	mu.Lock()
	urls = []string{"/short.xlsx?Signature=newer", "/keyword.xlsx", "/safe.xlsx"}
	mu.Unlock()
	syncFiles()
	checkTask(0, 0, biz.TaskStatusDone)
	if err = usecase.DeliverPendingCallbacks(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("late DONE import resent callback")
	}
	mu.Lock()
	for name, n := range downloads {
		if n != 1 {
			t.Fatalf("file %s downloaded %d times", name, n)
		}
	}
	mu.Unlock()
	var auditCount int
	if err = db.QueryRow(ctx, `select count(*) from cdp_admin_audit_logs where action='UPDATE_RESULT_VALIDATION_RULES'`).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("rule audit count=%d", auditCount)
	}

	// A second task recovers automatically when a new file removes its risks.
	mu.Lock()
	urls = []string{"/short.xlsx"}
	mu.Unlock()
	decode(postJSONWithAPIKey(t, server.URL+"/api/v1/tasks", cfg.PlatformUsers["mainland"].APIKey, map[string]any{"batchNo": "validation-recovery", "bizDate": time.Now().Format("2006-01-02"), "callbackUrl": upstream.URL, "items": []map[string]any{{"itemId": "item-1", "platformCode": "yuanbao", "seq": 1, "question": "问题"}}}), 200)
	task, err = repo.GetTaskByBatchNo(ctx, "validation-recovery")
	if err != nil {
		t.Fatal(err)
	}
	taskURL = fmt.Sprintf("%s/api/admin/v1/tasks/%d", server.URL, task.ID)
	syncFiles()
	checkTask(1, 1, biz.TaskStatusResultImported)
	mu.Lock()
	urls = []string{"/short.xlsx", "/late.xlsx"}
	mu.Unlock()
	syncFiles()
	checkTask(0, 0, biz.TaskStatusCallbackPending)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := usecase.DeliverPendingCallbacks(ctx, 10); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	checkTask(0, 0, biz.TaskStatusDone)
	if calls.Load() != 3 {
		t.Fatalf("concurrent workers sent %d total callbacks want 3", calls.Load())
	}
	// All suppliers validate answers; switches only control risk interception.
	createValidationTask := func(batch, providerCode string) {
		t.Helper()
		seedDispatchProfile(t, ctx, db, "mainland", "Local E2E", providerCode)
		decode(postJSONWithAPIKey(t, server.URL+"/api/v1/tasks", cfg.PlatformUsers["mainland"].APIKey, map[string]any{"batchNo": batch, "bizDate": time.Now().Format("2006-01-02"), "callbackUrl": upstream.URL, "items": []map[string]any{{"itemId": "item-1", "platformCode": "yuanbao", "seq": 1, "question": "问题"}}}), 200)
		task, err = repo.GetTaskByBatchNo(ctx, batch)
		if err != nil {
			t.Fatal(err)
		}
		if task.ProviderCode != providerCode {
			t.Fatalf("wrong provider %s", task.ProviderCode)
		}
		taskURL = fmt.Sprintf("%s/api/admin/v1/tasks/%d", server.URL, task.ID)
	}
	setFiles := func(paths ...string) { mu.Lock(); urls = paths; mu.Unlock() }
	assertAnswer := func(status string, risks int, version string) {
		t.Helper()
		detail := decode(adminGet(t, taskURL, auth), 200)["data"].(map[string]any)
		answer := detail["items"].([]any)[0].(map[string]any)
		if answer["validationStatus"] != status || len(answer["riskItems"].([]any)) != risks || answer["validationRuleVersion"] != version {
			t.Fatalf("answer=%v", answer)
		}
		var persistedStatus, persistedVersion string
		var count int
		if e := db.QueryRow(ctx, `select validation_status,jsonb_array_length(risk_items),validation_rule_version from cdp_task_item_results where task_id=$1`, task.ID).Scan(&persistedStatus, &count, &persistedVersion); e != nil {
			t.Fatal(e)
		}
		if persistedStatus != status || count != risks || persistedVersion != version {
			t.Fatalf("persisted answer=%s %d %s", persistedStatus, count, persistedVersion)
		}
	}

	setFiles("/short.xlsx")
	createValidationTask("provider-switch", "vendor_a")
	syncFiles()
	checkTask(1, 1, biz.TaskStatusResultImported)
	providerRulesURL := rulesURL + "/providers/vendor_a"
	decode(adminPutJSON(t, providerRulesURL, auth, map[string]any{"expectedVersion": saved["version"]}), 400)
	decode(adminPutJSON(t, providerRulesURL, auth, map[string]any{"expectedVersion": saved["version"], "enabled": false}), 400) // old UI cannot silently change policy
	decode(adminPutJSON(t, rulesURL+"/providers/missing", auth, map[string]any{"expectedVersion": saved["version"], "riskInterceptionEnabled": false}), 400)
	decode(adminPutJSON(t, providerRulesURL, badAuth, map[string]any{"expectedVersion": saved["version"], "riskInterceptionEnabled": false}), 403)
	disabled := decode(adminPutJSON(t, providerRulesURL, auth, map[string]any{"expectedVersion": saved["version"], "riskInterceptionEnabled": false}), 200)["data"].(map[string]any)
	if fmt.Sprint(disabled["riskCallbackAllowedProviderCodes"]) != "[vendor_a]" || fmt.Sprint(disabled["keywords"]) != "[违禁词]" {
		t.Fatalf("policy=%v", disabled)
	}
	decode(adminPutJSON(t, providerRulesURL, auth, map[string]any{"expectedVersion": saved["version"], "riskInterceptionEnabled": true}), 409)
	disabled = decode(adminPutJSON(t, rulesURL, auth, map[string]any{"expectedVersion": disabled["version"], "keywords": []string{"违禁词"}}), 200)["data"].(map[string]any)
	if fmt.Sprint(disabled["riskCallbackAllowedProviderCodes"]) != "[vendor_a]" {
		t.Fatal("keyword save erased policy")
	}
	// Persisting does not bypass the cache. After refresh, already-imported risky tasks can queue.
	if err = usecase.ImportPendingResults(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err = usecase.DeliverPendingCallbacks(ctx, 1); err != nil {
		t.Fatal(err)
	}
	checkTask(1, 1, biz.TaskStatusResultImported)
	if calls.Load() != 3 {
		t.Fatal("policy changed before refresh")
	}
	if err = cache.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err = usecase.ImportPendingResults(ctx, 1); err != nil {
		t.Fatal(err)
	}
	checkTask(1, 1, biz.TaskStatusCallbackPending)
	assertAnswer("INVALID", 2, saved["version"].(string)) // no reimport, no risk clearing
	if err = usecase.DeliverPendingCallbacks(ctx, 1); err != nil {
		t.Fatal(err)
	}
	checkTask(1, 1, biz.TaskStatusDone)
	if calls.Load() != 4 {
		t.Fatal("risk-allowed historical task was not sent")
	}

	setFiles("/disabled.xlsx")
	createValidationTask("all-answers-validated", "vendor_a")
	syncFiles()
	checkTask(1, 1, biz.TaskStatusCallbackPending)
	assertAnswer("INVALID", 3, disabled["version"].(string))
	enabled := decode(adminPutJSON(t, providerRulesURL, auth, map[string]any{"expectedVersion": disabled["version"], "riskInterceptionEnabled": true}), 200)["data"].(map[string]any)
	if fmt.Sprint(enabled["riskCallbackAllowedProviderCodes"]) != "[]" || fmt.Sprint(enabled["keywords"]) != "[违禁词]" {
		t.Fatalf("policy=%v", enabled)
	}
	syncFiles() // unchanged file and last loaded policy remain in effect
	checkTask(1, 1, biz.TaskStatusCallbackPending)
	assertAnswer("INVALID", 3, disabled["version"].(string))
	if err = cache.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err = usecase.DeliverPendingCallbacks(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 4 || retry(map[string]any{}) || retry(map[string]any{"force": true}) {
		t.Fatal("enabled interception allowed risk callback")
	}
	decode(adminPostJSON(t, taskURL+"/force-import-callback", auth, map[string]any{"reason": "risk must block legacy entry"}), 200)
	if calls.Load() != 4 {
		t.Fatal("legacy force-import bypassed interception")
	}
	if !retry(map[string]any{"ignoreRisk": true}) || calls.Load() != 5 {
		t.Fatal("manual risk callback failed")
	}
	checkTask(1, 1, biz.TaskStatusDone)

	// B remains intercepted and must not occupy the LIMIT ahead of eligible A.
	createValidationTask("provider-isolation", "vendor_b")
	syncFiles()
	checkTask(1, 1, biz.TaskStatusResultImported)
	assertAnswer("INVALID", 3, enabled["version"].(string))
	createValidationTask("queue-after-policy-change", "vendor_a")
	syncFiles()
	checkTask(1, 1, biz.TaskStatusResultImported)
	disabled = decode(adminPutJSON(t, providerRulesURL, auth, map[string]any{"expectedVersion": enabled["version"], "riskInterceptionEnabled": false}), 200)["data"].(map[string]any)
	if err = cache.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err = usecase.ImportPendingResults(ctx, 1); err != nil {
		t.Fatal(err)
	}
	checkTask(1, 1, biz.TaskStatusCallbackPending)
	fail.Store(true)
	if err = usecase.DeliverPendingCallbacks(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 6 {
		t.Fatal("eligible risky task was starved or not attempted")
	}
	fail.Store(false)
	if err = usecase.DeliverPendingCallbacks(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 7 {
		t.Fatal("risk-allowed callback retry failed")
	}
	checkTask(1, 1, biz.TaskStatusDone)
	other, err := repo.GetTaskByBatchNo(ctx, "provider-isolation")
	if err != nil {
		t.Fatal(err)
	}
	if other.Status != biz.TaskStatusResultImported || other.RiskCount != 1 {
		t.Fatal("provider B policy affected")
	}
	// Repair old SKIPPED answers from DB only, including completed historical tasks.
	if _, err = db.Exec(ctx, `update cdp_task_item_results set validation_status='SKIPPED',risk_items='[]' where task_id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if err = data.Migrate(ctx, db, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	afterMigration := decode(adminGet(t, rulesURL, auth), 200)["data"].(map[string]any)
	if afterMigration["version"] != disabled["version"] || fmt.Sprint(afterMigration["riskCallbackAllowedProviderCodes"]) != "[vendor_a]" {
		t.Fatal("replayed migration reset policy")
	}
	stale, err := repo.GetTask(ctx, task.ID)
	if err != nil || stale.RiskStatsUpdatedAt != nil {
		t.Fatalf("legacy statistics still trusted: %v", err)
	}
	mu.Lock()
	downloadsBefore := downloads["/disabled.xlsx"]
	mu.Unlock()
	if err = usecase.ImportPendingResults(ctx, 10); err != nil {
		t.Fatal(err)
	}
	assertAnswer("INVALID", 3, disabled["version"].(string))
	checkTask(1, 1, biz.TaskStatusDone)
	mu.Lock()
	downloadsAfter := downloads["/disabled.xlsx"]
	mu.Unlock()
	if downloadsBefore != downloadsAfter {
		t.Fatal("legacy repair redownloaded imported files")
	}
	if err = usecase.DeliverPendingCallbacks(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 7 {
		t.Fatal("historical DONE repair resent callback")
	}
	var toggles int
	if err = db.QueryRow(ctx, `select count(*) from cdp_admin_audit_logs where action='UPDATE_PROVIDER_RISK_INTERCEPTION' and resource_id='vendor_a' and payload->>'riskInterceptionEnabled' in ('true','false')`).Scan(&toggles); err != nil {
		t.Fatal(err)
	}
	if toggles != 3 {
		t.Fatalf("policy audits=%d", toggles)
	}
}
