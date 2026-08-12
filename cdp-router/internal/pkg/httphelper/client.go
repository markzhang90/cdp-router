package httphelper

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/GitMarkZhang/cdp-router/internal/pkg/trace"
)

const defaultBodyLogLimit = 2048

type Client struct {
	client       *http.Client
	name         string
	bodyLogLimit int
}

type Option func(*Client)

type Result struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	Latency    time.Duration
}

func NewClient(name string, timeout time.Duration, opts ...Option) *Client {
	c := &Client{
		name:         name,
		bodyLogLimit: defaultBodyLogLimit,
		client: &http.Client{
			Timeout:   timeout,
			Transport: NewPooledTransport(),
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func WithHTTPClient(client *http.Client) Option {
	return func(c *Client) {
		if client != nil {
			c.client = client
		}
	}
}

func WithBodyLogLimit(limit int) Option {
	return func(c *Client) {
		if limit >= 0 {
			c.bodyLogLimit = limit
		}
	}
}

func NewPooledTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   50,
		MaxConnsPerHost:       100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

func (c *Client) DoRead(ctx context.Context, req *http.Request, maxReadBytes int64, attrs ...any) (Result, error) {
	if ctx == nil {
		ctx = req.Context()
	}
	if req.Context() != ctx {
		req = req.WithContext(ctx)
	}
	startedAt := time.Now()
	baseAttrs := []any{
		"trace_id", trace.RequestID(ctx),
		"http_client", c.name,
		"method", req.Method,
		"url", SafeURL(req.URL.String()),
		"request_headers", RedactHeaders(req.Header),
		"request_body", c.requestBodySummary(req),
	}
	baseAttrs = append(baseAttrs, attrs...)
	slog.InfoContext(ctx, "external http request started", baseAttrs...)

	resp, err := c.client.Do(req)
	latency := time.Since(startedAt)
	if err != nil {
		slog.WarnContext(ctx, "external http request failed", append(baseAttrs,
			"latency_ms", latency.Milliseconds(),
			"error", err,
		)...)
		return Result{Latency: latency}, err
	}
	defer resp.Body.Close()

	if maxReadBytes <= 0 {
		maxReadBytes = 1 << 20
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxReadBytes))
	result := Result{
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
		Body:       body,
		Latency:    latency,
	}
	resultAttrs := append(baseAttrs,
		"status_code", resp.StatusCode,
		"response_headers", RedactHeaders(resp.Header),
		"response_body", c.bodySummary(body),
		"response_bytes", len(body),
		"latency_ms", latency.Milliseconds(),
	)
	if readErr != nil {
		slog.WarnContext(ctx, "external http response read failed", append(resultAttrs, "error", readErr)...)
		return result, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.WarnContext(ctx, "external http request completed with error", resultAttrs...)
		return result, nil
	}
	slog.InfoContext(ctx, "external http request completed", resultAttrs...)
	return result, nil
}

func (c *Client) requestBodySummary(req *http.Request) string {
	if req == nil || req.Body == nil || req.GetBody == nil || c.bodyLogLimit == 0 {
		return ""
	}
	body, err := req.GetBody()
	if err != nil {
		return ""
	}
	defer body.Close()
	content, _ := io.ReadAll(io.LimitReader(body, int64(c.bodyLogLimit)+1))
	return c.bodySummary(content)
}

func (c *Client) bodySummary(body []byte) string {
	if c.bodyLogLimit == 0 || len(body) == 0 {
		return ""
	}
	truncated := len(body) > c.bodyLogLimit
	if truncated {
		body = body[:c.bodyLogLimit]
	}
	summary := redactBody(body)
	if truncated {
		return summary + "...[truncated]"
	}
	return summary
}

func RedactHeaders(headers http.Header) map[string]string {
	out := make(map[string]string, len(headers))
	for key, values := range headers {
		if isSensitiveKey(key) {
			out[key] = "[REDACTED]"
			continue
		}
		out[key] = strings.Join(values, ",")
	}
	return out
}

func SafeURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func redactBody(body []byte) string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return ""
	}
	if !isText(trimmed) {
		return "[binary " + strconv.Itoa(len(body)) + " bytes]"
	}
	var value any
	if json.Unmarshal(trimmed, &value) == nil {
		redactJSON(value)
		redacted, err := json.Marshal(value)
		if err == nil {
			return string(redacted)
		}
	}
	return string(trimmed)
}

func isText(body []byte) bool {
	if !utf8.Valid(body) {
		return false
	}
	for len(body) > 0 {
		r, size := utf8.DecodeRune(body)
		if r == utf8.RuneError && size == 1 {
			return false
		}
		if r != '\n' && r != '\r' && r != '\t' && unicode.IsControl(r) {
			return false
		}
		body = body[size:]
	}
	return true
}

func redactJSON(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, val := range typed {
			if isSensitiveKey(key) {
				typed[key] = "[REDACTED]"
				continue
			}
			redactJSON(val)
		}
	case []any:
		for _, item := range typed {
			redactJSON(item)
		}
	}
}

func isSensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", ""), "_", ""))
	switch normalized {
	case "authorization", "cookie", "setcookie", "xprovidertoken", "token", "apikey", "accesskey", "accesskeyid", "accesskeysecret", "secret", "signature", "uploadurl", "fileurl", "fileurls":
		return true
	default:
		return strings.Contains(normalized, "token") ||
			strings.Contains(normalized, "secret") ||
			strings.Contains(normalized, "signature")
	}
}
