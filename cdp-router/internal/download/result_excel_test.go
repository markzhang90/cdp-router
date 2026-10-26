package download

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/xuri/excelize/v2"
)

func TestBuildResultWorkbookFromItemResults(t *testing.T) {
	recommendedQuestions, _ := json.Marshal([]string{"推荐追问一", "推荐追问二"})
	references, _ := json.Marshal([]map[string]string{
		{
			"url":                "https://example.com/source-1",
			"title":              "引用标题一",
			"content":            "引用内容一",
			"publishedAtText":    "2026-07-23",
			"siteName":           "示例网站",
			"authorityLevel":     "Tier 1",
			"siteCategory":       "新闻",
			"author":             "作者一",
			"authorCategory":     "媒体作者",
			"authorIdentityType": "个人",
		},
		{
			"url":                "https://example.com/source-2",
			"title":              "12345",
			"content":            "引用内容二",
			"publishedAtText":    "07-24-26",
			"siteName":           "67890",
			"authorityLevel":     "Tier 2",
			"siteCategory":       "社区",
			"author":             "作者二",
			"authorCategory":     "社区作者",
			"authorIdentityType": "机构",
		},
	})
	generatedAt := time.Date(2026, 7, 23, 18, 13, 0, 0, time.FixedZone("CST", 8*60*60))
	content, err := BuildResultWorkbook([]biz.TaskItemResultView{{
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
	assertRowPrefix(t, answerRows[0], []string{"itemId", "轮次", "问题", "平台", "深度思考过程", "AI回答", "AI生成时间", "推荐问题", "设备ID", "录屏url"})
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
	assertRowPrefix(t, referenceRows[0], []string{"itemId", "轮次", "url", "标题", "内容", "发布日期", "网站名称", "网站权威性等级", "网站分类", "作者", "作者分类", "作者身份类型"})
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
