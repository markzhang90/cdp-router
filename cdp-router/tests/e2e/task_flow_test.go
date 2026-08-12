package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
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
	"github.com/GitMarkZhang/cdp-router/internal/pkg/timezone"
	"github.com/GitMarkZhang/cdp-router/internal/provider"
	"github.com/GitMarkZhang/cdp-router/internal/service"
	"github.com/GitMarkZhang/cdp-router/internal/worker"
	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/xuri/excelize/v2"
)

func TestTaskProxyFlowWithRealPostgresAndOSS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	cfg, err := config.Load(filepath.Join("..", "..", "configs", "cdp-router.conf"))
	if err != nil {
		t.Fatal(err)
	}
	ensureE2EProviderConfig(cfg)

	if err := data.EnsureDatabase(ctx, cfg.Postgres); err != nil {
		t.Fatal(err)
	}
	db, err := data.Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := data.Migrate(ctx, db, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}

	batchNo := fmt.Sprintf("Q2A2-e2e-%d", time.Now().UnixNano())
	defer func() {
		cleanupTaskDataByBatchNo(t, db, batchNo)
	}()

	var callbackMu sync.Mutex
	var callbackPayload map[string]any
	callbackDone := make(chan struct{})
	providerErr := make(chan string, 1)
	businessCallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, _ := io.ReadAll(r.Body)
		callbackMu.Lock()
		_ = json.Unmarshal(body, &callbackPayload)
		callbackMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accepted":true}`))
		closeOnce(callbackDone)
	}))
	defer businessCallback.Close()

	var gatewayBaseURL string
	var providerServerURL string
	expectedPrefix := "cdp-router/e2e/vendor-a-managed"
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/provider/submit":
		case "/provider/status/vendor-batch-e2e":
			if got := r.Header.Get("X-Provider-Token"); got != cfg.Provider["vendor_a"].Token {
				http.Error(w, "bad provider token", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"message":"查询成功","code":200,"data":{"externalBatchId":"vendor-batch-e2e","questions":[{"questionId":"item-1","status":2}]}}`))
			return
		case "/provider/files/vendor-batch-e2e":
			if got := r.Header.Get("X-Provider-Token"); got != cfg.Provider["vendor_a"].Token {
				http.Error(w, "bad provider token", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"success":true,"message":"查询成功","code":200,"data":{"externalBatchId":"vendor-batch-e2e","fileUrls":["%s/provider/result.xlsx"]}}`, providerServerURL)))
			return
		case "/provider/result.xlsx":
			w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
			_, _ = w.Write(resultWorkbook(t))
			return
		default:
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("X-Provider-Token"); got != cfg.Provider["vendor_a"].Token {
			http.Error(w, "bad provider token", http.StatusUnauthorized)
			return
		}
		defer r.Body.Close()
		var req struct {
			BatchNo      string `json:"batchNo"`
			ProviderCode string `json:"providerCode"`
			BizDate      string `json:"bizDate"`
			CallbackURL  string `json:"callbackUrl"`
			Items        []any  `json:"items"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.ProviderCode != "vendor_a" {
			http.Error(w, fmt.Sprintf("providerCode=%q, want vendor_a", req.ProviderCode), http.StatusBadRequest)
			return
		}
		if req.CallbackURL != gatewayBaseURL+"/external/callback/vendor_a" {
			http.Error(w, fmt.Sprintf("callbackUrl=%q", req.CallbackURL), http.StatusBadRequest)
			return
		}
		go providerUploadFilesAndCallback(t, cfg.Provider["vendor_a"].Token, gatewayBaseURL, req.BatchNo, req.ProviderCode, expectedPrefix, []string{"result-a.xlsx", "result-b.xlsx"}, providerErr)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"externalBatchId":"vendor-batch-e2e","acceptedCount":1}}`))
	}))
	providerServerURL = providerServer.URL
	defer providerServer.Close()

	providerCfg := cfg.Provider["vendor_a"]
	providerCfg.SubmitURL = providerServer.URL + "/provider/submit"
	providerCfg.StatusURL = providerServer.URL + "/provider/status/{externalBatchId}"
	providerCfg.FilesURL = providerServer.URL + "/provider/files/{externalBatchId}"
	providerCfg.BucketPathPrefix = expectedPrefix
	cfg.Provider = map[string]config.ProviderConfig{"vendor_a": providerCfg}
	setDefaultProviderCode(cfg, "vendor_a")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gatewayBaseURL = "http://" + listener.Addr().String()
	cfg.Server.PublicBaseURL = gatewayBaseURL

	store, err := ossutil.New(cfg.OSS)
	if err != nil {
		t.Fatal(err)
	}
	repo := data.NewTaskRepo(db)
	platformUsers := syncPlatformUsers(t, ctx, db, cfg)
	providers := provider.NewRegistry(cfg.Provider)
	usecase := biz.NewTaskUsecase(repo, providers, providers, store, cleaner.NewExcelParser(), callback.NewHTTPClient(10*time.Second), biz.TaskUsecaseOptions{
		PublicBaseURL:           cfg.Server.PublicBaseURL,
		OSSUploadDir:            cfg.OSS.UploadDir,
		OSSExpire:               time.Duration(cfg.OSS.ExpireSeconds) * time.Second,
		TempObjectRetentionDays: cfg.OSS.TempObjectRetentionDays,
		CallbackMaxTry:          cfg.Callback.MaxRetry,
	})
	server := httpapi.New(service.NewTaskService(usecase, providers, platformUsers, cfg.OSS.Bucket), nil)
	httpServer := &http.Server{Handler: server.Handler()}
	go func() { _ = httpServer.Serve(listener) }()
	defer func() {
		_ = httpServer.Shutdown(context.Background())
	}()

	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()
	go worker.New(usecase).Run(workerCtx)

	createBody := map[string]any{
		"batchNo":      batchNo,
		"providerCode": "ignored_request_provider",
		"bizDate":      time.Now().Format("2006-01-02"),
		"callbackUrl":  businessCallback.URL,
		"items": []map[string]any{
			{
				"itemId":       "item-1",
				"platformCode": "yuanbao",
				"seq":          1,
				"question":     "请评价某产品的优缺点",
				"deepthink":    true,
				"webSearch":    true,
			},
		},
	}
	resp := postJSONWithAPIKey(t, gatewayBaseURL+"/api/v1/tasks", cfg.PlatformUsers["mainland"].APIKey, createBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create task status=%d body=%s", resp.StatusCode, readBody(resp))
	}
	var createResp struct {
		Code int `json:"code"`
		Data struct {
			ExternalBatchID string `json:"externalBatchId"`
			AcceptedCount   int    `json:"acceptedCount"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatal(err)
	}
	if createResp.Code != 0 || createResp.Data.ExternalBatchID == "" || createResp.Data.AcceptedCount != 1 {
		t.Fatalf("unexpected create response: %+v", createResp)
	}

	select {
	case <-callbackDone:
	case errMessage := <-providerErr:
		t.Fatal(errMessage)
	case <-ctx.Done():
		t.Fatal("timed out waiting for business callback")
	}

	task := waitTaskStatus(t, ctx, repo, batchNo, biz.TaskStatusDone)
	if len(task.ResultPayload) == 0 || string(task.ResultPayload) == "{}" {
		t.Fatal("expected result payload saved in database")
	}
	if task.ResultObjectKey == "" {
		t.Fatal("expected result object key")
	}
	uploadedKeys := listResultObjectKeys(t, db, task.ID)
	defer func() {
		for _, key := range uploadedKeys {
			deleteOSSObject(t, cfg.OSS, key)
		}
	}()
	if len(uploadedKeys) != 2 {
		t.Fatalf("uploaded result file count=%d, want 2 keys=%v", len(uploadedKeys), uploadedKeys)
	}
	if task.ResultRowCount == 0 {
		t.Fatal("expected result rows saved in database")
	}
	assertResultRowsAndItemBackfill(t, db, task.ID)

	externalTaskResp := getWithAPIKey(t, gatewayBaseURL+"/api/v1/external-tasks/"+createResp.Data.ExternalBatchID, cfg.PlatformUsers["mainland"].APIKey)
	if externalTaskResp.StatusCode != http.StatusOK {
		t.Fatalf("external task status=%d body=%s", externalTaskResp.StatusCode, readBody(externalTaskResp))
	}
	_ = externalTaskResp.Body.Close()

	externalResultResp := getWithAPIKey(t, gatewayBaseURL+"/api/v1/external-tasks/"+createResp.Data.ExternalBatchID+"/result", cfg.PlatformUsers["mainland"].APIKey)
	if externalResultResp.StatusCode != http.StatusOK {
		t.Fatalf("external task result status=%d body=%s", externalResultResp.StatusCode, readBody(externalResultResp))
	}
	var externalResultBody struct {
		Data struct {
			ExternalBatchID string `json:"externalBatchId"`
			ResultURL       string `json:"resultUrl"`
			Items           []struct {
				ItemID     string `json:"itemId"`
				AnswerText string `json:"answerText"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.NewDecoder(externalResultResp.Body).Decode(&externalResultBody); err != nil {
		t.Fatal(err)
	}
	if externalResultBody.Data.ExternalBatchID != createResp.Data.ExternalBatchID || externalResultBody.Data.ResultURL == "" || len(externalResultBody.Data.Items) == 0 {
		t.Fatalf("unexpected external result body: %+v", externalResultBody.Data)
	}

	statusResp := get(t, gatewayBaseURL+"/api/v1/tasks/"+fmt.Sprint(task.ID)+"/provider-status")
	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("provider status endpoint status=%d body=%s", statusResp.StatusCode, readBody(statusResp))
	}
	_ = statusResp.Body.Close()

	filesResp := get(t, gatewayBaseURL+"/api/v1/tasks/"+fmt.Sprint(task.ID)+"/provider-result-files")
	if filesResp.StatusCode != http.StatusOK {
		t.Fatalf("provider result files endpoint status=%d body=%s", filesResp.StatusCode, readBody(filesResp))
	}
	_ = filesResp.Body.Close()

	refetchResp := postJSON(t, gatewayBaseURL+"/api/v1/tasks/"+fmt.Sprint(task.ID)+"/refetch-result", "", map[string]any{
		"force":          true,
		"notifyCallback": false,
	})
	if refetchResp.StatusCode != http.StatusOK {
		t.Fatalf("refetch result status=%d body=%s", refetchResp.StatusCode, readBody(refetchResp))
	}
	_ = refetchResp.Body.Close()
	assertResultRowsAndItemBackfill(t, db, task.ID)

	callbackMu.Lock()
	gotBatchNo := callbackPayload["batchNo"]
	gotExternalBatchID := callbackPayload["externalBatchId"]
	gotResultURL := callbackPayload["resultUrl"]
	_, hasInlineResult := callbackPayload["result"]
	callbackMu.Unlock()
	if gotBatchNo != batchNo {
		t.Fatalf("callback batchNo=%v, want %s", gotBatchNo, batchNo)
	}
	if gotExternalBatchID != createResp.Data.ExternalBatchID || gotResultURL == "" || hasInlineResult {
		t.Fatalf("callback externalBatchId=%v resultUrl=%v hasInlineResult=%v", gotExternalBatchID, gotResultURL, hasInlineResult)
	}
}

