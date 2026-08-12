package cleaner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/xuri/excelize/v2"
)

type ExcelParser struct{}

func NewExcelParser() *ExcelParser {
	return &ExcelParser{}
}

func (p *ExcelParser) Parse(ctx context.Context, fileName string, content []byte) (*biz.ParsedResult, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if !strings.EqualFold(filepath.Ext(fileName), ".xlsx") {
		payload, err := json.Marshal(map[string]any{
			"fileName": fileName,
			"size":     len(content),
		})
		if err != nil {
			return nil, err
		}
		rowData, _ := json.Marshal(map[string]any{"fileName": fileName, "size": len(content)})
		return &biz.ParsedResult{
			Payload:       payload,
			RowCount:      1,
			ValidRowCount: 1,
			Rows: []biz.ResultRow{{
				SheetName: "file",
				RowIndex:  1,
				RowData:   rowData,
				RowHash:   rowHash(rowData),
			}},
		}, nil
	}
	book, err := excelize.OpenReader(bytes.NewReader(content))
	if err != nil {
		return nil, err
	}
	defer book.Close()

	result := map[string]any{}
	parsedRows := []biz.ResultRow{}
	answerRows := map[string][]answerRow{}
	referenceRows := map[string]map[int][]referenceRow{}
	for _, sheet := range book.GetSheetList() {
		rows, err := book.GetRows(sheet)
		if err != nil {
			return nil, err
		}
		result[sheet] = rows
		if len(rows) == 0 {
			continue
		}
		headers := normalizeHeaders(rows[0])
		for i := 1; i < len(rows); i++ {
			rowMap := map[string]any{}
			for col, header := range headers {
				key := columnName(headers, col)
				if header == "" {
					continue
				}
				if col < len(rows[i]) {
					rowMap[key] = strings.TrimSpace(rows[i][col])
				} else {
					rowMap[key] = ""
				}
			}
			body, err := json.Marshal(rowMap)
			if err != nil {
				return nil, err
			}
			itemID := firstString(rowMap, "itemId", "item_id", "问题ID", "questionId", "question_id")
			seq := 0
			switch strings.TrimSpace(sheet) {
			case "AI回答":
				if itemID != "" {
					answer := parseAnswerRow(rowMap, i+1)
					answer.Seq = len(answerRows[itemID]) + 1
					seq = answer.Seq
					answerRows[itemID] = append(answerRows[itemID], answer)
				}
			case "溯源信息":
				if itemID != "" {
					seq = firstInt(rowMap, "seq", "轮次", "问答轮次")
					if referenceRows[itemID] == nil {
						referenceRows[itemID] = map[int][]referenceRow{}
					}
					referenceRows[itemID][seq] = append(referenceRows[itemID][seq], parseReferenceRow(rowMap))
				}
			}
			parsedRows = append(parsedRows, biz.ResultRow{
				SheetName: sheet,
				RowIndex:  i + 1,
				ItemID:    itemID,
				Seq:       seq,
				RowData:   body,
				RowHash:   rowHash(body),
			})
		}
	}
	itemResults, payload, err := buildItemResults(answerRows, referenceRows)
	if err != nil {
		return nil, err
	}
	return &biz.ParsedResult{
		Payload:       payload,
		Rows:          parsedRows,
		ItemResults:   itemResults,
		RowCount:      len(parsedRows),
		ValidRowCount: len(parsedRows),
	}, nil
}

var _ biz.ResultParser = (*ExcelParser)(nil)

func normalizeHeaders(headers []string) []string {
	normalized := make([]string, len(headers))
	for i, header := range headers {
		normalized[i] = strings.TrimSpace(header)
	}
	return normalized
}

func columnName(headers []string, index int) string {
	if index < len(headers) && headers[index] != "" {
		return headers[index]
	}
	return "col_" + strconv.Itoa(index+1)
}

