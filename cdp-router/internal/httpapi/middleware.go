package httpapi

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/GitMarkZhang/cdp-router/internal/pkg/trace"
	"github.com/gin-gonic/gin"
)

const logBodyLimit = 64 << 10

func requestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := c.GetHeader("X-Request-ID")
		if requestID == "" {
			var b [16]byte
			_, _ = rand.Read(b[:])
			requestID = hex.EncodeToString(b[:])
		}
		c.Header("X-Request-ID", requestID)
		c.Set("request_id", requestID)
		c.Request = c.Request.WithContext(trace.WithRequestID(c.Request.Context(), requestID))
		c.Next()
	}
}

func accessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		startedAt := time.Now()
		requestBody := captureRequestBody(c)
		responseWriter := &bodyLogWriter{ResponseWriter: c.Writer}
		c.Writer = responseWriter
		defer func() {
			requestID := trace.RequestID(c.Request.Context())
			if requestID == "" {
				requestID = c.GetString("request_id")
			}
			slog.InfoContext(c.Request.Context(), "http access",
				"trace_id", requestID,
				"method", c.Request.Method,
				"path", c.FullPath(),
				"raw_path", c.Request.URL.Path,
				"status", c.Writer.Status(),
				"latency_ms", time.Since(startedAt).Milliseconds(),
				"client_ip", c.ClientIP(),
				"user_agent", c.Request.UserAgent(),
				"request_bytes", c.Request.ContentLength,
				"response_bytes", c.Writer.Size(),
				"request_headers", sanitizedHeaders(c.Request.Header),
				"request_body", requestBody,
				"response_body", responseWriter.bodySummary(),
			)
		}()

		c.Next()
	}
}

type bodyLogWriter struct {
	gin.ResponseWriter
	body bytes.Buffer
}

func (w *bodyLogWriter) Write(data []byte) (int, error) {
	w.capture(data)
	return w.ResponseWriter.Write(data)
}

func (w *bodyLogWriter) WriteString(data string) (int, error) {
	w.capture([]byte(data))
	return w.ResponseWriter.WriteString(data)
}

func (w *bodyLogWriter) capture(data []byte) {
	if w.body.Len() >= logBodyLimit {
		return
	}
	remaining := logBodyLimit - w.body.Len()
	if len(data) > remaining {
		data = data[:remaining]
	}
	_, _ = w.body.Write(data)
}

func (w *bodyLogWriter) bodySummary() string {
	status := w.Status()
	if status == http.StatusNoContent || status == http.StatusNotModified {
		return ""
	}
	if w.body.Len() == 0 {
		return ""
	}
	contentType := w.Header().Get("Content-Type")
	if !loggableContentType(contentType) {
		return omittedBody("response content type " + contentType)
	}
	body := w.body.Bytes()
	summary := sanitizedBodySummary(contentType, body)
	if w.Size() > logBodyLimit || w.body.Len() >= logBodyLimit {
		return summary + "...[truncated]"
	}
	return summary
}

func captureRequestBody(c *gin.Context) string {
	if c.Request.Body == nil || c.Request.Body == http.NoBody {
		return ""
	}
	contentType := c.Request.Header.Get("Content-Type")
	if !loggableContentType(contentType) {
		return omittedBody("request content type " + contentType)
	}
	if c.Request.ContentLength < 0 {
		return omittedBody("request body length unknown")
	}
	if c.Request.ContentLength > logBodyLimit {
		return omittedBody("request body too large")
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return omittedBody("read request body failed: " + err.Error())
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	return sanitizedBodySummary(contentType, body)
}

func sanitizedHeaders(headers http.Header) map[string]string {
	result := make(map[string]string, len(headers))
	for key, values := range headers {
		if isSensitiveKey(key) {
			result[key] = "[REDACTED]"
			continue
		}
		result[key] = strings.Join(values, ",")
	}
	return result
}

func sanitizedBodySummary(contentType string, body []byte) string {
	if len(body) == 0 {
		return ""
	}
	if isJSONContentType(contentType) {
		var value any
		if err := json.Unmarshal(body, &value); err == nil {
			redactJSON(value)
			if sanitized, err := json.Marshal(value); err == nil {
				return truncateLogBody(string(sanitized))
			}
		}
	}
	return truncateLogBody(string(body))
}

func redactJSON(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			if isSensitiveKey(key) {
				typed[key] = "[REDACTED]"
				continue
			}
			redactJSON(nested)
		}
	case []any:
		for _, nested := range typed {
			redactJSON(nested)
		}
	}
}

func loggableContentType(contentType string) bool {
	if contentType == "" {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	}
	if mediaType == "" {
		return true
	}
	return mediaType == "application/json" ||
		strings.HasSuffix(mediaType, "+json") ||
		strings.HasPrefix(mediaType, "text/") ||
		mediaType == "application/xml" ||
		strings.HasSuffix(mediaType, "+xml")
}

func isJSONContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	}
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json") || mediaType == ""
}

func isSensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
	switch normalized {
	case "authorization", "cookie", "setcookie", "xapikey", "xprovidertoken",
		"apikey", "token", "accesskey", "accesskeyid", "accesskeysecret",
		"secret", "password", "passwordhash", "csrftoken", "xcsrftoken",
		"uploadurl", "downloadurl", "signedurl", "fileurl", "fileurls":
		return true
	}
	return strings.Contains(normalized, "token") ||
		strings.Contains(normalized, "secret") ||
		strings.Contains(normalized, "password") ||
		strings.Contains(normalized, "apikey")
}

func truncateLogBody(value string) string {
	if len(value) <= logBodyLimit {
		return value
	}
	return value[:logBodyLimit] + "...[truncated]"
}

func omittedBody(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return "[omitted]"
	}
	return "[omitted: " + reason + "]"
}