func TestYangshiDevCustomCallbackWithRealPostgresAndOSS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	cfg, err := config.Load(filepath.Join("..", "..", "configs", "cdp-router.conf"))
	if err != nil {
		t.Fatal(err)
	}
	ensureE2EProviderConfig(cfg)

	if err := data.EnsureDatabase(ctx, cfg.Postgres); err != nil {
		t.Fatal(err)
	}
	db, err := data.Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := data.Migrate(ctx, db, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}

	batchNo := fmt.Sprintf("Q2A2-yangshi-dev-e2e-%d", time.Now().UnixNano())
	defer cleanupTaskDataByBatchNo(t, db, batchNo)

	const yangshiToken = "yangshi-provider-token"
	var yangshiBaseURL string
	uploadDone := make(chan []byte, 1)
	callbackDone := make(chan map[string]any, 1)
	yangshiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/upload/") {
			if r.Method != http.MethodPut {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			defer r.Body.Close()
			body, _ := io.ReadAll(r.Body)
			uploadDone <- body
			w.WriteHeader(http.StatusOK)
			return
		}
		if got := r.Header.Get("X-Provider-Token"); got != yangshiToken {
			http.Error(w, "bad yangshi token", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/external/oss/vendor_a/upload-url":
			defer r.Body.Close()
			var req struct {
				BatchNo  string `json:"batchNo"`
				FileName string `json:"fileName"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if req.BatchNo != batchNo || req.FileName == "" {
				http.Error(w, fmt.Sprintf("unexpected upload-url request: %+v", req), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"status":0,"message":null,"data":{"bucket":"yangshi-bucket","key":"external/q2a/vendor_a/%s/%s","uploadUrl":"%s/upload/%s?signature=put-token","method":"PUT","expiresAt":"2026-07-27T12:00:00+08:00","headers":{}}}`, batchNo, req.FileName, yangshiBaseURL, req.FileName)))
		case "/external/callback/vendor_a", "/custom/callback/vendor_a":
			defer r.Body.Close()
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			callbackDone <- payload
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"status":0,"message":null,"data":{"batchNo":%q,"accepted":true}}`, batchNo)))
		default:
			http.NotFound(w, r)
		}
	}))
	yangshiBaseURL = yangshiServer.URL
	defer yangshiServer.Close()

	yangshiUser := config.PlatformUserConfig{
		PlatformCode:        "yangshi_dev",
		Name:                "阳狮测试",
		APIKey:              "yangshi-api-key",
		Status:              "ACTIVE",
		DefaultProviderCode: "vendor_a",
		CallbackMode:        "CUSTOM",
		CallbackURL:         "",
		CallbackConfig:      fmt.Sprintf(`{"baseURL":%q,"providerCode":"vendor_a","providerToken":%q}`, yangshiBaseURL, yangshiToken),
	}
	cfg.PlatformUsers = map[string]config.PlatformUserConfig{"yangshi_dev": yangshiUser}

	var gatewayBaseURL string
	expectedPrefix := "cdp-router/e2e/yangshi-managed"
	providerErr := make(chan string, 1)
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/provider/submit" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("X-Provider-Token"); got != cfg.Provider["vendor_a"].Token {
			http.Error(w, "bad provider token", http.StatusUnauthorized)
			return
		}
		defer r.Body.Close()
		var req struct {
			BatchNo      string `json:"batchNo"`
			ProviderCode string `json:"providerCode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		go providerUploadFilesAndCallback(t, cfg.Provider["vendor_a"].Token, gatewayBaseURL, req.BatchNo, req.ProviderCode, expectedPrefix, []string{"yangshi-a.xlsx", "yangshi-b.xlsx"}, providerErr)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"externalBatchId":"vendor-batch-yangshi-e2e","acceptedCount":1}}`))
	}))
	defer providerServer.Close()
	providerCfg := cfg.Provider["vendor_a"]
	providerCfg.SubmitURL = providerServer.URL + "/provider/submit"
	providerCfg.BucketPathPrefix = expectedPrefix
	cfg.Provider = map[string]config.ProviderConfig{"vendor_a": providerCfg}
	setDefaultProviderCode(cfg, "vendor_a")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gatewayBaseURL = "http://" + listener.Addr().String()
	cfg.Server.PublicBaseURL = gatewayBaseURL

	store, err := ossutil.New(cfg.OSS)
	if err != nil {
		t.Fatal(err)
	}
	repo := data.NewTaskRepo(db)
	platformUsers := syncPlatformUsers(t, ctx, db, cfg)
	providers := provider.NewRegistry(cfg.Provider)
	usecase := biz.NewTaskUsecase(repo, providers, providers, store, cleaner.NewExcelParser(), callback.NewClient(10*time.Second, cfg.PlatformUsers, store, cfg.OSS.PublicHost, cfg.OSS.Host), biz.TaskUsecaseOptions{
		PublicBaseURL:           cfg.Server.PublicBaseURL,
		OSSUploadDir:            cfg.OSS.UploadDir,
		OSSExpire:               time.Duration(cfg.OSS.ExpireSeconds) * time.Second,
		TempObjectRetentionDays: cfg.OSS.TempObjectRetentionDays,
		CallbackMaxTry:          cfg.Callback.MaxRetry,
	})
	server := httpapi.New(service.NewTaskService(usecase, providers, platformUsers, cfg.OSS.Bucket), nil)
	httpServer := &http.Server{Handler: server.Handler()}
	go func() { _ = httpServer.Serve(listener) }()
	defer func() { _ = httpServer.Shutdown(context.Background()) }()

	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()
	go worker.New(usecase).Run(workerCtx)

	createBody := map[string]any{
		"batchNo":      batchNo,
		"providerCode": "vendor_a",
		"bizDate":      time.Now().Format("2006-01-02"),
		"callbackUrl":  yangshiBaseURL + "/custom/callback/vendor_a",
		"items": []map[string]any{
			{
				"itemId":       "item-1",
				"platformCode": "yuanbao",
				"seq":          1,
				"question":     "请评价某产品的优缺点",
				"deepthink":    true,
				"webSearch":    true,
			},
		},
	}
	resp := postJSONWithAPIKey(t, gatewayBaseURL+"/api/v1/tasks", yangshiUser.APIKey, createBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create task status=%d body=%s", resp.StatusCode, readBody(resp))
	}
	var createResp struct {
		Data struct {
			ExternalBatchID string `json:"externalBatchId"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	var uploaded []byte
	select {
	case uploaded = <-uploadDone:
	case errMessage := <-providerErr:
		t.Fatal(errMessage)
	case <-ctx.Done():
		t.Fatal("timed out waiting for yangshi upload")
	}
	if len(uploaded) == 0 {
		t.Fatal("expected yangshi upload content")
	}
	assertMergedWorkbookRows(t, uploaded, "AI回答", 2)
	assertMergedWorkbookRows(t, uploaded, "溯源信息", 3)
	var notifyPayload map[string]any
	select {
	case notifyPayload = <-callbackDone:
	case errMessage := <-providerErr:
		t.Fatal(errMessage)
	case <-ctx.Done():
		t.Fatal("timed out waiting for yangshi callback")
	}
	if notifyPayload["batchNo"] != batchNo || notifyPayload["externalBatchId"] != createResp.Data.ExternalBatchID || notifyPayload["status"] != string(biz.TaskStatusDone) {
		t.Fatalf("unexpected yangshi callback payload: %+v", notifyPayload)
	}

	task := waitTaskStatus(t, ctx, repo, batchNo, biz.TaskStatusDone)
	if task.ResultObjectKey == "" {
		t.Fatal("expected result object key")
	}
	uploadedKeys := listResultObjectKeys(t, db, task.ID)
	defer func() {
		for _, key := range uploadedKeys {
			deleteOSSObject(t, cfg.OSS, key)
		}
	}()
	if len(uploadedKeys) != 2 {
		t.Fatalf("yangshi uploaded result file count=%d, want 2 keys=%v", len(uploadedKeys), uploadedKeys)
	}
	var callbackTarget string
	var requestPayload []byte
	if err := db.QueryRow(ctx, `select target_url, request_payload from cdp_task_callbacks where task_id=$1 and status='DONE' order by id desc limit 1`, task.ID).Scan(&callbackTarget, &requestPayload); err != nil {
		t.Fatal(err)
	}
	if callbackTarget != yangshiBaseURL+"/custom/callback/vendor_a" {
		t.Fatalf("callback target=%q", callbackTarget)
	}
	if !strings.Contains(string(requestPayload), "uploadUrlRequest") || !strings.Contains(string(requestPayload), "callbackRequest") {
		t.Fatalf("callback request payload missing yangshi details: %s", string(requestPayload))
	}
	var eventPayloadBytes []byte
	if err := db.QueryRow(ctx, `
select payload
from cdp_task_events
where task_id=$1 and event_type='BUSINESS_CALLBACK_OK'
order by id desc
limit 1
`, task.ID).Scan(&eventPayloadBytes); err != nil {
		t.Fatal(err)
	}
	var eventPayload struct {
		UploadedFile struct {
			SourceType string `json:"sourceType"`
			Key        string `json:"key"`
			FileURL    string `json:"fileUrl"`
			FileName   string `json:"fileName"`
			ItemCount  int    `json:"itemCount"`
		} `json:"uploadedFile"`
	}
	if err := json.Unmarshal(eventPayloadBytes, &eventPayload); err != nil {
		t.Fatal(err)
	}
	if eventPayload.UploadedFile.SourceType != "DB_ITEM_RESULTS" || !strings.Contains(eventPayload.UploadedFile.Key, batchNo) || eventPayload.UploadedFile.FileName != batchNo+"-result.xlsx" || eventPayload.UploadedFile.FileURL != yangshiBaseURL+"/upload/"+batchNo+"-result.xlsx" || eventPayload.UploadedFile.ItemCount != 1 {
		t.Fatalf("unexpected callback event payload: %s", string(eventPayloadBytes))
	}
}

func TestSyncProviderResultWithRealExcelAndPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	cfg, err := config.Load(filepath.Join("..", "..", "configs", "cdp-router.conf"))
	if err != nil {
		t.Fatal(err)
	}
	ensureE2EProviderConfig(cfg)
	if err := data.EnsureDatabase(ctx, cfg.Postgres); err != nil {
		t.Fatal(err)
	}
	db, err := data.Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := data.Migrate(ctx, db, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}

	batchNo := fmt.Sprintf("Q2A2-sync-e2e-%d", time.Now().UnixNano())
	defer func() {
		cleanupTaskDataByBatchNo(t, db, batchNo)
	}()

	realWorkbook, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "AI数据接入文档", "8ce0e57e-数据采集_2026-07-23T18-09-59.xlsx"))
	if err != nil {
		t.Fatal(err)
	}

	var callbackMu sync.Mutex
	var callbackPayload map[string]any
	callbackDone := make(chan struct{})
	businessCallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, _ := io.ReadAll(r.Body)
		callbackMu.Lock()
		_ = json.Unmarshal(body, &callbackPayload)
		callbackMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accepted":true}`))
		closeOnce(callbackDone)
	}))
	defer businessCallback.Close()

	var providerServerURL string
	const providerCallbackToken = "provider-token-sync-e2e"
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/provider/result-real.xlsx" && r.Header.Get("X-Provider-Token") != providerCallbackToken {
			http.Error(w, "bad provider token", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/provider/submit":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"externalBatchId":"vendor-batch-sync-e2e","acceptedCount":2}}`))
		case "/provider/status/vendor-batch-sync-e2e":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"message":"查询成功","code":200,"data":{"externalBatchId":"vendor-batch-sync-e2e","questions":[{"questionId":"MPZ-REAL-20260723180500-doubao-001","status":2},{"questionId":"MPZ-REAL-20260723180500-qwen-002","status":2}]}}`))
		case "/provider/files/vendor-batch-sync-e2e":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"success":true,"message":"查询成功","code":200,"data":{"externalBatchId":"vendor-batch-sync-e2e","fileUrls":["%s/provider/result-real.xlsx"]}}`, providerServerURL)))
		case "/provider/result-real.xlsx":
			w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
			_, _ = w.Write(realWorkbook)
		default:
			http.NotFound(w, r)
		}
	}))
	providerServerURL = providerServer.URL
	defer providerServer.Close()

	providerCfg := cfg.Provider["vendor_a"]
	providerCfg.SubmitURL = providerServer.URL + "/provider/submit"
	providerCfg.StatusURL = providerServer.URL + "/provider/status/{externalBatchId}"
	providerCfg.FilesURL = providerServer.URL + "/provider/files/{externalBatchId}"
	providerCfg.Token = providerCallbackToken
	providerCfg.APIKey = ""
	cfg.Provider = map[string]config.ProviderConfig{"vendor_a": providerCfg}
	setDefaultProviderCode(cfg, "vendor_a")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gatewayBaseURL := "http://" + listener.Addr().String()
	cfg.Server.PublicBaseURL = gatewayBaseURL

	store, err := ossutil.New(cfg.OSS)
	if err != nil {
		t.Fatal(err)
	}
	repo := data.NewTaskRepo(db)
	platformUsers := syncPlatformUsers(t, ctx, db, cfg)
	providers := provider.NewRegistry(cfg.Provider)
	usecase := biz.NewTaskUsecase(repo, providers, providers, store, cleaner.NewExcelParser(), callback.NewHTTPClient(10*time.Second), biz.TaskUsecaseOptions{
		PublicBaseURL:           cfg.Server.PublicBaseURL,
		OSSUploadDir:            cfg.OSS.UploadDir,
		OSSExpire:               time.Duration(cfg.OSS.ExpireSeconds) * time.Second,
		TempObjectRetentionDays: cfg.OSS.TempObjectRetentionDays,
		CallbackMaxTry:          cfg.Callback.MaxRetry,
	})
	server := httpapi.New(service.NewTaskService(usecase, providers, platformUsers, cfg.OSS.Bucket), nil)
	httpServer := &http.Server{Handler: server.Handler()}
	go func() { _ = httpServer.Serve(listener) }()
	defer func() {
		_ = httpServer.Shutdown(context.Background())
	}()

	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()
	go worker.New(usecase).Run(workerCtx)

	resp := postJSONWithAuthHeaders(t, gatewayBaseURL+"/api/v1/tasks", map[string]string{
		"X-API-Key":        "wrong-api-key",
		"X-Provider-Token": cfg.PlatformUsers["mainland"].APIKey,
	}, map[string]any{
		"batchNo":      batchNo,
		"providerCode": "vendor_a",
		"bizDate":      time.Now().Format("2006-01-02"),
		"callbackUrl":  businessCallback.URL,
		"items": []map[string]any{
			{
				"itemId":       "MPZ-REAL-20260723180500-doubao-001",
				"platformCode": "doubao",
				"seq":          1,
				"question":     "请从用户体验、性价比和售后服务三个角度评价一款智能家居摄像头的优缺点，并给出购买建议。",
				"deepthink":    true,
				"webSearch":    true,
			},
			{
				"itemId":       "MPZ-REAL-20260723180500-qwen-002",
				"platformCode": "qwen",
				"seq":          2,
				"question":     "请分析新能源汽车在三四线城市推广时可能遇到的主要阻碍和机会。",
				"deepthink":    false,
				"webSearch":    true,
			},
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create task status=%d body=%s", resp.StatusCode, readBody(resp))
	}
	_ = resp.Body.Close()

	task := waitTaskStatus(t, ctx, repo, batchNo, biz.TaskStatusSubmitted)
	syncResp := postJSON(t, gatewayBaseURL+"/api/v1/tasks/"+fmt.Sprint(task.ID)+"/sync-provider-result", "", map[string]any{
		"force":          true,
		"notifyCallback": true,
	})
	if syncResp.StatusCode != http.StatusOK {
		t.Fatalf("sync provider result status=%d body=%s", syncResp.StatusCode, readBody(syncResp))
	}
	var syncBody struct {
		Data struct {
			ProviderDone bool  `json:"providerDone"`
			FileCount    int32 `json:"fileCount"`
			RowCount     int32 `json:"rowCount"`
		} `json:"data"`
	}
	if err := json.NewDecoder(syncResp.Body).Decode(&syncBody); err != nil {
		t.Fatal(err)
	}
	_ = syncResp.Body.Close()
	if !syncBody.Data.ProviderDone || syncBody.Data.FileCount != 1 || syncBody.Data.RowCount != 54 {
		t.Fatalf("unexpected sync response: %+v", syncBody.Data)
	}
	updatedTask := waitTaskStatus(t, ctx, repo, batchNo, biz.TaskStatusResultImported)
	select {
	case <-callbackDone:
		t.Fatal("business callback delivered before provider callback")
	default:
	}

	mismatchedProviderCallbackResp := postJSON(t, gatewayBaseURL+"/external/callback/vendor_a", cfg.Provider["vendor_a"].Token, map[string]any{
		"batchNo":         batchNo,
		"externalBatchId": "vendor-batch-e2e",
		"status":          "DONE",
		"totalCount":      3,
		"successCount":    2,
		"failedCount":     0,
		"finishedAt":      time.Now().Format(time.RFC3339),
	})
	if mismatchedProviderCallbackResp.StatusCode != http.StatusOK {
		t.Fatalf("mismatched provider callback after sync status=%d body=%s", mismatchedProviderCallbackResp.StatusCode, readBody(mismatchedProviderCallbackResp))
	}
	_ = mismatchedProviderCallbackResp.Body.Close()
	updatedTask = waitTaskStatus(t, ctx, repo, batchNo, biz.TaskStatusResultImported)
	select {
	case <-callbackDone:
		t.Fatal("business callback delivered before callback counts matched total")
	case <-time.After(1200 * time.Millisecond):
	}

	providerCallbackResp := postJSON(t, gatewayBaseURL+"/external/callback/vendor_a", cfg.Provider["vendor_a"].Token, map[string]any{
		"batchNo":         batchNo,
		"externalBatchId": "vendor-batch-e2e",
		"status":          "DONE",
		"totalCount":      2,
		"successCount":    2,
		"failedCount":     0,
		"finishedAt":      time.Now().Format(time.RFC3339),
	})
	if providerCallbackResp.StatusCode != http.StatusOK {
		t.Fatalf("provider callback after sync status=%d body=%s", providerCallbackResp.StatusCode, readBody(providerCallbackResp))
	}
	_ = providerCallbackResp.Body.Close()
	select {
	case <-callbackDone:
	case <-ctx.Done():
		t.Fatal("timed out waiting for business callback after provider callback")
	}

	updatedTask = waitTaskStatus(t, ctx, repo, batchNo, biz.TaskStatusDone)
	assertRealWorkbookResult(t, db, updatedTask.ID)
	callbackMu.Lock()
	gotBatchNo := callbackPayload["batchNo"]
	callbackMu.Unlock()
	if gotBatchNo != batchNo {
		t.Fatalf("callback batchNo=%v, want %s", gotBatchNo, batchNo)
	}
}

func TestSyncProviderResultMarksProviderDoneFromStatusCounts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	cfg, err := config.Load(filepath.Join("..", "..", "configs", "cdp-router.conf"))
	if err != nil {
		t.Fatal(err)
	}
	ensureE2EProviderConfig(cfg)
	if err := data.EnsureDatabase(ctx, cfg.Postgres); err != nil {
		t.Fatal(err)
	}
	db, err := data.Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := data.Migrate(ctx, db, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}

	batchNo := fmt.Sprintf("Q2A2-status-counts-e2e-%d", time.Now().UnixNano())
	defer func() {
		cleanupTaskDataByBatchNo(t, db, batchNo)
	}()

	var callbackMu sync.Mutex
	var callbackPayload map[string]any
	callbackDone := make(chan struct{})
	businessCallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, _ := io.ReadAll(r.Body)
		callbackMu.Lock()
		_ = json.Unmarshal(body, &callbackPayload)
		callbackMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accepted":true}`))
		closeOnce(callbackDone)
	}))
	defer businessCallback.Close()

	var providerServerURL string
	const providerCallbackToken = "provider-token-status-counts-e2e"
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/provider/result.xlsx" && r.Header.Get("X-Provider-Token") != providerCallbackToken {
			http.Error(w, "bad provider token", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/provider/submit":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"externalBatchId":"vendor-batch-status-counts-e2e","acceptedCount":1}}`))
		case "/provider/status/vendor-batch-status-counts-e2e":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"message":"查询成功","code":200,"data":{"externalBatchId":"vendor-batch-status-counts-e2e","totalCount":1,"successCount":1,"failedCount":0,"questions":[]}}`))
		case "/provider/files/vendor-batch-status-counts-e2e":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"success":true,"message":"查询成功","code":200,"data":{"externalBatchId":"vendor-batch-status-counts-e2e","fileUrls":["%s/provider/result.xlsx"]}}`, providerServerURL)))
		case "/provider/result.xlsx":
			w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
			_, _ = w.Write(resultWorkbook(t))
		default:
			http.NotFound(w, r)
		}
	}))
	providerServerURL = providerServer.URL
	defer providerServer.Close()

	providerCfg := cfg.Provider["vendor_a"]
	providerCfg.SubmitURL = providerServer.URL + "/provider/submit"
	providerCfg.StatusURL = providerServer.URL + "/provider/status/{externalBatchId}"
	providerCfg.FilesURL = providerServer.URL + "/provider/files/{externalBatchId}"
	providerCfg.Token = providerCallbackToken
	providerCfg.APIKey = ""
	cfg.Provider = map[string]config.ProviderConfig{"vendor_a": providerCfg}
	setDefaultProviderCode(cfg, "vendor_a")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gatewayBaseURL := "http://" + listener.Addr().String()
	cfg.Server.PublicBaseURL = gatewayBaseURL

	store, err := ossutil.New(cfg.OSS)
	if err != nil {
		t.Fatal(err)
	}
	repo := data.NewTaskRepo(db)
	platformUsers := syncPlatformUsers(t, ctx, db, cfg)
	providers := provider.NewRegistry(cfg.Provider)
	usecase := biz.NewTaskUsecase(repo, providers, providers, store, cleaner.NewExcelParser(), callback.NewHTTPClient(10*time.Second), biz.TaskUsecaseOptions{
		PublicBaseURL:           cfg.Server.PublicBaseURL,
		OSSUploadDir:            cfg.OSS.UploadDir,
		OSSExpire:               time.Duration(cfg.OSS.ExpireSeconds) * time.Second,
		TempObjectRetentionDays: cfg.OSS.TempObjectRetentionDays,
		CallbackMaxTry:          cfg.Callback.MaxRetry,
	})
	server := httpapi.New(service.NewTaskService(usecase, providers, platformUsers, cfg.OSS.Bucket), nil)
	httpServer := &http.Server{Handler: server.Handler()}
	go func() { _ = httpServer.Serve(listener) }()
	defer func() {
		_ = httpServer.Shutdown(context.Background())
	}()

	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()
	go worker.New(usecase).Run(workerCtx)

	resp := postJSONWithAPIKey(t, gatewayBaseURL+"/api/v1/tasks", cfg.PlatformUsers["mainland"].APIKey, map[string]any{
		"batchNo":      batchNo,
		"providerCode": "vendor_a",
		"bizDate":      time.Now().Format("2006-01-02"),
		"callbackUrl":  businessCallback.URL,
		"items": []map[string]any{
			{
				"itemId":       "item-1",
				"platformCode": "yuanbao",
				"seq":          1,
				"question":     "请评价某产品的优缺点",
				"deepthink":    true,
				"webSearch":    true,
			},
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create task status=%d body=%s", resp.StatusCode, readBody(resp))
	}
	_ = resp.Body.Close()

	task := waitTaskStatus(t, ctx, repo, batchNo, biz.TaskStatusSubmitted)
	syncResp := postJSON(t, gatewayBaseURL+"/api/v1/tasks/"+fmt.Sprint(task.ID)+"/sync-provider-result", "", map[string]any{
		"force":          true,
		"notifyCallback": true,
	})
	if syncResp.StatusCode != http.StatusOK {
		t.Fatalf("sync provider result status=%d body=%s", syncResp.StatusCode, readBody(syncResp))
	}
	var syncBody struct {
		Data struct {
			TaskStatus   string `json:"taskStatus"`
			ProviderDone bool   `json:"providerDone"`
			FileCount    int32  `json:"fileCount"`
			RowCount     int32  `json:"rowCount"`
		} `json:"data"`
	}
	if err := json.NewDecoder(syncResp.Body).Decode(&syncBody); err != nil {
		t.Fatal(err)
	}
	_ = syncResp.Body.Close()
	if !syncBody.Data.ProviderDone || syncBody.Data.FileCount != 1 || syncBody.Data.RowCount == 0 {
		t.Fatalf("unexpected sync response: %+v", syncBody.Data)
	}

	select {
	case <-callbackDone:
	case <-ctx.Done():
		t.Fatal("timed out waiting for business callback after status count completion")
	}
	updatedTask := waitTaskStatus(t, ctx, repo, batchNo, biz.TaskStatusDone)
	assertResultRowsAndItemBackfill(t, db, updatedTask.ID)

	callbackMu.Lock()
	gotBatchNo := callbackPayload["batchNo"]
	callbackMu.Unlock()
	if gotBatchNo != batchNo {
		t.Fatalf("callback batchNo=%v, want %s", gotBatchNo, batchNo)
	}

	var providerCallbackTotal int
	if err := db.QueryRow(ctx, `
select nullif(provider_response #>> '{providerCallback,totalCount}', '')::int
from cdp_tasks
where id=$1
`, updatedTask.ID).Scan(&providerCallbackTotal); err != nil {
		t.Fatal(err)
	}
	if providerCallbackTotal != 1 {
		t.Fatalf("provider callback total=%d, want 1", providerCallbackTotal)
	}
}

func TestSyncProviderResultExplainsProcessingStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg, err := config.Load(filepath.Join("..", "..", "configs", "cdp-router.conf"))
	if err != nil {
		t.Fatal(err)
	}
	ensureE2EProviderConfig(cfg)
	if err := data.EnsureDatabase(ctx, cfg.Postgres); err != nil {
		t.Fatal(err)
	}
	db, err := data.Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := data.Migrate(ctx, db, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}

	batchNo := fmt.Sprintf("Q2A2-processing-e2e-%d", time.Now().UnixNano())
	defer func() {
		cleanupTaskDataByBatchNo(t, db, batchNo)
	}()
	const providerCode = "mpzdata"

	businessCallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("business callback should not be called while provider status is processing")
	}))
	defer businessCallback.Close()

	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Provider-Token"); got != cfg.Provider[providerCode].Token {
			http.Error(w, "bad provider token", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/provider/submit":
			_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"externalBatchId":"vendor-batch-processing-e2e","acceptedCount":2}}`))
		case "/provider/status/vendor-batch-processing-e2e":
			_, _ = w.Write([]byte(`{"success":true,"message":"查询成功","code":200,"data":{"externalBatchId":"vendor-batch-processing-e2e","questions":[{"questionId":"item-done","status":2},{"questionId":"item-processing","status":1}]}}`))
		case "/provider/files/vendor-batch-processing-e2e":
			_, _ = w.Write([]byte(`{"success":true,"message":"查询成功","code":200,"data":{"externalBatchId":"vendor-batch-processing-e2e","fileUrls":[]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer providerServer.Close()

	providerCfg := cfg.Provider[providerCode]
	providerCfg.SubmitURL = providerServer.URL + "/provider/submit"
	providerCfg.StatusURL = providerServer.URL + "/provider/status/{externalBatchId}"
	providerCfg.FilesURL = providerServer.URL + "/provider/files/{externalBatchId}"
	cfg.Provider = map[string]config.ProviderConfig{providerCode: providerCfg}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gatewayBaseURL := "http://" + listener.Addr().String()
	cfg.Server.PublicBaseURL = gatewayBaseURL

	store, err := ossutil.New(cfg.OSS)
	if err != nil {
		t.Fatal(err)
	}
	repo := data.NewTaskRepo(db)
	platformUsers := syncPlatformUsers(t, ctx, db, cfg)
	providers := provider.NewRegistry(cfg.Provider)
	usecase := biz.NewTaskUsecase(repo, providers, providers, store, cleaner.NewExcelParser(), callback.NewHTTPClient(10*time.Second), biz.TaskUsecaseOptions{
		PublicBaseURL:           cfg.Server.PublicBaseURL,
		OSSUploadDir:            cfg.OSS.UploadDir,
		OSSExpire:               time.Duration(cfg.OSS.ExpireSeconds) * time.Second,
		TempObjectRetentionDays: cfg.OSS.TempObjectRetentionDays,
		CallbackMaxTry:          cfg.Callback.MaxRetry,
	})
	server := httpapi.New(service.NewTaskService(usecase, providers, platformUsers, cfg.OSS.Bucket), nil)
	httpServer := &http.Server{Handler: server.Handler()}
	go func() { _ = httpServer.Serve(listener) }()
	defer func() {
		_ = httpServer.Shutdown(context.Background())
	}()

	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()
	go worker.New(usecase).Run(workerCtx)

	resp := postJSONWithAPIKey(t, gatewayBaseURL+"/api/v1/tasks", cfg.PlatformUsers["mainland"].APIKey, map[string]any{
		"batchNo":      batchNo,
		"providerCode": providerCode,
		"bizDate":      time.Now().Format("2006-01-02"),
		"callbackUrl":  businessCallback.URL,
		"items": []map[string]any{
			{
				"itemId":       "item-done",
				"platformCode": "doubao",
				"seq":          1,
				"question":     "请分析一款智能家居摄像头在家庭安防场景下的用户价值。",
				"deepthink":    false,
				"webSearch":    true,
			},
			{
				"itemId":       "item-processing",
				"platformCode": "qwen",
				"seq":          2,
				"question":     "请分析一款智能家居摄像头在家庭安防场景下的用户价值。",
				"deepthink":    false,
				"webSearch":    true,
			},
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create task status=%d body=%s", resp.StatusCode, readBody(resp))
	}
	_ = resp.Body.Close()

	task := waitTaskStatus(t, ctx, repo, batchNo, biz.TaskStatusSubmitted)
	syncResp := postJSON(t, gatewayBaseURL+"/api/v1/tasks/"+fmt.Sprint(task.ID)+"/sync-provider-result", "", map[string]any{
		"force":          true,
		"notifyCallback": true,
	})
	if syncResp.StatusCode != http.StatusOK {
		t.Fatalf("sync provider result status=%d body=%s", syncResp.StatusCode, readBody(syncResp))
	}
	var syncBody struct {
		Message string `json:"message"`
		Data    struct {
			ProviderDone bool   `json:"providerDone"`
			FileCount    int32  `json:"fileCount"`
			RowCount     int32  `json:"rowCount"`
			Msg          string `json:"msg"`
		} `json:"data"`
	}
	if err := json.NewDecoder(syncResp.Body).Decode(&syncBody); err != nil {
		t.Fatal(err)
	}
	_ = syncResp.Body.Close()
	if syncBody.Data.ProviderDone || syncBody.Data.FileCount != 0 || syncBody.Data.RowCount != 0 {
		t.Fatalf("unexpected sync response: %+v", syncBody.Data)
	}
	if syncBody.Message != syncBody.Data.Msg ||
		!strings.Contains(syncBody.Data.Msg, "mpzdata") ||
		!strings.Contains(syncBody.Data.Msg, "已处理 1/2") ||
		!strings.Contains(syncBody.Data.Msg, "结果文件未生成") {
		t.Fatalf("message=%q does not explain processing status", syncBody.Message)
	}

	filesResp := get(t, gatewayBaseURL+"/api/v1/tasks/"+fmt.Sprint(task.ID)+"/provider-result-files")
	if filesResp.StatusCode != http.StatusOK {
		t.Fatalf("provider result files status=%d body=%s", filesResp.StatusCode, readBody(filesResp))
	}
	var filesBody struct {
		Message string `json:"message"`
		Data    struct {
			FileURLs []string `json:"fileUrls"`
		} `json:"data"`
	}
	if err := json.NewDecoder(filesResp.Body).Decode(&filesBody); err != nil {
		t.Fatal(err)
	}
	_ = filesResp.Body.Close()
	if len(filesBody.Data.FileURLs) != 0 || !strings.Contains(filesBody.Message, "未查询到结果文件") {
		t.Fatalf("unexpected provider files response: message=%q data=%+v", filesBody.Message, filesBody.Data)
	}

	var resultFileCount int
	if err := db.QueryRow(ctx, `select count(*) from cdp_task_result_files where task_id=$1`, task.ID).Scan(&resultFileCount); err != nil {
		t.Fatal(err)
	}
	if resultFileCount != 0 {
		t.Fatalf("result file count=%d, want 0", resultFileCount)
	}
}

func TestMpzdataCallbackReconcilesProviderFilesWithRealPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	cfg, err := config.Load(filepath.Join("..", "..", "configs", "cdp-router.conf"))
	if err != nil {
		t.Fatal(err)
	}
	ensureE2EProviderConfig(cfg)
	if err := data.EnsureDatabase(ctx, cfg.Postgres); err != nil {
		t.Fatal(err)
	}
	db, err := data.Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := data.Migrate(ctx, db, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}

	batchNo := fmt.Sprintf("Q2A2-mpz-callback-e2e-%d", time.Now().UnixNano())
	defer cleanupTaskDataByBatchNo(t, db, batchNo)

	const providerCode = "mpzdata"
	callbackDone := make(chan struct{})
	businessCallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accepted":true}`))
		closeOnce(callbackDone)
	}))
	defer businessCallback.Close()

	var providerServerURL string
	var resultDownloadCount int32
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Provider-Token"); got != cfg.Provider[providerCode].Token && !strings.HasPrefix(r.URL.Path, "/provider/result-") {
			http.Error(w, "bad provider token", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/provider/submit":
			_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"externalBatchId":"vendor-batch-mpz-callback-e2e","acceptedCount":2}}`))
		case "/provider/files/vendor-batch-mpz-callback-e2e":
			_, _ = w.Write([]byte(fmt.Sprintf(`{"success":true,"message":"查询成功","code":200,"data":{"externalBatchId":"vendor-batch-mpz-callback-e2e","fileUrls":["%s/provider/result-a.xlsx","%s/provider/result-b.xlsx"]}}`, providerServerURL, providerServerURL)))
		case "/provider/result-a.xlsx":
			atomic.AddInt32(&resultDownloadCount, 1)
			w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
			_, _ = w.Write(resultWorkbookForItem(t, "item-1"))
		case "/provider/result-b.xlsx":
			atomic.AddInt32(&resultDownloadCount, 1)
			w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
			_, _ = w.Write(resultWorkbookForItem(t, "item-2"))
		default:
			http.NotFound(w, r)
		}
	}))
	providerServerURL = providerServer.URL
	defer providerServer.Close()

	providerCfg := cfg.Provider[providerCode]
	providerCfg.SubmitURL = providerServer.URL + "/provider/submit"
	providerCfg.FilesURL = providerServer.URL + "/provider/files/{externalBatchId}"
	cfg.Provider = map[string]config.ProviderConfig{providerCode: providerCfg}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gatewayBaseURL := "http://" + listener.Addr().String()
	cfg.Server.PublicBaseURL = gatewayBaseURL

	store, err := ossutil.New(cfg.OSS)
	if err != nil {
		t.Fatal(err)
	}
	repo := data.NewTaskRepo(db)
	platformUsers := syncPlatformUsers(t, ctx, db, cfg)
	providers := provider.NewRegistry(cfg.Provider)
	usecase := biz.NewTaskUsecase(repo, providers, providers, store, cleaner.NewExcelParser(), callback.NewHTTPClient(10*time.Second), biz.TaskUsecaseOptions{
		PublicBaseURL:           cfg.Server.PublicBaseURL,
		OSSUploadDir:            cfg.OSS.UploadDir,
		OSSExpire:               time.Duration(cfg.OSS.ExpireSeconds) * time.Second,
		TempObjectRetentionDays: cfg.OSS.TempObjectRetentionDays,
		CallbackMaxTry:          cfg.Callback.MaxRetry,
	})
	server := httpapi.New(service.NewTaskService(usecase, providers, platformUsers, cfg.OSS.Bucket), nil)
	httpServer := &http.Server{Handler: server.Handler()}
	go func() { _ = httpServer.Serve(listener) }()
	defer func() { _ = httpServer.Shutdown(context.Background()) }()

	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()
	go worker.New(usecase).Run(workerCtx)

	resp := postJSONWithAPIKey(t, gatewayBaseURL+"/api/v1/tasks", cfg.PlatformUsers["mainland"].APIKey, map[string]any{
		"batchNo":      batchNo,
		"providerCode": providerCode,
		"bizDate":      time.Now().Format("2006-01-02"),
		"callbackUrl":  businessCallback.URL,
		"items": []map[string]any{
			{"itemId": "item-1", "platformCode": "doubao", "seq": 1, "question": "测试 mpzdata 回调文件校对", "deepthink": false, "webSearch": true},
			{"itemId": "item-2", "platformCode": "qwen", "seq": 2, "question": "测试 mpzdata 多文件处理", "deepthink": false, "webSearch": true},
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create task status=%d body=%s", resp.StatusCode, readBody(resp))
	}
	_ = resp.Body.Close()

	task := waitTaskStatus(t, ctx, repo, batchNo, biz.TaskStatusSubmitted)
	failedCallbackResp := postJSON(t, gatewayBaseURL+"/external/callback/"+providerCode, cfg.Provider[providerCode].Token, map[string]any{
		"providerCode":    "vendor_a",
		"batchNo":         batchNo,
		"externalBatchId": "vendor-batch-mpz-callback-e2e",
		"status":          "DONE",
		"totalCount":      2,
		"successCount":    2,
		"failedCount":     0,
	})
	if failedCallbackResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("failed provider callback status=%d body=%s", failedCallbackResp.StatusCode, readBody(failedCallbackResp))
	}
	_ = failedCallbackResp.Body.Close()

	callbackResp := postJSON(t, gatewayBaseURL+"/external/callback/"+providerCode, cfg.Provider[providerCode].Token, map[string]any{
		"batchNo":         batchNo,
		"externalBatchId": "vendor-batch-mpz-callback-e2e",
		"status":          "DONE",
		"totalCount":      2,
		"successCount":    2,
		"failedCount":     0,
		"finishedAt":      time.Now().Format(time.RFC3339),
	})
	if callbackResp.StatusCode != http.StatusOK {
		t.Fatalf("provider callback status=%d body=%s", callbackResp.StatusCode, readBody(callbackResp))
	}
	_ = callbackResp.Body.Close()

	select {
	case <-callbackDone:
	case <-ctx.Done():
		t.Fatal("timed out waiting for business callback")
	}
	task = waitTaskStatus(t, ctx, repo, batchNo, biz.TaskStatusDone)

	var resultFileCount int
	if err := db.QueryRow(ctx, `select count(*) from cdp_task_result_files where task_id=$1 and source_type='PROVIDER_FILE' and parse_status='SUCCESS'`, task.ID).Scan(&resultFileCount); err != nil {
		t.Fatal(err)
	}
	if resultFileCount != 2 {
		t.Fatalf("provider result file count=%d, want 2", resultFileCount)
	}
	if got := atomic.LoadInt32(&resultDownloadCount); got != 2 {
		t.Fatalf("result download count=%d, want 2", got)
	}
	refetchResp := postJSON(t, gatewayBaseURL+"/api/v1/tasks/"+fmt.Sprint(task.ID)+"/refetch-result", "", map[string]any{
		"force":          true,
		"notifyCallback": true,
	})
	if refetchResp.StatusCode != http.StatusOK {
		t.Fatalf("refetch task result status=%d body=%s", refetchResp.StatusCode, readBody(refetchResp))
	}
	_ = refetchResp.Body.Close()
	if got := atomic.LoadInt32(&resultDownloadCount); got != 2 {
		t.Fatalf("result download count after refetch=%d, want 2", got)
	}
	if err := db.QueryRow(ctx, `select count(*) from cdp_task_result_files where task_id=$1 and parse_status='SUCCESS'`, task.ID).Scan(&resultFileCount); err != nil {
		t.Fatal(err)
	}
	if resultFileCount != 2 {
		t.Fatalf("result file count after refetch=%d, want 2", resultFileCount)
	}
	var reconcileEventCount int
	if err := db.QueryRow(ctx, `select count(*) from cdp_task_events where task_id=$1 and event_type=$2 and payload->>'triggerType'='PROVIDER_CALLBACK_RECONCILE'`, task.ID, string(biz.EventProviderFilesFetched)).Scan(&reconcileEventCount); err != nil {
		t.Fatal(err)
	}
	if reconcileEventCount == 0 {
		t.Fatal("expected provider callback reconcile event")
	}
	assertTaskEventCount(t, db, task.ID, biz.EventProviderCallbackRecv, 2)
	assertTaskEventCount(t, db, task.ID, biz.EventProviderCallbackFail, 1)
	assertTaskEventCount(t, db, task.ID, biz.EventProviderCallback, 1)
	assertResultRowsAndItemBackfill(t, db, task.ID)
}

func TestSubmittedTaskMaintenanceWithRealPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg, err := config.Load(filepath.Join("..", "..", "configs", "cdp-router.conf"))
	if err != nil {
		t.Fatal(err)
	}
	ensureE2EProviderConfig(cfg)
	if err := data.EnsureDatabase(ctx, cfg.Postgres); err != nil {
		t.Fatal(err)
	}
	db, err := data.Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := data.Migrate(ctx, db, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}

	timeoutBatchNo := fmt.Sprintf("Q2A2-timeout-e2e-%d", time.Now().UnixNano())
	progressBatchNo := fmt.Sprintf("Q2A2-progress-e2e-%d", time.Now().UnixNano())
	defer cleanupTaskDataByBatchNo(t, db, timeoutBatchNo)
	defer cleanupTaskDataByBatchNo(t, db, progressBatchNo)

	const providerCode = "vendor_a"
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Provider-Token"); got != cfg.Provider[providerCode].Token {
			http.Error(w, "bad provider token", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/provider/status/progress-external-e2e":
			_, _ = w.Write([]byte(`{"success":true,"message":"查询成功","code":200,"data":{"externalBatchId":"progress-external-e2e","questions":[{"questionId":"progress-item-1","status":2},{"questionId":"progress-item-2","status":1}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer providerServer.Close()
	providerCfg := cfg.Provider[providerCode]
	providerCfg.StatusURL = providerServer.URL + "/provider/status/{externalBatchId}"
	cfg.Provider = map[string]config.ProviderConfig{providerCode: providerCfg}

	repo := data.NewTaskRepo(db)
	providers := provider.NewRegistry(cfg.Provider)
	usecase := biz.NewTaskUsecase(repo, providers, providers, nil, nil, nil, biz.TaskUsecaseOptions{})

	timeoutTask := createSubmittedTaskForMaintenance(t, ctx, repo, db, timeoutBatchNo, providerCode, "timeout-external-e2e", []biz.TaskItem{
		{ItemID: "timeout-item-1", PlatformCode: "doubao", Seq: 1, Question: "超时任务测试"},
	}, 25*time.Hour)
	progressTask := createSubmittedTaskForMaintenance(t, ctx, repo, db, progressBatchNo, providerCode, "progress-external-e2e", []biz.TaskItem{
		{ItemID: "progress-item-1", PlatformCode: "doubao", Seq: 1, Question: "进度任务测试 1"},
		{ItemID: "progress-item-2", PlatformCode: "qwen", Seq: 2, Question: "进度任务测试 2"},
	}, 16*time.Minute)

	timedOut, err := usecase.FailTimedOutSubmitted(ctx, 24*time.Hour, 10)
	if err != nil {
		t.Fatal(err)
	}
	if timedOut == 0 {
		t.Fatal("expected timed out submitted task")
	}
	updatedTimeoutTask, err := repo.GetTask(ctx, timeoutTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedTimeoutTask.Status != biz.TaskStatusFailed || updatedTimeoutTask.LastError != "执行超过24小时" {
		t.Fatalf("timeout task status=%s last_error=%q", updatedTimeoutTask.Status, updatedTimeoutTask.LastError)
	}
	var timeoutEventCount int
	if err := db.QueryRow(ctx, `select count(*) from cdp_task_events where task_id=$1 and event_type=$2`, timeoutTask.ID, string(biz.EventTaskTimedOut)).Scan(&timeoutEventCount); err != nil {
		t.Fatal(err)
	}
	if timeoutEventCount == 0 {
		t.Fatal("expected timeout task event")
	}

	refreshed, err := usecase.RefreshSubmittedProgress(ctx, 15*time.Minute, 10)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed == 0 {
		t.Fatal("expected submitted task progress refresh")
	}
	updatedProgressTask, err := repo.GetTask(ctx, progressTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedProgressTask.SuccessCount != 1 || updatedProgressTask.TotalCount != 2 {
		t.Fatalf("progress task success=%d total=%d", updatedProgressTask.SuccessCount, updatedProgressTask.TotalCount)
	}
	var doneStatus, processingStatus int
	if err := db.QueryRow(ctx, `select status from cdp_task_items where task_id=$1 and item_id='progress-item-1'`, progressTask.ID).Scan(&doneStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `select status from cdp_task_items where task_id=$1 and item_id='progress-item-2'`, progressTask.ID).Scan(&processingStatus); err != nil {
		t.Fatal(err)
	}
	if doneStatus != 2 || processingStatus != 1 {
		t.Fatalf("progress item statuses=%d,%d", doneStatus, processingStatus)
	}
}

func TestAdminTaskAPIsWithRealPostgresAndRedis(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	cfg, err := config.Load(filepath.Join("..", "..", "configs", "cdp-router.conf"))
	if err != nil {
		t.Fatal(err)
	}
	ensureE2EProviderConfig(cfg)
	cfg.Redis.Enabled = true
	if err := data.EnsureDatabase(ctx, cfg.Postgres); err != nil {
		t.Fatal(err)
	}
	db, err := data.Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := data.Migrate(ctx, db, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	redisClient, cleanupRedis := openRealRedisForTest(t, ctx, cfg)
	defer cleanupRedis()
	defer redisClient.Close()

	batchNo := fmt.Sprintf("Q2A2-admin-e2e-%d", time.Now().UnixNano())
	defer func() {
		cleanupTaskDataByBatchNo(t, db, batchNo)
		_ = redisClient.Del(context.Background(), cfg.Admin.Session.RedisKeyPrefix+"*").Err()
	}()

	realWorkbook, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "AI数据接入文档", "8ce0e57e-数据采集_2026-07-23T18-09-59.xlsx"))
	if err != nil {
		t.Fatal(err)
	}

	var callbackMu sync.Mutex
	var callbackPayload map[string]any
	callbackDone := make(chan struct{})
	businessCallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, _ := io.ReadAll(r.Body)
		callbackMu.Lock()
		_ = json.Unmarshal(body, &callbackPayload)
		callbackMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accepted":true}`))
		closeOnce(callbackDone)
	}))
	defer businessCallback.Close()

	var providerServerURL string
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Provider-Token"); got != cfg.Provider["vendor_a"].Token && r.URL.Path != "/provider/result-real.xlsx" {
			http.Error(w, "bad provider token", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/provider/submit":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"externalBatchId":"vendor-batch-admin-e2e","acceptedCount":2}}`))
		case "/provider/status/vendor-batch-admin-e2e":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"message":"查询成功","code":200,"data":{"externalBatchId":"vendor-batch-admin-e2e","questions":[{"questionId":"MPZ-REAL-20260723180500-doubao-001","status":2},{"questionId":"MPZ-REAL-20260723180500-qwen-002","status":2}]}}`))
		case "/provider/files/vendor-batch-admin-e2e":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"success":true,"message":"查询成功","code":200,"data":{"externalBatchId":"vendor-batch-admin-e2e","fileUrls":["%s/provider/result-real.xlsx"]}}`, providerServerURL)))
		case "/provider/result-real.xlsx":
			w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
			_, _ = w.Write(realWorkbook)
		default:
			http.NotFound(w, r)
		}
	}))
	providerServerURL = providerServer.URL
	defer providerServer.Close()

	providerCfg := cfg.Provider["vendor_a"]
	providerCfg.SubmitURL = providerServer.URL + "/provider/submit"
	providerCfg.StatusURL = providerServer.URL + "/provider/status/{externalBatchId}"
	providerCfg.FilesURL = providerServer.URL + "/provider/files/{externalBatchId}"
	cfg.Provider = map[string]config.ProviderConfig{"vendor_a": providerCfg}
	setDefaultProviderCode(cfg, "vendor_a")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gatewayBaseURL := "http://" + listener.Addr().String()
	cfg.Server.PublicBaseURL = gatewayBaseURL

	store, err := ossutil.New(cfg.OSS)
	if err != nil {
		t.Fatal(err)
	}
	repo := data.NewTaskRepo(db)
	platformUsers := syncPlatformUsers(t, ctx, db, cfg)
	providers := provider.NewRegistry(cfg.Provider)
	usecase := biz.NewTaskUsecase(repo, providers, providers, store, cleaner.NewExcelParser(), callback.NewHTTPClient(10*time.Second), biz.TaskUsecaseOptions{
		PublicBaseURL:           cfg.Server.PublicBaseURL,
		OSSUploadDir:            cfg.OSS.UploadDir,
		OSSExpire:               time.Duration(cfg.OSS.ExpireSeconds) * time.Second,
		TempObjectRetentionDays: cfg.OSS.TempObjectRetentionDays,
		CallbackMaxTry:          cfg.Callback.MaxRetry,
	})
	adminService := service.NewAdminService(cfg.Admin, redisClient, data.NewAdminRepo(db), usecase, cfg.Provider)
	adminService.SetOSSPublicHost(cfg.OSS.PublicHost)
	server := httpapi.New(service.NewTaskService(usecase, providers, platformUsers, cfg.OSS.Bucket), adminService)
	httpServer := &http.Server{Handler: server.Handler()}
	go func() { _ = httpServer.Serve(listener) }()
	defer func() {
		_ = httpServer.Shutdown(context.Background())
	}()

	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()
	go worker.New(usecase).Run(workerCtx)

	auth := adminLogin(t, gatewayBaseURL, cfg)
	providerListResp := adminGet(t, gatewayBaseURL+"/api/admin/v1/providers", auth)
	if providerListResp.StatusCode != http.StatusOK {
		t.Fatalf("admin providers status=%d body=%s", providerListResp.StatusCode, readBody(providerListResp))
	}
	var providerListBody struct {
		Data struct {
			Providers []struct {
				ProviderCode        string `json:"providerCode"`
				DisplayName         string `json:"displayName"`
				Enabled             bool   `json:"enabled"`
				SubmitURLConfigured bool   `json:"submitUrlConfigured"`
			} `json:"providers"`
		} `json:"data"`
	}
	if err := json.NewDecoder(providerListResp.Body).Decode(&providerListBody); err != nil {
		t.Fatal(err)
	}
	_ = providerListResp.Body.Close()
	if len(providerListBody.Data.Providers) != 1 ||
		providerListBody.Data.Providers[0].ProviderCode != "vendor_a" ||
		providerListBody.Data.Providers[0].DisplayName == "" ||
		!providerListBody.Data.Providers[0].Enabled ||
		!providerListBody.Data.Providers[0].SubmitURLConfigured {
		t.Fatalf("unexpected provider list body: %+v", providerListBody.Data.Providers)
	}
	metricsFrom := time.Now().Add(-1 * time.Second)
	createResp := postJSONWithAPIKey(t, gatewayBaseURL+"/api/v1/tasks", cfg.PlatformUsers["mainland"].APIKey, map[string]any{
		"batchNo":      batchNo,
		"providerCode": "vendor_a",
		"source":       "admin-e2e-source",
		"bizDate":      time.Now().Format("2006-01-02"),
		"callbackUrl":  businessCallback.URL,
		"items": []map[string]any{
			{
				"itemId":       "MPZ-REAL-20260723180500-doubao-001",
				"platformCode": "doubao",
				"seq":          1,
				"question":     "请从用户体验、性价比和售后服务三个角度评价一款智能家居摄像头的优缺点，并给出购买建议。",
				"deepthink":    true,
				"webSearch":    true,
			},
			{
				"itemId":       "MPZ-REAL-20260723180500-qwen-002",
				"platformCode": "qwen",
				"seq":          2,
				"question":     "请分析新能源汽车在三四线城市推广时可能遇到的主要阻碍和机会。",
				"deepthink":    false,
				"webSearch":    true,
			},
		},
	})
	if createResp.StatusCode != http.StatusOK {
		t.Fatalf("create task status=%d body=%s", createResp.StatusCode, readBody(createResp))
	}
	_ = createResp.Body.Close()

	task := waitTaskStatus(t, ctx, repo, batchNo, biz.TaskStatusSubmitted)
	var storedSource string
	if err := db.QueryRow(ctx, `select coalesce(source,'') from cdp_tasks where id=$1`, task.ID).Scan(&storedSource); err != nil {
		t.Fatal(err)
	}
	if storedSource != "admin-e2e-source" {
		t.Fatalf("source=%q, want admin-e2e-source", storedSource)
	}

	listResp := adminGet(t, gatewayBaseURL+"/api/admin/v1/tasks?batchNo="+batchNo+"&source=admin-e2e-source&resultStatus=PROCESSING&page=1&pageSize=10", auth)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("admin list status=%d body=%s", listResp.StatusCode, readBody(listResp))
	}
	var listBody struct {
		Data struct {
			Total int `json:"total"`
			Tasks []struct {
				TaskName                 string  `json:"taskName"`
				ExternalBatchID          string  `json:"externalBatchId"`
				Source                   string  `json:"source"`
				QuestionCount            int     `json:"questionCount"`
				Progress                 float64 `json:"progress"`
				ExecutionDurationSeconds int32   `json:"executionDurationSeconds"`
				ExecutionDurationText    string  `json:"executionDurationText"`
			} `json:"tasks"`
		} `json:"data"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listBody); err != nil {
		t.Fatal(err)
	}
	_ = listResp.Body.Close()
	if listBody.Data.Total != 1 ||
		listBody.Data.Tasks[0].TaskName != batchNo ||
		!strings.HasPrefix(listBody.Data.Tasks[0].ExternalBatchID, "job_") ||
		listBody.Data.Tasks[0].Source != "admin-e2e-source" ||
		listBody.Data.Tasks[0].QuestionCount != 2 {
		t.Fatalf("unexpected admin list body: %+v", listBody.Data)
	}
	if listBody.Data.Tasks[0].ExecutionDurationSeconds < 0 || !strings.HasPrefix(listBody.Data.Tasks[0].ExecutionDurationText, "已执行 ") {
		t.Fatalf("unexpected list execution duration: %+v", listBody.Data.Tasks[0])
	}
	platformFilteredResp := adminGet(t, gatewayBaseURL+"/api/admin/v1/tasks?batchNo="+batchNo+"&platformCode=mainland&page=1&pageSize=10", auth)
	if platformFilteredResp.StatusCode != http.StatusOK {
		t.Fatalf("admin platform filtered list status=%d body=%s", platformFilteredResp.StatusCode, readBody(platformFilteredResp))
	}
	var platformFilteredBody struct {
		Data struct {
			Total int `json:"total"`
			Tasks []struct {
				PlatformCode string `json:"platformCode"`
			} `json:"tasks"`
		} `json:"data"`
	}
	if err := json.NewDecoder(platformFilteredResp.Body).Decode(&platformFilteredBody); err != nil {
		t.Fatal(err)
	}
	_ = platformFilteredResp.Body.Close()
	if platformFilteredBody.Data.Total != 1 || platformFilteredBody.Data.Tasks[0].PlatformCode != "mainland" {
		t.Fatalf("unexpected platform filtered list body: %+v", platformFilteredBody.Data)
	}

	platformCode := fmt.Sprintf("e2e-platform-%d", time.Now().UnixNano())
	defer cleanupPlatformUser(t, db, platformCode)
	createPlatformResp := adminPostJSON(t, gatewayBaseURL+"/api/admin/v1/platform-users", auth, map[string]any{
		"platformCode":        platformCode,
		"name":                "E2E 接入方",
		"status":              "ACTIVE",
		"defaultProviderCode": "vendor_a",
		"callbackMode":        "DEFAULT_HTTP",
		"callbackUrl":         businessCallback.URL,
	})
	if createPlatformResp.StatusCode != http.StatusOK {
		t.Fatalf("create platform user status=%d body=%s", createPlatformResp.StatusCode, readBody(createPlatformResp))
	}
	var createPlatformBody struct {
		Data struct {
			ID                  string `json:"id"`
			PlatformCode        string `json:"platformCode"`
			APIKey              string `json:"apiKey"`
			CallbackMode        string `json:"callbackMode"`
			DefaultProviderCode string `json:"defaultProviderCode"`
		} `json:"data"`
	}
	if err := json.NewDecoder(createPlatformResp.Body).Decode(&createPlatformBody); err != nil {
		t.Fatal(err)
	}
	_ = createPlatformResp.Body.Close()
	if createPlatformBody.Data.ID == "" || createPlatformBody.Data.PlatformCode != platformCode ||
		!strings.HasPrefix(createPlatformBody.Data.APIKey, "mck_") ||
		createPlatformBody.Data.CallbackMode != "DEFAULT_HTTP" ||
		createPlatformBody.Data.DefaultProviderCode != "vendor_a" {
		t.Fatalf("unexpected create platform user body: %+v", createPlatformBody.Data)
	}
	updatePlatformResp := adminPutJSON(t, gatewayBaseURL+"/api/admin/v1/platform-users/"+createPlatformBody.Data.ID, auth, map[string]any{
		"platformCode":        platformCode,
		"name":                "E2E 接入方更新",
		"status":              "DISABLED",
		"defaultProviderCode": "vendor_a",
		"callbackMode":        "CUSTOM",
		"callbackUrl":         businessCallback.URL + "/custom",
	})
	if updatePlatformResp.StatusCode != http.StatusOK {
		t.Fatalf("update platform user status=%d body=%s", updatePlatformResp.StatusCode, readBody(updatePlatformResp))
	}
	var updatePlatformBody struct {
		Data struct {
			APIKey       string `json:"apiKey"`
			Status       string `json:"status"`
			CallbackMode string `json:"callbackMode"`
			CallbackURL  string `json:"callbackUrl"`
		} `json:"data"`
	}
	if err := json.NewDecoder(updatePlatformResp.Body).Decode(&updatePlatformBody); err != nil {
		t.Fatal(err)
	}
	_ = updatePlatformResp.Body.Close()
	if updatePlatformBody.Data.APIKey != createPlatformBody.Data.APIKey ||
		updatePlatformBody.Data.Status != "DISABLED" ||
		updatePlatformBody.Data.CallbackMode != "CUSTOM" ||
		!strings.HasSuffix(updatePlatformBody.Data.CallbackURL, "/custom") {
		t.Fatalf("unexpected update platform user body: %+v", updatePlatformBody.Data)
	}
	listPlatformResp := adminGet(t, gatewayBaseURL+"/api/admin/v1/platform-users?keyword="+platformCode+"&status=DISABLED&page=1&pageSize=10", auth)
	if listPlatformResp.StatusCode != http.StatusOK {
		t.Fatalf("list platform users status=%d body=%s", listPlatformResp.StatusCode, readBody(listPlatformResp))
	}
	var listPlatformBody struct {
		Data struct {
			Total int `json:"total"`
			Users []struct {
				PlatformCode string `json:"platformCode"`
			} `json:"users"`
		} `json:"data"`
	}
	if err := json.NewDecoder(listPlatformResp.Body).Decode(&listPlatformBody); err != nil {
		t.Fatal(err)
	}
	_ = listPlatformResp.Body.Close()
	if listPlatformBody.Data.Total != 1 || listPlatformBody.Data.Users[0].PlatformCode != platformCode {
		t.Fatalf("unexpected list platform user body: %+v", listPlatformBody.Data)
	}

	checkResp := adminPostJSON(t, gatewayBaseURL+"/api/admin/v1/tasks/"+fmt.Sprint(task.ID)+"/check-result", auth, map[string]any{})
	if checkResp.StatusCode != http.StatusOK {
		t.Fatalf("admin check result status=%d body=%s", checkResp.StatusCode, readBody(checkResp))
	}
	var checkBody struct {
		Message string `json:"message"`
		Data    struct {
			ProviderDone bool   `json:"providerDone"`
			FileCount    int32  `json:"fileCount"`
			RowCount     int32  `json:"rowCount"`
			Msg          string `json:"msg"`
		} `json:"data"`
	}
	if err := json.NewDecoder(checkResp.Body).Decode(&checkBody); err != nil {
		t.Fatal(err)
	}
	_ = checkResp.Body.Close()
	if !checkBody.Data.ProviderDone || checkBody.Data.FileCount != 1 || checkBody.Data.RowCount != 54 {
		t.Fatalf("unexpected admin check result body: %+v", checkBody.Data)
	}
	if checkBody.Message != checkBody.Data.Msg || !strings.Contains(checkBody.Data.Msg, "已处理 2/2") || !strings.Contains(checkBody.Data.Msg, "结果文件已生成 1 个") {
		t.Fatalf("unexpected admin check result msg: message=%q dataMsg=%q", checkBody.Message, checkBody.Data.Msg)
	}
	updatedTask := waitTaskStatus(t, ctx, repo, batchNo, biz.TaskStatusResultImported)
	select {
	case <-callbackDone:
		t.Fatal("business callback delivered before provider callback after admin check result")
	default:
	}

	providerCallbackResp := postJSON(t, gatewayBaseURL+"/external/callback/vendor_a", cfg.Provider["vendor_a"].Token, map[string]any{
		"batchNo":         batchNo,
		"externalBatchId": "vendor-batch-admin-e2e",
		"status":          "DONE",
		"totalCount":      2,
		"successCount":    2,
		"failedCount":     0,
		"finishedAt":      time.Now().Format(time.RFC3339),
	})
	if providerCallbackResp.StatusCode != http.StatusOK {
		t.Fatalf("provider callback after admin check result status=%d body=%s", providerCallbackResp.StatusCode, readBody(providerCallbackResp))
	}
	_ = providerCallbackResp.Body.Close()
	select {
	case <-callbackDone:
	case <-ctx.Done():
		t.Fatal("timed out waiting for business callback after provider callback")
	}
	updatedTask = waitTaskStatus(t, ctx, repo, batchNo, biz.TaskStatusDone)
	const adminResultObjectKey = "cdp-router/e2e/admin-detail-result.xlsx"
	if _, err := db.Exec(ctx, `
update cdp_task_result_files
set source_type='OSS_UPLOAD', object_key=$2, file_url=null, updated_at=now()
where task_id=$1
`, updatedTask.ID, adminResultObjectKey); err != nil {
		t.Fatal(err)
	}

	detailResp := adminGet(t, gatewayBaseURL+"/api/admin/v1/tasks/"+fmt.Sprint(updatedTask.ID)+"?itemPage=1&itemPageSize=1", auth)
	if detailResp.StatusCode != http.StatusOK {
		t.Fatalf("admin detail status=%d body=%s", detailResp.StatusCode, readBody(detailResp))
	}
	var detailBody struct {
		Data struct {
			Task struct {
				ExternalBatchID          string `json:"externalBatchId"`
				ExecutionDurationSeconds int32  `json:"executionDurationSeconds"`
				ExecutionDurationText    string `json:"executionDurationText"`
			} `json:"task"`
			Items []struct {
				ItemID         string `json:"itemId"`
				AnswerText     string `json:"answerText"`
				ReferenceCount int    `json:"referenceCount"`
				ResultStatus   string `json:"resultStatus"`
			} `json:"items"`
			Files []struct {
				ObjectKey string `json:"objectKey"`
				FileURL   string `json:"fileUrl"`
			} `json:"files"`
			Callbacks []struct {
				TargetURL      string `json:"targetUrl"`
				ResponseStatus int    `json:"responseStatus"`
				ResponseBody   string `json:"responseBody"`
				Status         string `json:"status"`
				LastError      string `json:"lastError"`
			} `json:"callbacks"`
			ItemTotal    int `json:"itemTotal"`
			ItemPage     int `json:"itemPage"`
			ItemPageSize int `json:"itemPageSize"`
		} `json:"data"`
	}
	if err := json.NewDecoder(detailResp.Body).Decode(&detailBody); err != nil {
		t.Fatal(err)
	}
	_ = detailResp.Body.Close()
	if len(detailBody.Data.Items) != 1 || detailBody.Data.ItemTotal != 2 || detailBody.Data.ItemPage != 1 || detailBody.Data.ItemPageSize != 1 || len(detailBody.Data.Files) != 1 {
		t.Fatalf("unexpected detail counts: %+v", detailBody.Data)
	}
	wantResultURL := strings.TrimRight(cfg.OSS.PublicHost, "/") + "/" + adminResultObjectKey
	if detailBody.Data.Files[0].ObjectKey != adminResultObjectKey || detailBody.Data.Files[0].FileURL != wantResultURL {
		t.Fatalf("unexpected detail file url: %+v want=%s", detailBody.Data.Files[0], wantResultURL)
	}
	detailPage2Resp := adminGet(t, gatewayBaseURL+"/api/admin/v1/tasks/"+fmt.Sprint(updatedTask.ID)+"?itemPage=2&itemPageSize=1", auth)
	if detailPage2Resp.StatusCode != http.StatusOK {
		t.Fatalf("admin detail page2 status=%d body=%s", detailPage2Resp.StatusCode, readBody(detailPage2Resp))
	}
	var detailPage2Body struct {
		Data struct {
			Items []struct {
				ItemID         string `json:"itemId"`
				AnswerText     string `json:"answerText"`
				ReferenceCount int    `json:"referenceCount"`
				ResultStatus   string `json:"resultStatus"`
			} `json:"items"`
			ItemTotal    int `json:"itemTotal"`
			ItemPage     int `json:"itemPage"`
			ItemPageSize int `json:"itemPageSize"`
		} `json:"data"`
	}
	if err := json.NewDecoder(detailPage2Resp.Body).Decode(&detailPage2Body); err != nil {
		t.Fatal(err)
	}
	_ = detailPage2Resp.Body.Close()
	if len(detailPage2Body.Data.Items) != 1 || detailPage2Body.Data.ItemTotal != 2 || detailPage2Body.Data.ItemPage != 2 || detailPage2Body.Data.ItemPageSize != 1 {
		t.Fatalf("unexpected detail page2 pagination: %+v", detailPage2Body.Data)
	}
	if len(detailBody.Data.Callbacks) != 1 {
		t.Fatalf("detail callback count=%d, want 1", len(detailBody.Data.Callbacks))
	}
	if detailBody.Data.Callbacks[0].TargetURL != businessCallback.URL ||
		detailBody.Data.Callbacks[0].ResponseStatus != http.StatusOK ||
		detailBody.Data.Callbacks[0].ResponseBody != `{"accepted":true}` ||
		detailBody.Data.Callbacks[0].Status != string(biz.TaskStatusDone) ||
		detailBody.Data.Callbacks[0].LastError != "" {
		t.Fatalf("unexpected detail callback: %+v", detailBody.Data.Callbacks[0])
	}
	if !strings.HasPrefix(detailBody.Data.Task.ExternalBatchID, "job_") {
		t.Fatalf("detail externalBatchId=%q, want job_ prefix", detailBody.Data.Task.ExternalBatchID)
	}
	if detailBody.Data.Task.ExecutionDurationSeconds < 0 || detailBody.Data.Task.ExecutionDurationText == "" || strings.HasPrefix(detailBody.Data.Task.ExecutionDurationText, "已执行 ") {
		t.Fatalf("unexpected detail execution duration: %+v", detailBody.Data.Task)
	}
	allDetailItems := append(detailBody.Data.Items, detailPage2Body.Data.Items...)
	for _, item := range allDetailItems {
		if item.ResultStatus == "PENDING" {
			continue
		}
		if item.AnswerText == "" || item.ReferenceCount == 0 || item.ResultStatus != "IMPORTED" {
			t.Fatalf("unexpected detail item: %+v", item)
		}
	}
	var mainlandPlatformUserID int64
	if err := db.QueryRow(ctx, `select id from platform_users where platform_code='mainland'`).Scan(&mainlandPlatformUserID); err != nil {
		t.Fatal(err)
	}
	metricsTo := time.Now().Add(1 * time.Second)
	metricsURL := fmt.Sprintf("%s/api/admin/v1/platform-users/%d/metrics?createdFrom=%s&createdTo=%s",
		gatewayBaseURL,
		mainlandPlatformUserID,
		url.QueryEscape(metricsFrom.Format(time.RFC3339Nano)),
		url.QueryEscape(metricsTo.Format(time.RFC3339Nano)),
	)
	metricsResp := adminGet(t, metricsURL, auth)
	if metricsResp.StatusCode != http.StatusOK {
		t.Fatalf("admin platform metrics status=%d body=%s", metricsResp.StatusCode, readBody(metricsResp))
	}
	var metricsBody struct {
		Data struct {
			User struct {
				PlatformCode string `json:"platformCode"`
			} `json:"user"`
			TaskMetrics struct {
				SubmittedTaskCount     int     `json:"submittedTaskCount"`
				CompletedTaskCount     int     `json:"completedTaskCount"`
				ProcessingTaskCount    int     `json:"processingTaskCount"`
				FailedTaskCount        int     `json:"failedTaskCount"`
				TaskSuccessRate        float64 `json:"taskSuccessRate"`
				AverageDurationSeconds int32   `json:"averageDurationSeconds"`
				AverageDurationText    string  `json:"averageDurationText"`
			} `json:"taskMetrics"`
			QuestionMetrics struct {
				SubmittedQuestionCount int     `json:"submittedQuestionCount"`
				CompletedAnswerCount   int     `json:"completedAnswerCount"`
				FailedQuestionCount    int     `json:"failedQuestionCount"`
				AnswerCompletionRate   float64 `json:"answerCompletionRate"`
			} `json:"questionMetrics"`
			ResultMetrics struct {
				ResultFileCount int     `json:"resultFileCount"`
				RowCount        int     `json:"rowCount"`
				ValidRowCount   int     `json:"validRowCount"`
				ValidRowRate    float64 `json:"validRowRate"`
			} `json:"resultMetrics"`
			CallbackMetrics struct {
				CallbackCount        int     `json:"callbackCount"`
				CallbackSuccessCount int     `json:"callbackSuccessCount"`
				CallbackSuccessRate  float64 `json:"callbackSuccessRate"`
			} `json:"callbackMetrics"`
			StatusBreakdown []struct {
				Label string `json:"label"`
				Value int    `json:"value"`
			} `json:"statusBreakdown"`
			FailureReasons []any `json:"failureReasons"`
			RecentTasks    []struct {
				TaskName string `json:"taskName"`
				Status   string `json:"status"`
			} `json:"recentTasks"`
			ProviderDailyMetrics []struct {
				MetricDate         string `json:"metricDate"`
				ProviderCode       string `json:"providerCode"`
				TaskCount          int    `json:"taskCount"`
				QuestionCount      int    `json:"questionCount"`
				SuccessAnswerCount int    `json:"successAnswerCount"`
				FailedAnswerCount  int    `json:"failedAnswerCount"`
			} `json:"providerDailyMetrics"`
		} `json:"data"`
	}
	if err := json.NewDecoder(metricsResp.Body).Decode(&metricsBody); err != nil {
		t.Fatal(err)
	}
	_ = metricsResp.Body.Close()
	if metricsBody.Data.User.PlatformCode != "mainland" ||
		metricsBody.Data.TaskMetrics.SubmittedTaskCount != 1 ||
		metricsBody.Data.TaskMetrics.CompletedTaskCount != 1 ||
		metricsBody.Data.TaskMetrics.ProcessingTaskCount != 0 ||
		metricsBody.Data.TaskMetrics.FailedTaskCount != 0 ||
		metricsBody.Data.TaskMetrics.TaskSuccessRate != 1 ||
		metricsBody.Data.TaskMetrics.AverageDurationSeconds < 0 ||
		metricsBody.Data.TaskMetrics.AverageDurationText == "" {
		t.Fatalf("unexpected platform task metrics: %+v", metricsBody.Data.TaskMetrics)
	}
	if metricsBody.Data.QuestionMetrics.SubmittedQuestionCount != 2 ||
		metricsBody.Data.QuestionMetrics.CompletedAnswerCount != 2 ||
		metricsBody.Data.QuestionMetrics.FailedQuestionCount != 0 ||
		metricsBody.Data.QuestionMetrics.AnswerCompletionRate != 1 {
		t.Fatalf("unexpected platform question metrics: %+v", metricsBody.Data.QuestionMetrics)
	}
	if metricsBody.Data.ResultMetrics.ResultFileCount != 1 ||
		metricsBody.Data.ResultMetrics.RowCount != 54 ||
		metricsBody.Data.ResultMetrics.ValidRowCount != 54 ||
		metricsBody.Data.ResultMetrics.ValidRowRate != 1 {
		t.Fatalf("unexpected platform result metrics: %+v", metricsBody.Data.ResultMetrics)
	}
	if metricsBody.Data.CallbackMetrics.CallbackCount != 1 ||
		metricsBody.Data.CallbackMetrics.CallbackSuccessCount != 1 ||
		metricsBody.Data.CallbackMetrics.CallbackSuccessRate != 1 {
		t.Fatalf("unexpected platform callback metrics: %+v", metricsBody.Data.CallbackMetrics)
	}
	if len(metricsBody.Data.StatusBreakdown) != 1 ||
		metricsBody.Data.StatusBreakdown[0].Label != string(biz.TaskStatusDone) ||
		metricsBody.Data.StatusBreakdown[0].Value != 1 ||
		len(metricsBody.Data.FailureReasons) != 0 ||
		len(metricsBody.Data.RecentTasks) != 1 ||
		metricsBody.Data.RecentTasks[0].TaskName != batchNo ||
		metricsBody.Data.RecentTasks[0].Status != string(biz.TaskStatusDone) {
		t.Fatalf("unexpected platform metric detail lists: %+v", metricsBody.Data)
	}
	if len(metricsBody.Data.ProviderDailyMetrics) != 1 ||
		metricsBody.Data.ProviderDailyMetrics[0].MetricDate != timezone.InAdminLocation(updatedTask.CreatedAt).Format("2006-01-02") ||
		metricsBody.Data.ProviderDailyMetrics[0].ProviderCode != "vendor_a" ||
		metricsBody.Data.ProviderDailyMetrics[0].TaskCount != 1 ||
		metricsBody.Data.ProviderDailyMetrics[0].QuestionCount != 2 ||
		metricsBody.Data.ProviderDailyMetrics[0].SuccessAnswerCount != 2 ||
		metricsBody.Data.ProviderDailyMetrics[0].FailedAnswerCount != 0 {
		t.Fatalf("unexpected provider daily metrics: %+v", metricsBody.Data.ProviderDailyMetrics)
	}
	providerMetricsURL := fmt.Sprintf("%s/api/admin/v1/providers/vendor_a/metrics?createdFrom=%s&createdTo=%s",
		gatewayBaseURL,
		url.QueryEscape(metricsFrom.Format(time.RFC3339Nano)),
		url.QueryEscape(metricsTo.Format(time.RFC3339Nano)),
	)
	providerMetricsResp := adminGet(t, providerMetricsURL, auth)
	if providerMetricsResp.StatusCode != http.StatusOK {
		t.Fatalf("admin provider metrics status=%d body=%s", providerMetricsResp.StatusCode, readBody(providerMetricsResp))
	}
	var providerMetricsBody struct {
		Data struct {
			Provider struct {
				ProviderCode string `json:"providerCode"`
			} `json:"provider"`
			Summary struct {
				TaskCount          int     `json:"taskCount"`
				QuestionCount      int     `json:"questionCount"`
				SuccessAnswerCount int     `json:"successAnswerCount"`
				FailedAnswerCount  int     `json:"failedAnswerCount"`
				AnswerSuccessRate  float64 `json:"answerSuccessRate"`
			} `json:"summary"`
			DailyMetrics []struct {
				MetricDate         string `json:"metricDate"`
				ProviderCode       string `json:"providerCode"`
				TaskCount          int    `json:"taskCount"`
				QuestionCount      int    `json:"questionCount"`
				SuccessAnswerCount int    `json:"successAnswerCount"`
				FailedAnswerCount  int    `json:"failedAnswerCount"`
			} `json:"dailyMetrics"`
		} `json:"data"`
	}
	if err := json.NewDecoder(providerMetricsResp.Body).Decode(&providerMetricsBody); err != nil {
		t.Fatal(err)
	}
	_ = providerMetricsResp.Body.Close()
	if providerMetricsBody.Data.Provider.ProviderCode != "vendor_a" ||
		providerMetricsBody.Data.Summary.TaskCount != 1 ||
		providerMetricsBody.Data.Summary.QuestionCount != 2 ||
		providerMetricsBody.Data.Summary.SuccessAnswerCount != 2 ||
		providerMetricsBody.Data.Summary.FailedAnswerCount != 0 ||
		providerMetricsBody.Data.Summary.AnswerSuccessRate != 1 ||
		len(providerMetricsBody.Data.DailyMetrics) != 1 ||
		providerMetricsBody.Data.DailyMetrics[0].MetricDate != timezone.InAdminLocation(updatedTask.CreatedAt).Format("2006-01-02") {
		t.Fatalf("unexpected admin provider metrics body: %+v", providerMetricsBody.Data)
	}
	if _, err := db.Exec(ctx, `delete from cdp_task_item_results where task_id=$1 and item_id=$2`, updatedTask.ID, "MPZ-REAL-20260723180500-qwen-002"); err != nil {
		t.Fatal(err)
	}
	unansweredPlatformResp := adminGet(t, metricsURL, auth)
	if unansweredPlatformResp.StatusCode != http.StatusOK {
		t.Fatalf("admin platform unanswered metrics status=%d body=%s", unansweredPlatformResp.StatusCode, readBody(unansweredPlatformResp))
	}
	var unansweredPlatformBody struct {
		Data struct {
			QuestionMetrics struct {
				SubmittedQuestionCount int     `json:"submittedQuestionCount"`
				CompletedAnswerCount   int     `json:"completedAnswerCount"`
				FailedQuestionCount    int     `json:"failedQuestionCount"`
				AnswerCompletionRate   float64 `json:"answerCompletionRate"`
			} `json:"questionMetrics"`
			ProviderDailyMetrics []struct {
				SuccessAnswerCount int `json:"successAnswerCount"`
				FailedAnswerCount  int `json:"failedAnswerCount"`
			} `json:"providerDailyMetrics"`
		} `json:"data"`
	}
	if err := json.NewDecoder(unansweredPlatformResp.Body).Decode(&unansweredPlatformBody); err != nil {
		t.Fatal(err)
	}
	_ = unansweredPlatformResp.Body.Close()
	if unansweredPlatformBody.Data.QuestionMetrics.SubmittedQuestionCount != 2 ||
		unansweredPlatformBody.Data.QuestionMetrics.CompletedAnswerCount != 1 ||
		unansweredPlatformBody.Data.QuestionMetrics.FailedQuestionCount != 1 ||
		unansweredPlatformBody.Data.QuestionMetrics.AnswerCompletionRate != float64(1)/float64(2) ||
		len(unansweredPlatformBody.Data.ProviderDailyMetrics) != 1 ||
		unansweredPlatformBody.Data.ProviderDailyMetrics[0].SuccessAnswerCount != 1 ||
		unansweredPlatformBody.Data.ProviderDailyMetrics[0].FailedAnswerCount != 1 {
		t.Fatalf("unexpected unanswered platform metrics body: %+v", unansweredPlatformBody.Data)
	}
	unansweredResp := adminGet(t, providerMetricsURL, auth)
	if unansweredResp.StatusCode != http.StatusOK {
		t.Fatalf("admin provider unanswered metrics status=%d body=%s", unansweredResp.StatusCode, readBody(unansweredResp))
	}
	var unansweredBody struct {
		Data struct {
			Summary struct {
				SuccessAnswerCount int     `json:"successAnswerCount"`
				FailedAnswerCount  int     `json:"failedAnswerCount"`
				AnswerSuccessRate  float64 `json:"answerSuccessRate"`
			} `json:"summary"`
			DailyMetrics []struct {
				SuccessAnswerCount int `json:"successAnswerCount"`
				FailedAnswerCount  int `json:"failedAnswerCount"`
			} `json:"dailyMetrics"`
		} `json:"data"`
	}
	if err := json.NewDecoder(unansweredResp.Body).Decode(&unansweredBody); err != nil {
		t.Fatal(err)
	}
	_ = unansweredResp.Body.Close()
	if unansweredBody.Data.Summary.SuccessAnswerCount != 1 ||
		unansweredBody.Data.Summary.FailedAnswerCount != 1 ||
		unansweredBody.Data.Summary.AnswerSuccessRate != float64(1)/float64(2) ||
		len(unansweredBody.Data.DailyMetrics) != 1 ||
		unansweredBody.Data.DailyMetrics[0].SuccessAnswerCount != 1 ||
		unansweredBody.Data.DailyMetrics[0].FailedAnswerCount != 1 {
		t.Fatalf("unexpected unanswered provider metrics body: %+v", unansweredBody.Data)
	}
	exportResp := adminPostJSON(t, gatewayBaseURL+"/api/admin/v1/tasks/"+fmt.Sprint(updatedTask.ID)+"/items-export", auth, map[string]any{})
	if exportResp.StatusCode != http.StatusOK {
		t.Fatalf("admin export status=%d body=%s", exportResp.StatusCode, readBody(exportResp))
	}
	var exportBody struct {
		Data struct {
			DownloadURL string `json:"downloadUrl"`
			ObjectKey   string `json:"objectKey"`
			ExpiresAt   string `json:"expiresAt"`
		} `json:"data"`
	}
	if err := json.NewDecoder(exportResp.Body).Decode(&exportBody); err != nil {
		t.Fatal(err)
	}
	_ = exportResp.Body.Close()
	if exportBody.Data.DownloadURL == "" || exportBody.Data.ObjectKey == "" || exportBody.Data.ExpiresAt == "" {
		t.Fatalf("unexpected export body: %+v", exportBody.Data)
	}
	defer deleteOSSObject(t, cfg.OSS, exportBody.Data.ObjectKey)
	downloadResp := get(t, exportBody.Data.DownloadURL)
	if downloadResp.StatusCode != http.StatusOK {
		t.Fatalf("export download status=%d body=%s", downloadResp.StatusCode, readBody(downloadResp))
	}
	exportBytes, err := io.ReadAll(downloadResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = downloadResp.Body.Close()
	exportWorkbook, err := excelize.OpenReader(bytes.NewReader(exportBytes))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exportWorkbook.Close() }()
	successRows, err := exportWorkbook.GetRows("成功")
	if err != nil {
		t.Fatal(err)
	}
	failedRows, err := exportWorkbook.GetRows("失败／未执行")
	if err != nil {
		t.Fatal(err)
	}
	if len(successRows) != 2 || len(failedRows) != 2 {
		t.Fatalf("export sheet row counts: success=%d failed=%d", len(successRows), len(failedRows))
	}
	if successRows[0][0] != "轮次" || successRows[1][1] != "MPZ-REAL-20260723180500-doubao-001" || successRows[1][4] != "IMPORTED" {
		t.Fatalf("unexpected success export rows: %+v", successRows)
	}
	if failedRows[0][0] != "轮次" ||
		failedRows[1][0] != "1" ||
		failedRows[1][1] != "MPZ-REAL-20260723180500-qwen-002" ||
		failedRows[1][4] != "PENDING" {
		t.Fatalf("unexpected failed export rows: %+v", failedRows)
	}
	if _, err := db.Exec(ctx, `update cdp_task_items set result_status='FAILED', updated_at=now() where task_id=$1 and item_id=$2`, updatedTask.ID, "MPZ-REAL-20260723180500-qwen-002"); err != nil {
		t.Fatal(err)
	}
	sortedDetailResp := adminGet(t, gatewayBaseURL+"/api/admin/v1/tasks/"+fmt.Sprint(updatedTask.ID)+"?itemPage=1&itemPageSize=1", auth)
	if sortedDetailResp.StatusCode != http.StatusOK {
		t.Fatalf("admin sorted detail status=%d body=%s", sortedDetailResp.StatusCode, readBody(sortedDetailResp))
	}
	var sortedDetailBody struct {
		Data struct {
			Items []struct {
				ItemID       string `json:"itemId"`
				ResultStatus string `json:"resultStatus"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.NewDecoder(sortedDetailResp.Body).Decode(&sortedDetailBody); err != nil {
		t.Fatal(err)
	}
	_ = sortedDetailResp.Body.Close()
	if len(sortedDetailBody.Data.Items) != 1 ||
		sortedDetailBody.Data.Items[0].ItemID != "MPZ-REAL-20260723180500-qwen-002" ||
		sortedDetailBody.Data.Items[0].ResultStatus != "FAILED" {
		t.Fatalf("unfinished or failed item was not prioritized: %+v", sortedDetailBody.Data.Items)
	}
	var auditCount int
	if err := db.QueryRow(ctx, `select count(*) from cdp_admin_audit_logs where action='CHECK_TASK_RESULT' and resource_id=$1`, fmt.Sprint(task.ID)).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("audit count=%d, want 1", auditCount)
	}
	retryResp := adminPostJSON(t, gatewayBaseURL+"/api/admin/v1/tasks/"+fmt.Sprint(task.ID)+"/retry-callback", auth, map[string]any{})
	if retryResp.StatusCode != http.StatusOK {
		t.Fatalf("admin retry callback status=%d body=%s", retryResp.StatusCode, readBody(retryResp))
	}
	var retryBody struct {
		Data struct {
			Delivered     bool   `json:"delivered"`
			ForceRequired bool   `json:"forceRequired"`
			Reason        string `json:"reason"`
			SuccessCount  int    `json:"successCount"`
			TotalCount    int    `json:"totalCount"`
		} `json:"data"`
	}
	if err := json.NewDecoder(retryResp.Body).Decode(&retryBody); err != nil {
		t.Fatal(err)
	}
	_ = retryResp.Body.Close()
	if retryBody.Data.Delivered || !retryBody.Data.ForceRequired || retryBody.Data.SuccessCount != 1 || retryBody.Data.TotalCount != 2 {
		t.Fatalf("retry callback readiness=%+v, want force required with 1/2", retryBody.Data)
	}
	if !strings.Contains(retryBody.Data.Reason, "当前成功 1 / 任务总数 2") {
		t.Fatalf("retry callback reason=%q", retryBody.Data.Reason)
	}
	forceRetryResp := adminPostJSON(t, gatewayBaseURL+"/api/admin/v1/tasks/"+fmt.Sprint(task.ID)+"/retry-callback", auth, map[string]any{
		"force": true,
	})
	if forceRetryResp.StatusCode != http.StatusOK {
		t.Fatalf("admin force retry callback status=%d body=%s", forceRetryResp.StatusCode, readBody(forceRetryResp))
	}
	var forceRetryBody struct {
		Data struct {
			Delivered    bool `json:"delivered"`
			SuccessCount int  `json:"successCount"`
			TotalCount   int  `json:"totalCount"`
		} `json:"data"`
	}
	if err := json.NewDecoder(forceRetryResp.Body).Decode(&forceRetryBody); err != nil {
		t.Fatal(err)
	}
	_ = forceRetryResp.Body.Close()
	if !forceRetryBody.Data.Delivered || forceRetryBody.Data.SuccessCount != 1 || forceRetryBody.Data.TotalCount != 2 {
		t.Fatalf("force retry callback=%+v, want delivered with 1/2", forceRetryBody.Data)
	}
	detailAfterRetryResp := adminGet(t, gatewayBaseURL+"/api/admin/v1/tasks/"+fmt.Sprint(updatedTask.ID), auth)
	if detailAfterRetryResp.StatusCode != http.StatusOK {
		t.Fatalf("admin detail after retry status=%d body=%s", detailAfterRetryResp.StatusCode, readBody(detailAfterRetryResp))
	}
	var detailAfterRetryBody struct {
		Data struct {
			Callbacks []any `json:"callbacks"`
		} `json:"data"`
	}
	if err := json.NewDecoder(detailAfterRetryResp.Body).Decode(&detailAfterRetryBody); err != nil {
		t.Fatal(err)
	}
	_ = detailAfterRetryResp.Body.Close()
	if len(detailAfterRetryBody.Data.Callbacks) != 2 {
		t.Fatalf("detail callback count after retry=%d, want 2", len(detailAfterRetryBody.Data.Callbacks))
	}
	if err := db.QueryRow(ctx, `select count(*) from cdp_admin_audit_logs where action='RETRY_TASK_CALLBACK' and resource_id=$1`, fmt.Sprint(task.ID)).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 2 {
		t.Fatalf("retry audit count=%d, want 2", auditCount)
	}
	for _, action := range []string{"CREATE_PLATFORM_USER", "UPDATE_PLATFORM_USER"} {
		if err := db.QueryRow(ctx, `select count(*) from cdp_admin_audit_logs where action=$1 and resource_type='PLATFORM_USER' and resource_id=$2`, action, createPlatformBody.Data.ID).Scan(&auditCount); err != nil {
			t.Fatal(err)
		}
		if auditCount != 1 {
			t.Fatalf("%s audit count=%d, want 1", action, auditCount)
		}
	}
	callbackMu.Lock()
	gotBatchNo := callbackPayload["batchNo"]
	gotResultURL, _ := callbackPayload["resultUrl"].(string)
	callbackMu.Unlock()
	if gotBatchNo != batchNo {
		t.Fatalf("callback batchNo=%v, want %s", gotBatchNo, batchNo)
	}
	if !strings.Contains(gotResultURL, "successOnly=true") {
		t.Fatalf("force callback resultUrl=%q, want successOnly=true", gotResultURL)
	}
}

