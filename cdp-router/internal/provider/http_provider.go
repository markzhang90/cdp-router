package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/GitMarkZhang/cdp-router/internal/config"
	"github.com/GitMarkZhang/cdp-router/internal/pkg/httphelper"
	"golang.org/x/time/rate"
)

type Registry struct {
	providers map[string]biz.ProviderClient
	configs   map[string]config.ProviderConfig
}

func NewRegistry(configs map[string]config.ProviderConfig) *Registry {
	providers := make(map[string]biz.ProviderClient, len(configs))
	copiedConfigs := make(map[string]config.ProviderConfig, len(configs))
	for code, cfg := range configs {
		if cfg.Code == "" {
			cfg.Code = code
		}
		if cfg.BucketPathPrefix == "" {
			cfg.BucketPathPrefix = "cdp-router/q2a/" + cfg.Code
		}
		providers[code] = NewHTTPProvider(cfg)
		copiedConfigs[code] = cfg
	}
	return &Registry{providers: providers, configs: copiedConfigs}
}

func (r *Registry) Get(code string) (biz.ProviderClient, bool) {
	provider, ok := r.providers[code]
	return provider, ok
}

type HTTPProvider struct {
	cfg        config.ProviderConfig
	client     *httphelper.Client
	limiter    *rate.Limiter
	semaphore  chan struct{}
	once       sync.Once
	completion fileCompletion
}

func NewHTTPProvider(cfg config.ProviderConfig) *HTTPProvider {
	return &HTTPProvider{
		cfg:        cfg,
		client:     httphelper.NewClient("provider."+cfg.Code, cfg.Timeout),
		limiter:    rate.NewLimiter(rate.Limit(cfg.QPSLimit), cfg.QPSLimit),
		semaphore:  make(chan struct{}, cfg.ConcurrencyLimit),
		completion: newFileCompletion(cfg.Code),
	}
}

func (p *HTTPProvider) SubmitTask(ctx context.Context, _ *biz.Task, payload biz.SubmitTaskPayload) (biz.SubmitResult, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return biz.SubmitResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.SubmitURL, bytes.NewReader(body))
	if err != nil {
		return biz.SubmitResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	p.setAuthHeader(req)

	httpResult, err := p.do(req, 1<<20,
		"provider_code", p.cfg.Code,
		"operation", "submit_task",
		"batch_no", payload.BatchNo,
		"item_count", len(payload.Items),
	)
	if err != nil {
		return biz.SubmitResult{}, err
	}
	respBody := httpResult.Body
	if httpResult.StatusCode < 200 || httpResult.StatusCode >= 300 {
		return biz.SubmitResult{RawResponse: respBody}, fmt.Errorf("provider submit returned %d: %s", httpResult.StatusCode, string(respBody))
	}

	var decoded struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			ExternalBatchID string `json:"externalBatchId"`
			AcceptedCount   int    `json:"acceptedCount"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return biz.SubmitResult{RawResponse: respBody}, fmt.Errorf("decode provider response: %w", err)
	}
	if decoded.Code != 0 {
		return biz.SubmitResult{RawResponse: respBody}, fmt.Errorf("provider submit failed: %s", decoded.Message)
	}
	return biz.SubmitResult{
		ExternalBatchID: decoded.Data.ExternalBatchID,
		AcceptedCount:   decoded.Data.AcceptedCount,
		RawResponse:     respBody,
	}, nil
}

func (p *HTTPProvider) FetchStatus(ctx context.Context, externalBatchID string) (biz.ProviderStatusResult, error) {
	if p.cfg.StatusURL == "" {
		return biz.ProviderStatusResult{}, fmt.Errorf("provider status_url is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, expandExternalBatchID(p.cfg.StatusURL, externalBatchID), nil)
	if err != nil {
		return biz.ProviderStatusResult{}, err
	}
	p.setAuthHeader(req)
	httpResult, err := p.do(req, 1<<20,
		"provider_code", p.cfg.Code,
		"operation", "fetch_status",
		"external_batch_id", externalBatchID,
	)
	if err != nil {
		return biz.ProviderStatusResult{}, err
	}
	respBody := httpResult.Body
	if httpResult.StatusCode < 200 || httpResult.StatusCode >= 300 {
		return biz.ProviderStatusResult{RawResponse: respBody}, fmt.Errorf("provider status returned %d: %s", httpResult.StatusCode, string(respBody))
	}
	var decoded struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Code    int    `json:"code"`
		Data    struct {
			ExternalBatchID string `json:"externalBatchId"`
			TotalCount      int    `json:"totalCount"`
			SuccessCount    int    `json:"successCount"`
			FailedCount     int    `json:"failedCount"`
			Questions       []struct {
				QuestionID string `json:"questionId"`
				Status     int    `json:"status"`
			} `json:"questions"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return biz.ProviderStatusResult{RawResponse: respBody}, fmt.Errorf("decode provider status: %w", err)
	}
	if decoded.Code != 200 && decoded.Code != 0 {
		return biz.ProviderStatusResult{RawResponse: respBody}, fmt.Errorf("provider status failed: %s", decoded.Message)
	}
	statusResult := biz.ProviderStatusResult{
		ExternalBatchID: decoded.Data.ExternalBatchID,
		TotalCount:      decoded.Data.TotalCount,
		SuccessCount:    decoded.Data.SuccessCount,
		FailedCount:     decoded.Data.FailedCount,
		RawResponse:     respBody,
	}
	if statusResult.ExternalBatchID == "" {
		statusResult.ExternalBatchID = externalBatchID
	}
	for _, question := range decoded.Data.Questions {
		statusResult.Questions = append(statusResult.Questions, biz.ProviderQuestionStatus{
			QuestionID: question.QuestionID,
			Status:     question.Status,
		})
	}
	return statusResult, nil
}

