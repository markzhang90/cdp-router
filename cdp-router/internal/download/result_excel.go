package download

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

var answerHeaders = []string{
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

var referenceHeaders = []string{
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

type resultReference struct {
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

// BuildResultWorkbook renders result data as an Excel file for download or upload.
func BuildResultWorkbook(items []biz.TaskItemResultView) ([]byte, error) {
	if len(items) == 0 {
		return nil, fmt.Errorf("no task item results available for export")
	}
	file := excelize.NewFile()
	defer func() { _ = file.Close() }()
	if err := WriteSheets(file, items); err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	if err := file.Write(&buffer); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// WriteSheets writes the shared result template into a new workbook.
// Empty results produce headers so admin exports can include unexecuted tasks.
func WriteSheets(file *excelize.File, items []biz.TaskItemResultView) error {
	if err := file.SetSheetName("Sheet1", "AI回答"); err != nil {
		return err
	}
	if _, err := file.NewSheet("溯源信息"); err != nil {
		return err
	}
	if err := writeHeader(file, "AI回答", answerHeaders); err != nil {
		return err
	}
	if err := writeHeader(file, "溯源信息", referenceHeaders); err != nil {
		return err
	}

	dateStyle, err := file.NewStyle(&excelize.Style{NumFmt: 14})
	if err != nil {
		return err
	}

	products := make([]productAnswer, 0, len(items))
	answerRow := 2
	referenceRow := 2
	for _, item := range items {
		products = append(products, productAnswer{
			PlatformCode: item.PlatformCode,
			ItemID:       item.ItemID,
			Seq:          item.Seq,
			Question:     item.Question,
			ProductCards: item.ProductCards,
		})
		if err := writeAnswerRow(file, answerRow, item); err != nil {
			return err
		}
		answerRow++

		references, err := parseReferences(item.ReferencesJSON)
		if err != nil {
			return fmt.Errorf("parse references for item %s: %w", item.ItemID, err)
		}
		for _, reference := range references {
			if err := writeReferenceRow(file, referenceRow, item.ItemID, item.Seq, reference, dateStyle); err != nil {
				return err
			}
			referenceRow++
		}
	}

	if err := writeProductSheet(file, products); err != nil {
		return fmt.Errorf("write product cards: %w", err)
	}

	file.SetActiveSheet(0)
	return nil
}

func writeHeader(file *excelize.File, sheet string, headers []string) error {
	values := make([]any, len(headers))
	for i, header := range headers {
		values[i] = header
	}
	return file.SetSheetRow(sheet, "A1", &values)
}

func writeAnswerRow(file *excelize.File, rowIndex int, item biz.TaskItemResultView) error {
	values := []any{
		numericID(item.ItemID),
		item.Seq,
		item.Question,
		item.PlatformCode,
		item.ThinkingProcess,
		item.AnswerText,
		generatedAtText(item),
		recommendedQuestionsText(item.RecommendedQuestions),
		maybeNumber(item.DeviceID),
		item.RecordingURL,
	}
	cell, err := excelize.CoordinatesToCellName(1, rowIndex)
	if err != nil {
		return err
	}
	return file.SetSheetRow("AI回答", cell, &values)
}

func writeReferenceRow(file *excelize.File, rowIndex int, itemID string, seq int, reference resultReference, dateStyle int) error {
	publishedAt := any(reference.PublishedAtText)
	if parsed, ok := parseDate(reference.PublishedAtText); ok {
		publishedAt = parsed
	}
	values := []any{
		numericID(itemID),
		seq,
		reference.URL,
		maybeNumber(reference.Title),
		reference.Content,
		publishedAt,
		maybeNumber(reference.SiteName),
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

func parseReferences(raw json.RawMessage) ([]resultReference, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var references []resultReference
	if err := json.Unmarshal(raw, &references); err != nil {
		return nil, err
	}
	return references, nil
}

func generatedAtText(item biz.TaskItemResultView) string {
	if strings.TrimSpace(item.GeneratedAtText) != "" {
		return strings.TrimSpace(item.GeneratedAtText)
	}
	if item.GeneratedAt.IsZero() {
		return ""
	}
	return item.GeneratedAt.Format("2006年01月02日15:04")
}

func recommendedQuestionsText(raw json.RawMessage) string {
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

func numericID(value string) any {
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

func maybeNumber(value string) any {
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

func parseDate(value string) (time.Time, bool) {
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
