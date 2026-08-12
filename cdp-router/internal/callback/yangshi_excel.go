package callback

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/xuri/excelize/v2"
)

var yangshiAnswerHeaders = []string{
	"itemId",
	"轮次",
	"问题",
	"平台",
	"深度思考过程",
	"AI回答",
	"AI生成时间",
	"推荐问题",
	"设备ID",
	"录屏url",
}

var yangshiReferenceHeaders = []string{
	"itemId",
	"轮次",
	"url",
	"标题",
	"内容",
	"发布日期",
	"网站名称",
	"网站权威性等级",
	"网站分类",
	"作者",
	"作者分类",
	"作者身份类型",
}

type yangshiReferenceRow struct {
	URL                string `json:"url"`
	Title              string `json:"title"`
	Content            string `json:"content"`
	PublishedAtText    string `json:"publishedAtText"`
	SiteName           string `json:"siteName"`
	AuthorityLevel     string `json:"authorityLevel"`
	SiteCategory       string `json:"siteCategory"`
	Author             string `json:"author"`
	AuthorCategory     string `json:"authorCategory"`
	AuthorIdentityType string `json:"authorIdentityType"`
}

func buildYangshiResultWorkbook(items []biz.TaskItemResultView) ([]byte, error) {
	if len(items) == 0 {
		return nil, fmt.Errorf("no task item results available for yangshi callback")
	}

	file := excelize.NewFile()
	defer func() { _ = file.Close() }()
	if err := file.SetSheetName("Sheet1", "AI回答"); err != nil {
		return nil, err
	}
	if _, err := file.NewSheet("溯源信息"); err != nil {
		return nil, err
	}
	if err := writeYangshiHeader(file, "AI回答", yangshiAnswerHeaders); err != nil {
		return nil, err
	}
	if err := writeYangshiHeader(file, "溯源信息", yangshiReferenceHeaders); err != nil {
		return nil, err
	}

	dateStyle, err := file.NewStyle(&excelize.Style{NumFmt: 14})
	if err != nil {
		return nil, err
	}

	answerRow := 2
	referenceRow := 2
	for _, item := range items {
		if err := writeYangshiAnswerRow(file, answerRow, item); err != nil {
			return nil, err
		}
		answerRow++

		references, err := parseYangshiReferences(item.ReferencesJSON)
		if err != nil {
			return nil, fmt.Errorf("parse references for item %s: %w", item.ItemID, err)
		}
		for _, reference := range references {
			if err := writeYangshiReferenceRow(file, referenceRow, item.ItemID, item.Seq, reference, dateStyle); err != nil {
				return nil, err
			}
			referenceRow++
		}
	}

	file.SetActiveSheet(0)
	var buffer bytes.Buffer
	if err := file.Write(&buffer); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func writeYangshiHeader(file *excelize.File, sheet string, headers []string) error {
	values := make([]any, len(headers))
	for i, header := range headers {
		values[i] = header
	}
	return file.SetSheetRow(sheet, "A1", &values)
}

func writeYangshiAnswerRow(file *excelize.File, rowIndex int, item biz.TaskItemResultView) error {
	values := []any{
		yangshiNumericID(item.ItemID),
		item.Seq,
		item.Question,
		item.PlatformCode,
		item.ThinkingProcess,
		item.AnswerText,
		yangshiGeneratedAtText(item),
		yangshiRecommendedQuestionsText(item.RecommendedQuestions),
		yangshiMaybeNumber(item.DeviceID),
		item.RecordingURL,
	}
	cell, err := excelize.CoordinatesToCellName(1, rowIndex)
	if err != nil {
		return err
	}
	return file.SetSheetRow("AI回答", cell, &values)
}

func writeYangshiReferenceRow(file *excelize.File, rowIndex int, itemID string, seq int, reference yangshiReferenceRow, dateStyle int) error {
	publishedAt := any(reference.PublishedAtText)
	if parsed, ok := parseYangshiDate(reference.PublishedAtText); ok {
		publishedAt = parsed
	}
	values := []any{
		yangshiNumericID(itemID),
		seq,
		reference.URL,
		yangshiMaybeNumber(reference.Title),
		reference.Content,
		publishedAt,
		yangshiMaybeNumber(reference.SiteName),
		reference.AuthorityLevel,
		reference.SiteCategory,
		reference.Author,
		reference.AuthorCategory,
		reference.AuthorIdentityType,
	}
	cell, err := excelize.CoordinatesToCellName(1, rowIndex)
	if err != nil {
		return err
	}
	if err := file.SetSheetRow("溯源信息", cell, &values); err != nil {
		return err
	}
	if _, ok := publishedAt.(time.Time); ok {
		dateCell, err := excelize.CoordinatesToCellName(6, rowIndex)
		if err != nil {
			return err
		}
		return file.SetCellStyle("溯源信息", dateCell, dateCell, dateStyle)
	}
	return nil
}

func parseYangshiReferences(raw json.RawMessage) ([]yangshiReferenceRow, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var references []yangshiReferenceRow
	if err := json.Unmarshal(raw, &references); err != nil {
		return nil, err
	}
	return references, nil
}

func yangshiGeneratedAtText(item biz.TaskItemResultView) string {
	if strings.TrimSpace(item.GeneratedAtText) != "" {
		return strings.TrimSpace(item.GeneratedAtText)
	}
	if item.GeneratedAt.IsZero() {
		return ""
	}
	return item.GeneratedAt.Format("2006年01月02日15:04")
}

func yangshiRecommendedQuestionsText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var questions []string
	if err := json.Unmarshal(raw, &questions); err == nil {
		out := make([]string, 0, len(questions))
		for _, question := range questions {
			question = strings.TrimSpace(question)
			if question != "" {
				out = append(out, question)
			}
		}
		return strings.Join(out, "\n")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return strings.TrimSpace(value)
	}
	return ""
}

func yangshiNumericID(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || hasLeadingZero(value) {
		return value
	}
	return parsed
}

func yangshiMaybeNumber(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || hasLeadingZero(value) {
		return value
	}
	return parsed
}

func hasLeadingZero(value string) bool {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "+")
	value = strings.TrimPrefix(value, "-")
	return len(value) > 1 && strings.HasPrefix(value, "0")
}

func parseYangshiDate(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	location := time.FixedZone("CST", 8*60*60)
	layouts := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
		"2006/01/02 15:04:05",
		"2006/01/02",
		"01-02-06",
		"01/02/06",
		"2006年01月02日15:04",
		"2006年01月02日",
	}
	for _, layout := range layouts {
		if parsed, err := time.ParseInLocation(layout, value, location); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}
