package cleaner

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestExcelParserParsesRealProviderWorkbook(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "AI数据接入文档", "8ce0e57e-数据采集_2026-07-23T18-09-59.xlsx"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewExcelParser().Parse(context.Background(), "result.xlsx", content)
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 54 {
		t.Fatalf("row count=%d, want 54", result.RowCount)
	}
	if len(result.ItemResults) != 2 {
		t.Fatalf("item result count=%d, want 2", len(result.ItemResults))
	}

	byItemID := map[string]int{}
	for _, item := range result.ItemResults {
		var refs []map[string]any
		if err := json.Unmarshal(item.ReferencesJSON, &refs); err != nil {
			t.Fatal(err)
		}
		byItemID[item.ItemID] = len(refs)
		if item.AnswerText == "" {
			t.Fatalf("item %s answer is empty", item.ItemID)
		}
	}
	if byItemID["MPZ-REAL-20260723180500-doubao-001"] != 28 {
		t.Fatalf("doubao reference count=%d, want 28", byItemID["MPZ-REAL-20260723180500-doubao-001"])
	}
	if byItemID["MPZ-REAL-20260723180500-qwen-002"] != 24 {
		t.Fatalf("qwen reference count=%d, want 24", byItemID["MPZ-REAL-20260723180500-qwen-002"])
	}
}

func TestExcelParserKeepsDuplicateAnswersAsResultSeq(t *testing.T) {
	file := excelize.NewFile()
	defer func() { _ = file.Close() }()
	if err := file.SetSheetName("Sheet1", "AI回答"); err != nil {
		t.Fatal(err)
	}
	answerHeaders := []any{"itemId", "问题", "平台", "AI回答"}
	if err := file.SetSheetRow("AI回答", "A1", &answerHeaders); err != nil {
		t.Fatal(err)
	}
	firstAnswer := []any{"item-1", "问题一", "doubao", "第一轮回答"}
	secondAnswer := []any{"item-1", "问题一", "doubao", "第二轮回答"}
	if err := file.SetSheetRow("AI回答", "A2", &firstAnswer); err != nil {
		t.Fatal(err)
	}
	if err := file.SetSheetRow("AI回答", "A3", &secondAnswer); err != nil {
		t.Fatal(err)
	}
	var content bytes.Buffer
	if err := file.Write(&content); err != nil {
		t.Fatal(err)
	}

	result, err := NewExcelParser().Parse(context.Background(), "result.xlsx", content.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ItemResults) != 2 {
		t.Fatalf("item result count=%d, want 2", len(result.ItemResults))
	}
	if result.ItemResults[0].Seq != 1 || result.ItemResults[0].AnswerText != "第一轮回答" {
		t.Fatalf("first result=%+v, want seq=1 first answer", result.ItemResults[0])
	}
	if result.ItemResults[1].Seq != 2 || result.ItemResults[1].AnswerText != "第二轮回答" {
		t.Fatalf("second result=%+v, want seq=2 second answer", result.ItemResults[1])
	}
	if result.Rows[0].Seq != 1 || result.Rows[1].Seq != 2 {
		t.Fatalf("row seqs=%d,%d; want 1,2", result.Rows[0].Seq, result.Rows[1].Seq)
	}
}
