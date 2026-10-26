package callback

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMatchYangshiKeyword(t *testing.T) {
	tests := []struct {
		name string
		code string
		want bool
	}{
		{name: "exact", code: "yangshi", want: true},
		{name: "dev suffix", code: "yangshi_dev", want: true},
		{name: "keyword in middle", code: "platform_yangshi_dev", want: true},
		{name: "case insensitive", code: "Yangshi_Test", want: true},
		{name: "trim spaces", code: " yangshi ", want: true},
		{name: "other platform", code: "mainland", want: false},
		{name: "empty", code: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchYangshiKeyword(tt.code); got != tt.want {
				t.Fatalf("matchYangshiKeyword(%q)=%v, want %v", tt.code, got, tt.want)
			}
		})
	}
}

func TestYangshiModeMatchesKeyword(t *testing.T) {
	if !matchYangshiKeyword("YANGSHI") {
		t.Fatal("expected YANGSHI callback mode to match yangshi adapter")
	}
}

func TestYangshiUploadClientUsesSeparateTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(25 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := NewYangshiClient(time.Nanosecond, time.Second, nil)
	err := c.putFile(context.Background(), yangshiUploadURL{UploadURL: server.URL, Method: http.MethodPut}, []byte("xlsx"), "batch-no")
	if err != nil {
		t.Fatalf("putFile should use upload timeout instead of API timeout: %v", err)
	}
}

func TestYangshiNotifyURL(t *testing.T) {
	cfg := yangshiConfig{BaseURL: "https://staging-api-growth.wiredcraft.cn/api2", ProviderCode: "mpzdata2"}
	customURL := "https://example.com/business/callback"
	if got := yangshiNotifyURL(customURL, cfg); got != customURL {
		t.Fatalf("yangshiNotifyURL custom=%q, want %q", got, customURL)
	}
	if got := yangshiNotifyURL("custom://yangshi_dev", cfg); got != "https://staging-api-growth.wiredcraft.cn/api2/external/callback/mpzdata2" {
		t.Fatalf("yangshiNotifyURL fallback=%q", got)
	}
}

func TestYangshiCallbackFileURLStripsSignedQuery(t *testing.T) {
	upload := yangshiUploadURL{UploadURL: "https://bucket.oss-cn-shanghai.aliyuncs.com/path/result.xlsx?Expires=1&Signature=secret#fragment"}
	if got := yangshiCallbackFileURL(upload); got != "https://bucket.oss-cn-shanghai.aliyuncs.com/path/result.xlsx" {
		t.Fatalf("yangshiCallbackFileURL=%q", got)
	}
}
