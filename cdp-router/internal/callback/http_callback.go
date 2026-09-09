package callback

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/GitMarkZhang/cdp-router/internal/pkg/httphelper"
)

type HTTPClient struct {
	client *httphelper.Client
}

func NewHTTPClient(timeout time.Duration) *HTTPClient {
	return &HTTPClient{client: httphelper.NewClient("business_callback", timeout)}
}

func (c *HTTPClient) Code() string {
	return "DEFAULT_HTTP"
}

func (c *HTTPClient) ResolveMode(_ *biz.Task) string {
	return "DEFAULT_HTTP"
}

func (c *HTTPClient) Deliver(ctx context.Context, task *biz.Task, payload biz.CallbackDelivery) (biz.CallbackResult, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return biz.CallbackResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, task.CallbackURL, bytes.NewReader(body))
	if err != nil {
		return biz.CallbackResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	result, err := c.client.DoRead(ctx, req, 1<<20,
		"operation", "deliver_business_callback",
		"batch_no", payload.BatchNo,
		"external_batch_id", payload.ExternalBatchID,
	)
	if err != nil {
		return biz.CallbackResult{TargetURL: task.CallbackURL, RequestPayload: body}, err
	}
	respBody := string(result.Body)
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		return biz.CallbackResult{TargetURL: task.CallbackURL, StatusCode: result.StatusCode, ResponseBody: respBody, RequestPayload: body}, fmt.Errorf("business callback returned %d: %s", result.StatusCode, respBody)
	}
	return biz.CallbackResult{TargetURL: task.CallbackURL, StatusCode: result.StatusCode, ResponseBody: respBody, RequestPayload: body}, nil
}

var _ biz.CallbackClient = (*HTTPClient)(nil)
