package download

import (
	"fmt"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/xuri/excelize/v2"
)

// productAnswer associates ordered product cards with their question and answer round.
type productAnswer struct {
	PlatformCode string
	ItemID       string
	Seq          int
	Question     string
	ProductCards []biz.ProductCard
}

// writeProductSheet adds one row per card and omits the sheet when no cards exist.
func writeProductSheet(file *excelize.File, items []productAnswer) error {
	count := 0
	for _, item := range items {
		count += len(item.ProductCards)
	}
	if count == 0 {
		return nil
	}
	const sheet = "商品"
	if _, err := file.NewSheet(sheet); err != nil {
		return err
	}
	headers := []any{"App", "itemId", "answerSeq", "cardIndex", "提问", "商品ID", "商品名", "价格", "店铺", "店铺ID", "卖点", "图片URL", "销量"}
	if err := file.SetSheetRow(sheet, "A1", &headers); err != nil {
		return err
	}
	rowNumber := 2
	for _, item := range items {
		for index, card := range item.ProductCards {
			row := []any{item.PlatformCode, item.ItemID, item.Seq, index + 1, item.Question, card.ProductID, card.ProductName, card.Price, card.ShopName, card.ShopID, card.SellingPoints, card.ImageURL, card.SalesVolume}
			for i, value := range row {
				if text, ok := value.(string); ok {
					row[i] = truncateExcelCell(text)
				}
			}
			if err := file.SetSheetRow(sheet, fmt.Sprintf("A%d", rowNumber), &row); err != nil {
				return err
			}
			rowNumber++
		}
	}
	for i, width := range []float64{14, 22, 12, 12, 46, 24, 46, 16, 32, 24, 46, 42, 20} {
		col, err := excelize.ColumnNumberToName(i + 1)
		if err != nil {
			return err
		}
		if err := file.SetColWidth(sheet, col, col, width); err != nil {
			return err
		}
	}
	style, err := file.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Color: "1D2925"}, Fill: excelize.Fill{Type: "pattern", Color: []string{"F4F8F6"}, Pattern: 1}})
	if err != nil {
		return err
	}
	if err := file.SetCellStyle(sheet, "A1", "M1", style); err != nil {
		return err
	}
	if err := file.SetPanes(sheet, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"}); err != nil {
		return err
	}
	return file.AutoFilter(sheet, fmt.Sprintf("A1:M%d", rowNumber-1), nil)
}

func truncateExcelCell(value string) string {
	const maxExcelCellChars = 32767
	runes := []rune(value)
	if len(runes) <= maxExcelCellChars {
		return value
	}
	return string(runes[:maxExcelCellChars-16]) + "\n...[已截断]"
}
