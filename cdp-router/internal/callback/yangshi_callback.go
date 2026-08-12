package callback

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/GitMarkZhang/cdp-router/internal/config"
	"github.com/GitMarkZhang/cdp-router/internal/pkg/httphelper"
)

type YangshiClient struct {
	client       *httphelper.Client
	uploadClient *httphelper.Client
}

type yangshiConfig struct {
	BaseURL       string
	ProviderCode  string
	ProviderToken string
}

type yangshiUploadURLResponse struct {
	Status  int              `json:"status"`
	Message any              `json:"message"`
	Data    yangshiUploadURL `json:"data"`
}

type yangshiUploadURL struct {
	Bucket    string            `json:"bucket"`
	Key       string            `json:"key"`
	UploadURL string            `json:"uploadUrl"`
	Method    string            `json:"method"`
	ExpiresAt string            `json:"expiresAt"`
	Headers   map[string]string `json:"headers"`
}

func NewYangshiClient(timeout time.Duration, _ biz.ObjectStore, _ ...string) *YangshiClient {
	return &YangshiClient{
		client:       httphelper.NewClient("yangshi_callback", timeout),
		uploadClient: httphelper.NewClient("yangshi_callback_upload", 0),
	}
}

func (c *YangshiClient) Deliver(ctx context.Context, task *biz.Task, payload biz.CallbackDelivery, user config.PlatformUserConfig) (biz.CallbackResult, error) {
	cfg, err := parseYangshiConfig(user)
	if err != nil {
		return biz.CallbackResult{}, err
	}
	content, err := buildYangshiResultWorkbook(payload.ResultItems)
	if err != nil {
		return biz.CallbackResult{}, err
	}
	fileName := task.BatchNo + "-result.xlsx"
	upload, uploadBody, err := c.requestUploadURL(ctx, cfg, task.BatchNo, fileName)
	if err != nil {
		return biz.CallbackResult{ResponseBody: uploadBody}, err
	}
	if err := c.putFile(ctx, upload, content, task.BatchNo); err != nil {
		return biz.CallbackResult{ResponseBody: uploadBody, EventPayload: yangshiCallbackEventPayload(upload, fileName, len(payload.ResultItems))}, err
	}
	notifyPayload := map[string]any{
		"batchNo":         task.BatchNo,
		"externalBatchId": task.JobID,
		"status":          string(biz.TaskStatusDone),
		"totalCount":      task.TotalCount,
		"successCount":    task.SuccessCount,
		"failedCount":     task.FailedCount,
		"finishedAt":      time.Now().Format(time.RFC3339),
	}
	requestPayload, _ := json.Marshal(map[string]any{
		"uploadUrlRequest": map[string]any{
			"batchNo":  task.BatchNo,
			"fileName": fileName,
		},
		"upload": map[string]any{
			"bucket": upload.Bucket,
			"key":    upload.Key,
			"method": upload.Method,
		},
		"callbackRequest": notifyPayload,
		"file": map[string]any{
			"sourceType": "DB_ITEM_RESULTS",
			"fileUrl":    yangshiCallbackFileURL(upload),
			"fileName":   fileName,
			"itemCount":  len(payload.ResultItems),
		},
	})
	notifyURL := yangshiNotifyURL(task.CallbackURL, cfg)
	statusCode, responseBody, err := c.notifyDone(ctx, cfg, notifyURL, notifyPayload, task.BatchNo, task.JobID)
	resultBody, _ := json.Marshal(map[string]any{
		"uploadUrlResponse": json.RawMessage(defaultJSONBytes([]byte(uploadBody))),
		"callbackResponse":  json.RawMessage(defaultJSONBytes([]byte(responseBody))),
	})
	result := biz.CallbackResult{
		TargetURL:      notifyURL,
		StatusCode:     statusCode,
		ResponseBody:   string(resultBody),
		RequestPayload: requestPayload,
		EventPayload:   yangshiCallbackEventPayload(upload, fileName, len(payload.ResultItems)),
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

func yangshiCallbackEventPayload(upload yangshiUploadURL, fileName string, itemCount int) map[string]any {
	return map[string]any{
		"uploadedFile": map[string]any{
			"sourceType": "DB_ITEM_RESULTS",
			"bucket":     upload.Bucket,
			"key":        upload.Key,
			"fileUrl":    yangshiCallbackFileURL(upload),
			"fileName":   fileName,
			"method":     upload.Method,
			"itemCount":  itemCount,
		},
	}
}

func yangshiCallbackFileURL(upload yangshiUploadURL) string {
	raw := strings.TrimSpace(upload.UploadURL)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func parseYangshiConfig(user config.PlatformUserConfig) (yangshiConfig, error) {
	var raw map[string]string
	if err := json.Unmarshal([]byte(user.CallbackConfig), &raw); err != nil {
		return yangshiConfig{}, fmt.Errorf("parse yangshi callback_config: %w", err)
	}
	cfg := yangshiConfig{
		BaseURL:       firstNonEmpty(raw["baseURL"], raw["base_url"], user.CallbackURL),
		ProviderCode:  firstNonEmpty(raw["providerCode"], raw["provider_code"]),
		ProviderToken: firstNonEmpty(raw["providerToken"], raw["provider_token"], user.APIKey),
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.ProviderCode = strings.TrimSpace(cfg.ProviderCode)
	cfg.ProviderToken = strings.TrimSpace(cfg.ProviderToken)
	if cfg.BaseURL == "" || cfg.ProviderCode == "" || cfg.ProviderToken == "" {
		return yangshiConfig{}, fmt.Errorf("yangshi callback_config requires baseURL, providerCode and providerToken/apiKey")
	}
	return cfg, nil
}

func yangshiNotifyURL(taskCallbackURL string, cfg yangshiConfig) string {
	callbackURL := strings.TrimSpace(taskCallbackURL)
	if callbackURL != "" && !strings.HasPrefix(strings.ToLower(callbackURL), "custom://") {
		return callbackURL
	}
	return cfg.BaseURL + "/external/callback/" + url.PathEscape(cfg.ProviderCode)
}

func (c *YangshiClient) requestUploadURL(ctx context.Context, cfg yangshiConfig, batchNo string, fileName string) (yangshiUploadURL, string, error) {
	body, _ := json.Marshal(map[string]string{
		"batchNo":  batchNo,
		"fileName": fileName,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.BaseURL+"/external/oss/"+url.PathEscape(cfg.ProviderCode)+"/upload-url", bytes.NewReader(body))
	if err != nil {
		return yangshiUploadURL{}, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Provider-Token", cfg.ProviderToken)
	result, err := c.client.DoRead(ctx, req, 1<<20, "operation", "yangshi_request_upload_url", "batch_no", batchNo)
	if err != nil {
		return yangshiUploadURL{}, "", err
	}
	responseBody := string(result.Body)
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		return yangshiUploadURL{}, responseBody, fmt.Errorf("yangshi upload-url returned %d: %s", result.StatusCode, responseBody)
	}
	var decoded yangshiUploadURLResponse
	if err := json.Unmarshal(result.Body, &decoded); err != nil {
		return yangshiUploadURL{}, responseBody, fmt.Errorf("decode yangshi upload-url response: %w", err)
	}
	if decoded.Status != 0 || decoded.Data.UploadURL == "" {
		return yangshiUploadURL{}, responseBody, fmt.Errorf("yangshi upload-url failed: %s", responseBody)
	}
	if decoded.Data.Method == "" {
		decoded.Data.Method = http.MethodPut
	}
	return decoded.Data, responseBody, nil
}

func (c *YangshiClient) putFile(ctx context.Context, upload yangshiUploadURL, content []byte, batchNo string) error {
	method := upload.Method
	if method == "" {
		method = http.MethodPut
	}
	req, err := http.NewRequestWithContext(ctx, method, upload.UploadURL, bytes.NewReader(content))
	if err != nil {
		return err
	}
	for key, value := range upload.Headers {
		if strings.TrimSpace(key) == "" {
			continue
		}
		req.Header.Set(key, value)
	}
	result, err := c.uploadClient.DoRead(ctx, req, 1<<20, "operation", "yangshi_put_result_file", "batch_no", batchNo)
	if err != nil {
		return err
	}
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		return fmt.Errorf("yangshi put file returned %d: %s", result.StatusCode, string(result.Body))
	}
	return nil
}

func (c *YangshiClient) notifyDone(ctx context.Context, cfg yangshiConfig, notifyURL string, payload map[string]any, batchNo string, externalBatchID string) (int, string, error) {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, notifyURL, bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Provider-Token", cfg.ProviderToken)
	result, err := c.client.DoRead(ctx, req, 1<<20,
		"operation", "yangshi_notify_done",
		"batch_no", batchNo,
		"external_batch_id", externalBatchID,
	)
	if err != nil {
		return 0, "", err
	}
	responseBody := string(result.Body)
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		return result.StatusCode, responseBody, fmt.Errorf("yangshi callback returned %d: %s", result.StatusCode, responseBody)
	}
	return result.StatusCode, responseBody, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func defaultJSONBytes(body []byte) []byte {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return []byte(`{}`)
	}
	if json.Valid(trimmed) {
		return trimmed
	}
	encoded, _ := json.Marshal(string(trimmed))
	return encoded
}