func get(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func getWithAPIKey(t *testing.T, url string, apiKey string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-API-Key", apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func syncPlatformUsers(t *testing.T, ctx context.Context, db *pgxpool.Pool, cfg *config.Config) *data.PlatformUserRepo {
	t.Helper()
	repo := data.NewPlatformUserRepo(db)
	if err := repo.SyncFromConfig(ctx, cfg.PlatformUsers); err != nil {
		t.Fatal(err)
	}
	return repo
}

func ensureE2EProviderConfig(cfg *config.Config) {
	if cfg.Provider == nil {
		cfg.Provider = map[string]config.ProviderConfig{}
	}
	if existing, ok := cfg.Provider["vendor_a"]; ok && existing.QPSLimit > 0 && existing.ConcurrencyLimit > 0 {
		return
	}
	base := config.ProviderConfig{
		Code:             "vendor_a",
		DisplayName:      "vendor_a",
		Token:            "e2e-provider-token",
		ResultMode:       "callback_upload",
		Timeout:          15 * time.Second,
		QPSLimit:         10,
		ConcurrencyLimit: 20,
		Enabled:          true,
	}
	for _, provider := range cfg.Provider {
		base.Timeout = provider.Timeout
		base.QPSLimit = provider.QPSLimit
		base.ConcurrencyLimit = provider.ConcurrencyLimit
		base.ResultMode = provider.ResultMode
		if provider.Token != "" {
			base.Token = provider.Token
		}
		break
	}
	base.Code = "vendor_a"
	base.DisplayName = "vendor_a"
	cfg.Provider["vendor_a"] = base
}

func setDefaultProviderCode(cfg *config.Config, providerCode string) {
	for code, user := range cfg.PlatformUsers {
		user.DefaultProviderCode = providerCode
		cfg.PlatformUsers[code] = user
	}
}

type adminAuth struct {
	Cookie *http.Cookie
	CSRF   string
}

func adminLogin(t *testing.T, baseURL string, cfg *config.Config) adminAuth {
	t.Helper()
	resp := postJSON(t, baseURL+"/api/admin/v1/login", "", map[string]any{
		"username": cfg.Admin.Bootstrap.Username,
		"password": cfg.Admin.Bootstrap.Password,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin login status=%d body=%s", resp.StatusCode, readBody(resp))
	}
	var body struct {
		Data struct {
			CSRFToken string `json:"csrfToken"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	var cookie *http.Cookie
	for _, item := range resp.Cookies() {
		if item.Name == cfg.Admin.Session.CookieName {
			copied := *item
			cookie = &copied
			break
		}
	}
	if cookie == nil || body.Data.CSRFToken == "" {
		t.Fatalf("admin login missing cookie or csrf: cookie=%v csrf=%q", cookie, body.Data.CSRFToken)
	}
	return adminAuth{Cookie: cookie, CSRF: body.Data.CSRFToken}
}

func adminGet(t *testing.T, url string, auth adminAuth) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(auth.Cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func adminPostJSON(t *testing.T, url string, auth adminAuth, payload any) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", auth.CSRF)
	req.AddCookie(auth.Cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func adminPutJSON(t *testing.T, url string, auth adminAuth, payload any) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", auth.CSRF)
	req.AddCookie(auth.Cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func openRealRedisForTest(t *testing.T, ctx context.Context, cfg *config.Config) (*redis.Client, func()) {
	t.Helper()
	client, err := data.OpenRedis(ctx, cfg.Redis)
	if err == nil {
		return client, func() {}
	}
	dockerPath, dockerErr := exec.LookPath("docker")
	if dockerErr != nil {
		t.Fatalf("open redis: %v; docker unavailable: %v", err, dockerErr)
	}
	listener, listenErr := net.Listen("tcp", "127.0.0.1:0")
	if listenErr != nil {
		t.Fatal(listenErr)
	}
	_, port, splitErr := net.SplitHostPort(listener.Addr().String())
	_ = listener.Close()
	if splitErr != nil {
		t.Fatal(splitErr)
	}
	name := fmt.Sprintf("cdp-router-e2e-redis-%d", time.Now().UnixNano())
	runCmd := exec.Command(dockerPath, "run", "-d", "--rm", "--name", name, "-p", "127.0.0.1:"+port+":6379", "redis:latest")
	if output, runErr := runCmd.CombinedOutput(); runErr != nil {
		t.Fatalf("start redis container: %v output=%s", runErr, string(output))
	}
	cleanup := func() {
		_ = exec.Command(dockerPath, "rm", "-f", name).Run()
	}
	cfg.Redis.Addr = "127.0.0.1:" + port
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		client, lastErr = data.OpenRedis(ctx, cfg.Redis)
		if lastErr == nil {
			return client, cleanup
		}
		time.Sleep(200 * time.Millisecond)
	}
	cleanup()
	t.Fatalf("wait redis container ready: %v", lastErr)
	return nil, func() {}
}

func assertResultRowsAndItemBackfill(t *testing.T, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, taskID int64) {
	t.Helper()
	var rowCount int
	if err := db.QueryRow(context.Background(), `select count(*) from cdp_task_result_rows where task_id=$1`, taskID).Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	if rowCount == 0 {
		t.Fatal("expected cdp_task_result_rows rows")
	}
	var itemResultCount int
	if err := db.QueryRow(context.Background(), `select count(*) from cdp_task_item_results where task_id=$1`, taskID).Scan(&itemResultCount); err != nil {
		t.Fatal(err)
	}
	if itemResultCount == 0 {
		t.Fatal("expected cdp_task_item_results rows")
	}
	var answerText string
	var referenceCount int
	var recommendedQuestions []byte
	if err := db.QueryRow(context.Background(), `
select coalesce(answer_text,''), reference_count, recommended_questions
from cdp_task_item_results
where task_id=$1 and item_id='item-1'
`, taskID).Scan(&answerText, &referenceCount, &recommendedQuestions); err != nil {
		t.Fatal(err)
	}
	if answerText != "这是一条 E2E 测试回答" {
		t.Fatalf("answer_text=%q", answerText)
	}
	if referenceCount != 2 {
		t.Fatalf("reference_count=%d, want 2", referenceCount)
	}
	if !strings.Contains(string(recommendedQuestions), "推荐追问一") {
		t.Fatalf("recommended_questions=%s", string(recommendedQuestions))
	}
	var importedCount int
	if err := db.QueryRow(context.Background(), `select count(*) from cdp_task_items where task_id=$1 and result_status='IMPORTED'`, taskID).Scan(&importedCount); err != nil {
		t.Fatal(err)
	}
	if importedCount == 0 {
		t.Fatal("expected cdp_task_items result_status imported")
	}
}

func assertRealWorkbookResult(t *testing.T, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, taskID int64) {
	t.Helper()
	var rowCount int
	if err := db.QueryRow(context.Background(), `select count(*) from cdp_task_result_rows where task_id=$1`, taskID).Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	if rowCount != 54 {
		t.Fatalf("result row count=%d, want 54", rowCount)
	}
	var itemResultCount int
	if err := db.QueryRow(context.Background(), `select count(*) from cdp_task_item_results where task_id=$1`, taskID).Scan(&itemResultCount); err != nil {
		t.Fatal(err)
	}
	if itemResultCount != 2 {
		t.Fatalf("item result count=%d, want 2", itemResultCount)
	}
	assertReferenceCount(t, db, taskID, "MPZ-REAL-20260723180500-doubao-001", 28)
	assertReferenceCount(t, db, taskID, "MPZ-REAL-20260723180500-qwen-002", 24)
}

func assertReferenceCount(t *testing.T, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, taskID int64, itemID string, want int) {
	t.Helper()
	var answerText string
	var referenceCount int
	if err := db.QueryRow(context.Background(), `
select coalesce(answer_text,''), reference_count
from cdp_task_item_results
where task_id=$1 and item_id=$2
`, taskID, itemID).Scan(&answerText, &referenceCount); err != nil {
		t.Fatal(err)
	}
	if answerText == "" {
		t.Fatalf("item %s answer_text is empty", itemID)
	}
	if referenceCount != want {
		t.Fatalf("item %s reference_count=%d, want %d", itemID, referenceCount, want)
	}
}

func assertTaskEventCount(t *testing.T, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, taskID int64, eventType biz.EventType, want int) {
	t.Helper()
	var count int
	if err := db.QueryRow(context.Background(), `
select count(*)
from cdp_task_events
where task_id=$1 and event_type=$2
`, taskID, string(eventType)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("task event %s count=%d, want %d", eventType, count, want)
	}
}

func createSubmittedTaskForMaintenance(t *testing.T, ctx context.Context, repo *data.TaskRepo, db *pgxpool.Pool, batchNo string, providerCode string, externalBatchID string, items []biz.TaskItem, submittedAge time.Duration) *biz.Task {
	t.Helper()
	rawRequest, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		t.Fatal(err)
	}
	task, err := repo.CreateTask(ctx, biz.CreateTaskInput{
		JobID:          "job_" + batchNo,
		PlatformUserID: 1,
		PlatformCode:   "maintenance-e2e",
		BatchNo:        batchNo,
		ProviderCode:   providerCode,
		GatewayCode:    "maintenance-e2e",
		Source:         "maintenance-e2e",
		BizDate:        time.Now().Format("2006-01-02"),
		CallbackURL:    "http://127.0.0.1/callback",
		Items:          items,
		RawRequest:     rawRequest,
	}, "http://127.0.0.1/external/callback/"+providerCode)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkSubmitted(ctx, task.ID, json.RawMessage(`{}`), biz.SubmitResult{
		ExternalBatchID: externalBatchID,
		AcceptedCount:   len(items),
		RawResponse:     json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `update cdp_tasks set submitted_at=$2, updated_at=$2 where id=$1`, task.ID, time.Now().Add(-submittedAge)); err != nil {
		t.Fatal(err)
	}
	updated, err := repo.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func listResultObjectKeys(t *testing.T, db *pgxpool.Pool, taskID int64) []string {
	t.Helper()
	rows, err := db.Query(context.Background(), `
select object_key
from cdp_task_result_files
where task_id=$1 and object_key is not null
order by id
`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return keys
}

func assertMergedWorkbookRows(t *testing.T, content []byte, sheet string, wantRows int) {
	t.Helper()
	book, err := excelize.OpenReader(bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = book.Close() }()
	rows, err := book.GetRows(sheet)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != wantRows {
		t.Fatalf("merged workbook sheet %s row count=%d, want %d", sheet, len(rows), wantRows)
	}
}

func providerUploadAndCallback(t *testing.T, token, gatewayBaseURL, batchNo, providerCode, expectedPrefix string, providerErr chan<- string) {
	t.Helper()
	providerUploadFilesAndCallback(t, token, gatewayBaseURL, batchNo, providerCode, expectedPrefix, []string{"result.xlsx"}, providerErr)
}

func providerUploadFilesAndCallback(t *testing.T, token, gatewayBaseURL, batchNo, providerCode, expectedPrefix string, fileNames []string, providerErr chan<- string) {
	t.Helper()
	for _, fileName := range fileNames {
		providerUploadResultFile(t, token, gatewayBaseURL, batchNo, providerCode, expectedPrefix, fileName, providerErr)
	}

	callbackResp := postJSON(t, gatewayBaseURL+"/external/callback/"+providerCode, token, map[string]any{
		"batchNo":         batchNo,
		"externalBatchId": "vendor-batch-e2e",
		"status":          "DONE",
		"totalCount":      1,
		"successCount":    1,
		"failedCount":     0,
		"finishedAt":      time.Now().Format(time.RFC3339),
	})
	if callbackResp.StatusCode != http.StatusOK {
		reportProviderErr(providerErr, fmt.Sprintf("provider callback status=%d body=%s", callbackResp.StatusCode, readBody(callbackResp)))
	}
	_ = callbackResp.Body.Close()
}

func providerUploadResultFile(t *testing.T, token, gatewayBaseURL, batchNo, providerCode, expectedPrefix, fileName string, providerErr chan<- string) {
	t.Helper()
	uploadResp := postJSON(t, gatewayBaseURL+"/external/oss/"+providerCode+"/upload-url", token, map[string]any{
		"batchNo":  batchNo,
		"fileName": fileName,
	})
	if uploadResp.StatusCode != http.StatusOK {
		reportProviderErr(providerErr, fmt.Sprintf("upload url status=%d body=%s", uploadResp.StatusCode, readBody(uploadResp)))
		return
	}
	var upload struct {
		Data struct {
			UploadURL string            `json:"uploadUrl"`
			Key       string            `json:"key"`
			Headers   map[string]string `json:"headers"`
		} `json:"data"`
	}
	if err := json.NewDecoder(uploadResp.Body).Decode(&upload); err != nil {
		reportProviderErr(providerErr, fmt.Sprintf("decode upload url: %v", err))
		return
	}
	_ = uploadResp.Body.Close()
	if expectedPrefix != "" && !strings.HasPrefix(upload.Data.Key, expectedPrefix+"/"+batchNo+"/") {
		reportProviderErr(providerErr, fmt.Sprintf("upload object key=%s does not use provider bucket path prefix=%s", upload.Data.Key, expectedPrefix))
		return
	}

	xlsx := resultWorkbook(t)
	req, err := http.NewRequest(http.MethodPut, upload.Data.UploadURL, bytes.NewReader(xlsx))
	if err != nil {
		reportProviderErr(providerErr, fmt.Sprintf("build put request: %v", err))
		return
	}
	for key, value := range upload.Data.Headers {
		req.Header.Set(key, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		reportProviderErr(providerErr, fmt.Sprintf("put oss object: %v", err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		reportProviderErr(providerErr, fmt.Sprintf("put oss object status=%d body=%s", resp.StatusCode, string(body)))
		return
	}
}

func reportProviderErr(ch chan<- string, message string) {
	select {
	case ch <- message:
	default:
	}
}

func resultWorkbook(t *testing.T) []byte {
	t.Helper()
	return resultWorkbookForItem(t, "item-1")
}

func resultWorkbookForItem(t *testing.T, itemID string) []byte {
	t.Helper()
	book := excelize.NewFile()
	sheet := "AI回答"
	book.SetSheetName("Sheet1", sheet)
	_ = book.SetSheetRow(sheet, "A1", &[]any{"itemId", "问题", "平台", "深度思考过程", "AI回答", "AI生成时间", "推荐问题", "设备ID", "录屏url"})
	_ = book.SetSheetRow(sheet, "A2", &[]any{itemID, "请评价某产品的优缺点", "yuanbao", "这是一段思考过程", "这是一条 E2E 测试回答", "2026年07月23日18:13", "推荐追问一, 推荐追问二", "device-e2e", "https://example.com/recording"})
	sourceSheet := "溯源信息"
	_, _ = book.NewSheet(sourceSheet)
	_ = book.SetSheetRow(sourceSheet, "A1", &[]any{"itemId", "url", "标题", "内容", "发布日期", "网站名称", "网站权威性等级", "网站分类", "作者", "作者分类", "作者身份类型"})
	_ = book.SetSheetRow(sourceSheet, "A2", &[]any{itemID, "https://example.com/source-1", "引用标题一", "引用内容一", "2026-07-23", "示例网站", "Tier 1", "新闻", "作者一", "媒体作者", "个人"})
	_ = book.SetSheetRow(sourceSheet, "A3", &[]any{itemID, "https://example.com/source-2", "引用标题二", "引用内容二", "2026-07-23", "示例网站", "Tier 2", "社区", "作者二", "社区作者", "机构"})
	buf, err := book.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func postJSON(t *testing.T, url string, providerToken string, payload any) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if providerToken != "" {
		req.Header.Set("X-Provider-Token", providerToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func postJSONWithAPIKey(t *testing.T, url string, apiKey string, payload any) *http.Response {
	t.Helper()
	return postJSONWithAuthHeaders(t, url, map[string]string{"X-API-Key": apiKey}, payload)
}

func postJSONWithAuthHeaders(t *testing.T, url string, headers map[string]string, payload any) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		if value != "" {
			req.Header.Set(key, value)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func readBody(resp *http.Response) string {
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

func deleteOSSObject(t *testing.T, cfg config.OSSConfig, key string) {
	t.Helper()
	endpoint := cfg.Endpoint
	if endpoint == "" && cfg.Region != "" {
		endpoint = "https://oss-" + cfg.Region + ".aliyuncs.com"
	}
	if endpoint == "" {
		endpoint = cfg.Host
	}
	client, err := oss.New(endpoint, cfg.AccessKeyID, cfg.AccessKeySecret)
	if err != nil {
		t.Logf("cleanup oss client: %v", err)
		return
	}
	bucket, err := client.Bucket(cfg.Bucket)
	if err != nil {
		t.Logf("cleanup oss bucket: %v", err)
		return
	}
	if err := bucket.DeleteObject(key); err != nil {
		t.Logf("cleanup oss object %s: %v", key, err)
	}
}

func waitTaskStatus(t *testing.T, ctx context.Context, repo *data.TaskRepo, batchNo string, status biz.TaskStatus) *biz.Task {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("timed out waiting for task status")
		case <-ticker.C:
			task, err := repo.GetTaskByBatchNo(ctx, batchNo)
			if err != nil {
				continue
			}
			if task.Status == status {
				return task
			}
			if task.Status == biz.TaskStatusFailed {
				t.Fatalf("task failed: %s", task.LastError)
			}
		}
	}
}

func closeOnce(ch chan struct{}) {
	defer func() { _ = recover() }()
	close(ch)
}

func cleanupTaskDataByBatchNo(t *testing.T, db *pgxpool.Pool, batchNo string) {
	t.Helper()
	ctx := context.Background()
	statements := []string{
		`delete from cdp_admin_audit_logs where resource_type='TASK' and resource_id in (select id::text from cdp_tasks where batch_no=$1)`,
		`delete from cdp_task_callbacks where task_id in (select id from cdp_tasks where batch_no=$1)`,
		`delete from cdp_task_events where task_id in (select id from cdp_tasks where batch_no=$1)`,
		`delete from cdp_task_item_results where batch_no=$1`,
		`delete from cdp_task_result_rows where batch_no=$1`,
		`delete from cdp_task_result_files where batch_no=$1`,
		`delete from cdp_task_items where task_id in (select id from cdp_tasks where batch_no=$1)`,
		`delete from cdp_tasks where batch_no=$1`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(ctx, statement, batchNo); err != nil {
			t.Logf("cleanup task data for batch %s failed: %v", batchNo, err)
		}
	}
}

func cleanupPlatformUser(t *testing.T, db *pgxpool.Pool, platformCode string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Exec(ctx, `delete from cdp_admin_audit_logs where resource_type='PLATFORM_USER' and resource_id in (select id::text from platform_users where platform_code=$1)`, platformCode); err != nil {
		t.Logf("cleanup platform user audit logs for %s failed: %v", platformCode, err)
	}
	if _, err := db.Exec(ctx, `delete from platform_users where platform_code=$1`, platformCode); err != nil {
		t.Logf("cleanup platform user %s failed: %v", platformCode, err)
	}
}
