package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestSanitizedBodySummaryRedactsSensitiveJSONFields(t *testing.T) {
	body := []byte(`{"apiKey":"secret-key","password":"secret-password","nested":{"fileUrls":["https://bucket/object.xlsx?Expires=1&Signature=abc"],"ok":"visible"}}`)

	got := sanitizedBodySummary("application/json", body)

	for _, forbidden := range []string{"secret-key", "secret-password", "Signature=abc"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("sanitized body contains %q: %s", forbidden, got)
		}
	}
	for _, want := range []string{`"apiKey":"[REDACTED]"`, `"password":"[REDACTED]"`, `"fileUrls":"[REDACTED]"`, `"ok":"visible"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("sanitized body missing %q: %s", want, got)
		}
	}
}

func TestSanitizedHeadersRedactsSensitiveHeaders(t *testing.T) {
	headers := http.Header{
		"X-Provider-Token": []string{"provider-token"},
		"Authorization":    []string{"Bearer token"},
		"Content-Type":     []string{"application/json"},
	}

	got := sanitizedHeaders(headers)

	if got["X-Provider-Token"] != "[REDACTED]" || got["Authorization"] != "[REDACTED]" {
		t.Fatalf("sensitive headers not redacted: %+v", got)
	}
	if got["Content-Type"] != "application/json" {
		t.Fatalf("content type header changed: %+v", got)
	}
}
