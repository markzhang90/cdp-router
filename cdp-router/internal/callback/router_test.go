package callback

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/xuri/excelize/v2"
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

func TestYangshiUploadClientHasNoTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(25 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := NewYangshiClient(time.Nanosecond, nil)
	err := c.putFile(context.Background(), yangshiUploadURL{UploadURL: server.URL, Method: http.MethodPut}, []byte("xlsx"), "batch-no")
	if err != nil {
		t.Fatalf("putFile with no upload timeout failed: %v", err)
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

func TestBuildYangshiResultWorkbookFromItemResults(t *testing.T) {
	recommendedQuestions, _ := json.Marshal([]string{"推荐追问一", "推荐追问二"})
	references, _ := json.Marshal([]yangshiReferenceRow{
		{
			URL:                "https://example.com/source-1",
			Title:              "引用标题一",
			Content:            "引用内容一",
			PublishedAtText:    "2026-07-23",
			SiteName:           "示例网站",
			AuthorityLevel:     "Tier 1",
			SiteCategory:       "新闻",
			Author:             "作者一",
			AuthorCategory:     "媒体作者",
			AuthorIdentityType: "个人",
		},
		{
			URL:                "https://example.com/source-2",
			Title:              "12345",
			Content:            "引用内容二",
			PublishedAtText:    "07-24-26",
			SiteName:           "67890",
			AuthorityLevel:     "Tier 2",
			SiteCategory:       "社区",
			Author:             "作者二",
			AuthorCategory:     "社区作者",
			AuthorIdentityType: "机构",
		},
	})
	generatedAt := time.Date(2026, 7, 23, 18, 13, 0, 0, time.FixedZone("CST", 8*60*60))
	content, err := buildYangshiResultWorkbook([]biz.TaskItemResultView{{
		ItemID:               "864777",
		PlatformCode:         "yuanbao",
		Seq:                  1,
		Question:             "请评价某产品的优缺点",
		AnswerText:           "这是一条测试回答",
		ThinkingProcess:      "这是一段思考过程",
		GeneratedAt:          generatedAt,
		GeneratedAtText:      "2026年07月23日18:13",
		RecommendedQuestions: recommendedQuestions,
		DeviceID:             "123456",
		RecordingURL:         "https://example.com/recording",
		ReferencesJSON:       references,
		ReferenceCount:       2,
		ParseStatus:          "SUCCESS",
	}})
	if err != nil {
		t.Fatal(err)
	}

	book, err := excelize.OpenReader(bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = book.Close() }()
	if got := book.GetSheetList(); len(got) != 2 || got[0] != "AI回答" || got[1] != "溯源信息" {
		t.Fatalf("sheet list=%v", got)
	}
	answerRows, err := book.GetRows("AI回答")
	if err != nil {
		t.Fatal(err)
	}
	if len(answerRows) != 2 {
		t.Fatalf("AI回答 rows=%d, want 2", len(answerRows))
	}
	assertRowPrefix(t, answerRows[0], yangshiAnswerHeaders)
	assertRowPrefix(t, answerRows[1], []string{
		"864777",
		"1",
		"请评价某产品的优缺点",
		"yuanbao",
		"这是一段思考过程",
		"这是一条测试回答",
		"2026年07月23日18:13",
		"推荐追问一\n推荐追问二",
		"123456",
		"https://example.com/recording",
	})

	referenceRows, err := book.GetRows("溯源信息")
	if err != nil {
		t.Fatal(err)
	}
	if len(referenceRows) != 3 {
		t.Fatalf("溯源信息 rows=%d, want 3", len(referenceRows))
	}
	assertRowPrefix(t, referenceRows[0], yangshiReferenceHeaders)
	if referenceRows[1][0] != "864777" || referenceRows[1][1] != "1" || referenceRows[1][2] != "https://example.com/source-1" || referenceRows[1][5] == "" {
		t.Fatalf("unexpected first reference row=%v", referenceRows[1])
	}
	if referenceRows[2][3] != "12345" || referenceRows[2][6] != "67890" {
		t.Fatalf("numeric-looking reference fields were not preserved: %v", referenceRows[2])
	}
}

func assertRowPrefix(t *testing.T, got []string, want []string) {
	t.Helper()
	if len(got) < len(want) {
		t.Fatalf("row length=%d, want at least %d row=%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row[%d]=%q, want %q row=%v", i, got[i], want[i], got)
		}
	}
}