func (p *HTTPProvider) FetchResultFiles(ctx context.Context, externalBatchID string) (biz.ProviderFilesResult, error) {
	if p.cfg.FilesURL == "" {
		return biz.ProviderFilesResult{}, fmt.Errorf("provider files_url is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, expandExternalBatchID(p.cfg.FilesURL, externalBatchID), nil)
	if err != nil {
		return biz.ProviderFilesResult{}, err
	}
	p.setAuthHeader(req)
	httpResult, err := p.do(req, 1<<20,
		"provider_code", p.cfg.Code,
		"operation", "fetch_result_files",
		"external_batch_id", externalBatchID,
	)
	if err != nil {
		return biz.ProviderFilesResult{}, err
	}
	respBody := httpResult.Body
	if httpResult.StatusCode < 200 || httpResult.StatusCode >= 300 {
		return biz.ProviderFilesResult{RawResponse: respBody}, fmt.Errorf("provider files returned %d: %s", httpResult.StatusCode, string(respBody))
	}
	var decoded struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Code    int    `json:"code"`
		Data    struct {
			ExternalBatchID string   `json:"externalBatchId"`
			FileURLs        []string `json:"fileUrls"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return biz.ProviderFilesResult{RawResponse: respBody}, fmt.Errorf("decode provider files: %w", err)
	}
	if decoded.Code != 200 && decoded.Code != 0 {
		return biz.ProviderFilesResult{RawResponse: respBody}, fmt.Errorf("provider files failed: %s", decoded.Message)
	}
	filesResult := biz.ProviderFilesResult{
		ExternalBatchID: decoded.Data.ExternalBatchID,
		FileURLs:        decoded.Data.FileURLs,
		RawResponse:     respBody,
	}
	if filesResult.ExternalBatchID == "" {
		filesResult.ExternalBatchID = externalBatchID
	}
	return filesResult, nil
}

func (p *HTTPProvider) CompletionFiles(ctx context.Context, task *biz.Task) (biz.ProviderCompletionFilesResult, error) {
	return p.completion.CompletionFiles(ctx, p, task)
}

func (p *HTTPProvider) DownloadResultFile(ctx context.Context, fileURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, err
	}
	result, err := p.do(req, 64<<20,
		"provider_code", p.cfg.Code,
		"operation", "download_result_file",
	)
	if err != nil {
		return nil, err
	}
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		return nil, fmt.Errorf("download result file returned %d: %s", result.StatusCode, string(result.Body))
	}
	return result.Body, nil
}

func (p *HTTPProvider) do(req *http.Request, maxReadBytes int64, attrs ...any) (httphelper.Result, error) {
	if err := p.limiter.Wait(req.Context()); err != nil {
		return httphelper.Result{}, err
	}
	select {
	case p.semaphore <- struct{}{}:
		defer func() { <-p.semaphore }()
	case <-req.Context().Done():
		return httphelper.Result{}, req.Context().Err()
	}
	return p.client.DoRead(req.Context(), req, maxReadBytes, attrs...)
}

func (p *HTTPProvider) setAuthHeader(req *http.Request) {
	if p.cfg.Token != "" {
		req.Header.Set("X-Provider-Token", p.cfg.Token)
		return
	}
	if p.cfg.APIKey != "" {
		req.Header.Set("X-API-Key", p.cfg.APIKey)
	}
}

func expandExternalBatchID(template string, externalBatchID string) string {
	return strings.ReplaceAll(template, "{externalBatchId}", externalBatchID)
}

func (r *Registry) Token(_ context.Context, code string) (string, bool) {
	cfg, ok := r.configs[code]
	if !ok {
		return "", false
	}
	if cfg.Token != "" {
		return cfg.Token, true
	}
	return cfg.APIKey, cfg.APIKey != ""
}

func (r *Registry) GetSettings(_ context.Context, code string) (biz.ProviderSettings, bool) {
	cfg, ok := r.configs[code]
	if !ok {
		return biz.ProviderSettings{}, false
	}
	return biz.ProviderSettings{
		ProviderCode:     cfg.Code,
		BucketPathPrefix: cfg.BucketPathPrefix,
	}, true
}