func firstString(row map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := row[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstInt(row map[string]any, keys ...string) int {
	for _, key := range keys {
		value, ok := row[key].(string)
		if !ok {
			continue
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err == nil && parsed > 0 {
			return parsed
		}
	}
	return 0
}

func rowHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

type answerRow struct {
	ItemID               string
	Seq                  int
	Question             string
	PlatformCode         string
	ThinkingProcess      string
	AnswerText           string
	GeneratedAt          time.Time
	GeneratedAtText      string
	RecommendedQuestions []string
	DeviceID             string
	RecordingURL         string
	RowIndex             int
}

type referenceRow struct {
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

func parseAnswerRow(row map[string]any, rowIndex int) answerRow {
	generatedAtText := firstString(row, "AI生成时间", "generatedAt", "generated_at")
	return answerRow{
		ItemID:               firstString(row, "itemId", "item_id", "问题ID", "questionId", "question_id"),
		Question:             firstString(row, "问题", "question"),
		PlatformCode:         firstString(row, "平台", "platformCode", "platform_code"),
		ThinkingProcess:      firstString(row, "深度思考过程", "thinkingProcess", "thinking_process"),
		AnswerText:           firstString(row, "AI回答", "answer", "answerText", "answer_text"),
		GeneratedAt:          parseTime(generatedAtText),
		GeneratedAtText:      generatedAtText,
		RecommendedQuestions: splitRecommendedQuestions(firstString(row, "推荐问题", "recommendedQuestions", "recommended_questions")),
		DeviceID:             firstString(row, "设备ID", "deviceId", "device_id"),
		RecordingURL:         firstString(row, "录屏url", "录屏URL", "recordingUrl", "recording_url"),
		RowIndex:             rowIndex,
	}
}

func parseReferenceRow(row map[string]any) referenceRow {
	return referenceRow{
		URL:                firstString(row, "url", "URL"),
		Title:              firstString(row, "标题", "title"),
		Content:            firstString(row, "内容", "content"),
		PublishedAtText:    firstString(row, "发布日期", "publishedAt", "published_at"),
		SiteName:           firstString(row, "网站名称", "siteName", "site_name"),
		AuthorityLevel:     firstString(row, "网站权威性等级", "authorityLevel", "authority_level"),
		SiteCategory:       firstString(row, "网站分类", "siteCategory", "site_category"),
		Author:             firstString(row, "作者", "author"),
		AuthorCategory:     firstString(row, "作者分类", "authorCategory", "author_category"),
		AuthorIdentityType: firstString(row, "作者身份类型", "authorIdentityType", "author_identity_type"),
	}
}

func buildItemResults(answerRows map[string][]answerRow, referenceRows map[string]map[int][]referenceRow) ([]biz.TaskItemResult, json.RawMessage, error) {
	items := make([]biz.TaskItemResult, 0, len(answerRows))
	payloadItems := make([]json.RawMessage, 0, len(answerRows))
	itemIDs := make([]string, 0, len(answerRows))
	for itemID := range answerRows {
		itemIDs = append(itemIDs, itemID)
	}
	sort.Strings(itemIDs)
	for _, itemID := range itemIDs {
		for _, answer := range answerRows[itemID] {
			references := referencesForSeq(referenceRows[itemID], answer.Seq)
			referencesJSON, err := json.Marshal(references)
			if err != nil {
				return nil, nil, err
			}
			recommendedQuestions, err := json.Marshal(answer.RecommendedQuestions)
			if err != nil {
				return nil, nil, err
			}
			cleanedPayload, err := json.Marshal(map[string]any{
				"schemaVersion":        "mpzdata.q2a_result.v1",
				"itemId":               answer.ItemID,
				"seq":                  answer.Seq,
				"platformCode":         answer.PlatformCode,
				"question":             answer.Question,
				"answerText":           answer.AnswerText,
				"thinkingProcess":      answer.ThinkingProcess,
				"generatedAt":          formatTime(answer.GeneratedAt),
				"generatedAtText":      answer.GeneratedAtText,
				"recommendedQuestions": answer.RecommendedQuestions,
				"deviceId":             answer.DeviceID,
				"recordingUrl":         answer.RecordingURL,
				"references":           references,
			})
			if err != nil {
				return nil, nil, err
			}
			itemResult := biz.TaskItemResult{
				ItemID:               answer.ItemID,
				Seq:                  answer.Seq,
				PlatformCode:         answer.PlatformCode,
				Question:             answer.Question,
				AnswerText:           answer.AnswerText,
				ThinkingProcess:      answer.ThinkingProcess,
				GeneratedAt:          answer.GeneratedAt,
				GeneratedAtText:      answer.GeneratedAtText,
				RecommendedQuestions: recommendedQuestions,
				DeviceID:             answer.DeviceID,
				RecordingURL:         answer.RecordingURL,
				ReferencesJSON:       referencesJSON,
				CleanedPayload:       cleanedPayload,
				ParseStatus:          "SUCCESS",
				AnswerSheetRowIndex:  answer.RowIndex,
				ReferenceCount:       len(references),
				RowHash:              rowHash(cleanedPayload),
			}
			items = append(items, itemResult)
			payloadItems = append(payloadItems, cleanedPayload)
		}
	}
	payload, err := json.Marshal(map[string]any{
		"schemaVersion": "mpzdata.q2a_batch_result.v1",
		"items":         payloadItems,
	})
	if err != nil {
		return nil, nil, err
	}
	return items, payload, nil
}

func referencesForSeq(rows map[int][]referenceRow, seq int) []referenceRow {
	if len(rows) == 0 {
		return nil
	}
	if refs := rows[seq]; len(refs) > 0 {
		return refs
	}
	return rows[0]
}

func splitRecommendedQuestions(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return []string{}
	}
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '，' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	location := time.FixedZone("CST", 8*60*60)
	layouts := []string{
		time.RFC3339,
		"2006年01月02日15:04",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
	}
	for _, layout := range layouts {
		if parsed, err := time.ParseInLocation(layout, value, location); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}
