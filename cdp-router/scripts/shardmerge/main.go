package main

// shardmerge is a one-off operations helper for the YRERUN14 rerun batches.
//
// It downloads nothing by itself: point -shard-dir at locally downloaded
// provider shard xlsx files (OSS_UPLOAD result files) and it will:
//   - parse them with the exact production cleaner (cleaner.ExcelParser)
//   - align rounds (轮次) with cdp_task_items.seq from the database
//   - optionally backfill rows missing from the shards from a -fill-file
//     workbook that is already in the final Yangshi target format
//     (provider shards always win; fill rows are used only for itemIds
//     absent from the shards, answers and references together)
//   - render one Yangshi-format workbook per task via
//     callback.BuildYangshiResultWorkbook (same code as the final upload)
//
// Usage:
//   go run ./scripts/shardmerge -task-id 962 -batch-no YRERUN14-20260929-960-V1 \
//     -shard-dir ./exports/yangshi-rerun-20260929/shards/960-V1 \
//     -fill-file ./tmp/futalin-history-valid-20260929/YRERUN14-20260929-960-V1-result.xlsx \
//     -output ./exports/yangshi-rerun-20260929/merged/YRERUN14-20260929-960-V1.xlsx
//
// Database access follows the same convention as scripts/yangshi_rerun_export.go:
// PGHOST/PGPORT/PGUSER/PGPASSWORD/PGDATABASE/PGSSLMODE or DATABASE_URL.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/GitMarkZhang/cdp-router/internal/callback"
	"github.com/GitMarkZhang/cdp-router/internal/cleaner"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xuri/excelize/v2"
)

func main() {
	taskID := flag.Int64("task-id", 0, "cdp_tasks.id")
	batchNo := flag.String("batch-no", "", "batch no, used for logging only")
	shardDir := flag.String("shard-dir", "", "directory containing provider shard .xlsx files")
	fillFile := flag.String("fill-file", "", "optional target-format workbook used to backfill itemIds missing from shards")
	output := flag.String("output", "", "merged xlsx output path")
	flag.Parse()

	if *taskID <= 0 || strings.TrimSpace(*shardDir) == "" || strings.TrimSpace(*output) == "" {
		fatalf("-task-id, -shard-dir and -output are required")
	}

	ctx := context.Background()
	db, err := pgxpool.New(ctx, databaseConfig())
	if err != nil {
		fatalf("connect database: %v", err)
	}
	defer db.Close()

	roundByItem, err := loadTaskItemRounds(ctx, db, *taskID)
	if err != nil {
		fatalf("load task items: %v", err)
	}

	entries, err := os.ReadDir(*shardDir)
	if err != nil {
		fatalf("read shard dir: %v", err)
	}
	fileNames := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".xlsx") {
			continue
		}
		fileNames = append(fileNames, entry.Name())
	}
	sort.Strings(fileNames) // file names embed the upload timestamp, so sort == upload order
	if len(fileNames) == 0 {
		fatalf("no .xlsx shards found in %s", *shardDir)
	}

	parser := cleaner.NewExcelParser()
	merged := make(map[string]biz.TaskItemResult)
	perFile := make([]string, 0, len(fileNames))
	duplicates := 0
	for _, name := range fileNames {
		content, err := os.ReadFile(filepath.Join(*shardDir, name))
		if err != nil {
			fatalf("read shard %s: %v", name, err)
		}
		parsed, err := parser.Parse(ctx, name, content)
		if err != nil {
			fatalf("parse shard %s: %v", name, err)
		}
		fresh := 0
		for _, item := range parsed.ItemResults {
			if item.ParseStatus != "" && item.ParseStatus != "SUCCESS" {
				fatalf("shard %s has non-SUCCESS item %s: %s", name, item.ItemID, item.ParseStatus)
			}
			if _, ok := merged[item.ItemID]; ok {
				duplicates++ // pipeline semantics: later import overwrites earlier rows
			}
			merged[item.ItemID] = item
			fresh++
		}
		perFile = append(perFile, fmt.Sprintf("  %-38s answer_rows=%d", name, len(parsed.ItemResults)))
	}

	unknown := make([]string, 0)
	views := make([]biz.TaskItemResultView, 0, len(merged))
	for itemID, item := range merged {
		round, ok := roundByItem[itemID]
		if !ok {
			unknown = append(unknown, itemID)
			continue
		}
		if len(item.RecommendedQuestions) == 0 || !jsonValid(item.RecommendedQuestions) {
			item.RecommendedQuestions = []byte(`[]`)
		}
		if len(item.ReferencesJSON) == 0 || !jsonValid(item.ReferencesJSON) {
			item.ReferencesJSON = []byte(`[]`)
		}
		views = append(views, biz.TaskItemResultView{
			ItemID:               item.ItemID,
			PlatformCode:         item.PlatformCode,
			Seq:                  round,
			Question:             item.Question,
			ResultStatus:         "IMPORTED",
			AnswerText:           item.AnswerText,
			ThinkingProcess:      item.ThinkingProcess,
			GeneratedAt:          item.GeneratedAt,
			GeneratedAtText:      item.GeneratedAtText,
			RecommendedQuestions: item.RecommendedQuestions,
			DeviceID:             item.DeviceID,
			RecordingURL:         item.RecordingURL,
			ReferencesJSON:       item.ReferencesJSON,
			ReferenceCount:       item.ReferenceCount,
			ParseStatus:          item.ParseStatus,
			ParseError:           item.ParseError,
		})
	}
	missing := make([]string, 0)
	known := make(map[string]bool, len(roundByItem))
	for itemID := range merged {
		known[itemID] = true
	}
	for itemID := range roundByItem {
		if !known[itemID] {
			missing = append(missing, itemID)
		}
	}
	sort.Strings(missing)

	fillStats := fillReport{}
	if strings.TrimSpace(*fillFile) != "" {
		var fillViews []biz.TaskItemResultView
		fillViews, fillStats = loadFillViews(*fillFile, roundByItem, missing)
		views = append(views, fillViews...)
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].ItemID != views[j].ItemID {
			return views[i].ItemID < views[j].ItemID
		}
		return views[i].Seq < views[j].Seq
	})

	stillMissing := make([]string, 0)
	covered := make(map[string]bool, len(views))
	for _, view := range views {
		covered[view.ItemID] = true
	}
	for itemID := range roundByItem {
		if !covered[itemID] {
			stillMissing = append(stillMissing, itemID)
		}
	}
	sort.Strings(stillMissing)

	if err := os.MkdirAll(filepath.Dir(*output), 0o755); err != nil {
		fatalf("create output dir: %v", err)
	}
	content, err := callback.BuildYangshiResultWorkbook(views)
	if err != nil {
		fatalf("build workbook: %v", err)
	}
	if err := os.WriteFile(*output, content, 0o644); err != nil {
		fatalf("write workbook: %v", err)
	}

	fmt.Printf("batch=%s task_id=%d shards=%d\n", *batchNo, *taskID, len(fileNames))
	for _, line := range perFile {
		fmt.Println(line)
	}
	fmt.Printf("shard_answer_rows=%d duplicates_overwritten=%d unknown_item_ids=%d missing_from_shards=%d\n",
		len(merged), duplicates, len(unknown), len(missing))
	if len(unknown) > 0 {
		fmt.Printf("  unknown (not in cdp_task_items, excluded): %s\n", strings.Join(prefix(unknown, 5), ", "))
	}
	if len(missing) > 0 {
		fmt.Printf("  missing (submitted but no shard row yet): %s\n", strings.Join(prefix(missing, 5), ", "))
	}
	if strings.TrimSpace(*fillFile) != "" {
		fmt.Printf("fill_file=%s answer_rows=%d used=%d skipped_covered_or_unknown=%d not_in_fill=%d refs_used=%d\n",
			filepath.Base(*fillFile), fillStats.AnswerRows, fillStats.Used, fillStats.Skipped, fillStats.NotInFill, fillStats.RefsUsed)
		if fillStats.NotInFill > 0 {
			fmt.Printf("  still missing after fill: %s\n", strings.Join(prefix(fillStats.MissingIDs, 10), ", "))
		}
	}
	fmt.Printf("final_answer_rows=%d expected_items=%d remaining_missing=%d\n",
		len(views), len(roundByItem), len(stillMissing))
	emptyAnswers := 0
	for _, view := range views {
		if strings.TrimSpace(view.AnswerText) == "" {
			emptyAnswers++
		}
	}
	fmt.Printf("empty_answer_rows=%d output=%s\n", emptyAnswers, *output)
}

type fillReport struct {
	AnswerRows int
	Used       int
	Skipped    int
	NotInFill  int
	RefsUsed   int
	MissingIDs []string
}

type fillAnswer struct {
	FillRound    int
	Question     string
	Platform     string
	Thinking     string
	Answer       string
	GeneratedAt  string
	Recommended  string
	DeviceID     string
	RecordingURL string
}

type fillReference struct {
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

// loadFillViews reads a workbook that is already in the final Yangshi target
// format and returns views only for the wanted (missing) itemIds. Answers and
// their references are paired by itemId, mirroring how the shard parser pairs
// them.
func loadFillViews(path string, roundByItem map[string]int, wanted []string) ([]biz.TaskItemResultView, fillReport) {
	file, err := excelize.OpenFile(path)
	if err != nil {
		fatalf("open fill file: %v", err)
	}
	defer func() { _ = file.Close() }()

	wantSet := make(map[string]bool, len(wanted))
	for _, itemID := range wanted {
		wantSet[itemID] = true
	}

	answers, report := loadFillAnswers(file, "AI回答", wantSet)
	references := loadFillReferences(file, "溯源信息", wantSet)

	views := make([]biz.TaskItemResultView, 0, len(wanted))
	missing := make([]string, 0)
	for _, itemID := range wanted {
		answer, ok := answers[itemID]
		if !ok {
			missing = append(missing, itemID)
			continue
		}
		refs := references[itemID]
		refsJSON, err := json.Marshal(refs)
		if err != nil {
			fatalf("marshal fill references for %s: %v", itemID, err)
		}
		recommended := []byte(`[]`)
		if strings.TrimSpace(answer.Recommended) != "" {
			recommended, err = json.Marshal([]string{answer.Recommended})
			if err != nil {
				fatalf("marshal fill recommended for %s: %v", itemID, err)
			}
		}
		if answer.FillRound != 0 && answer.FillRound != roundByItem[itemID] {
			fmt.Printf("  WARN fill round mismatch for %s: fill=%d db=%d, using db value\n",
				itemID, answer.FillRound, roundByItem[itemID])
		}
		report.RefsUsed += len(refs)
		report.Used++
		views = append(views, biz.TaskItemResultView{
			ItemID:               itemID,
			PlatformCode:         answer.Platform,
			Seq:                  roundByItem[itemID],
			Question:             answer.Question,
			ResultStatus:         "IMPORTED",
			AnswerText:           answer.Answer,
			ThinkingProcess:      answer.Thinking,
			GeneratedAtText:      answer.GeneratedAt,
			RecommendedQuestions: recommended,
			DeviceID:             answer.DeviceID,
			RecordingURL:         answer.RecordingURL,
			ReferencesJSON:       refsJSON,
			ReferenceCount:       len(refs),
			ParseStatus:          "SUCCESS",
		})
	}
	report.MissingIDs = missing
	report.NotInFill = len(missing)
	return views, report
}

func loadFillAnswers(file *excelize.File, sheet string, wantSet map[string]bool) (map[string]fillAnswer, fillReport) {
	rows, err := file.GetRows(sheet)
	if err != nil {
		fatalf("read fill sheet %s: %v", sheet, err)
	}
	if len(rows) == 0 {
		fatalf("fill sheet %s is empty", sheet)
	}
	header := headerIndex(rows[0])
	colItem := header("itemId")
	if colItem < 0 {
		fatalf("fill sheet %s has no itemId column", sheet)
	}
	answers := make(map[string]fillAnswer)
	report := fillReport{}
	for _, row := range rows[1:] {
		itemID := cellString(row, colItem)
		if itemID == "" {
			continue
		}
		report.AnswerRows++
		if !wantSet[itemID] {
			report.Skipped++
			continue
		}
		if _, dup := answers[itemID]; dup {
			fatalf("fill sheet %s has duplicate itemId %s", sheet, itemID)
		}
		answers[itemID] = fillAnswer{
			FillRound:    cellInt(row, header("轮次")),
			Question:     cellString(row, header("问题")),
			Platform:     cellString(row, header("平台")),
			Thinking:     cellString(row, header("深度思考过程")),
			Answer:       cellString(row, header("AI回答")),
			GeneratedAt:  cellString(row, header("AI生成时间")),
			Recommended:  cellString(row, header("推荐问题")),
			DeviceID:     cellString(row, header("设备ID")),
			RecordingURL: cellString(row, header("录屏url")),
		}
	}
	return answers, report
}

func loadFillReferences(file *excelize.File, sheet string, wantSet map[string]bool) map[string][]fillReference {
	rows, err := file.GetRows(sheet)
	if err != nil {
		fatalf("read fill sheet %s: %v", sheet, err)
	}
	out := make(map[string][]fillReference)
	if len(rows) == 0 {
		return out
	}
	header := headerIndex(rows[0])
	colItem := header("itemId")
	if colItem < 0 {
		fatalf("fill sheet %s has no itemId column", sheet)
	}
	for _, row := range rows[1:] {
		itemID := cellString(row, colItem)
		if itemID == "" || !wantSet[itemID] {
			continue
		}
		out[itemID] = append(out[itemID], fillReference{
			URL:                cellString(row, header("url")),
			Title:              cellString(row, header("标题")),
			Content:            cellString(row, header("内容")),
			PublishedAtText:    cellString(row, header("发布日期")),
			SiteName:           cellString(row, header("网站名称")),
			AuthorityLevel:     cellString(row, header("网站权威性等级")),
			SiteCategory:       cellString(row, header("网站分类")),
			Author:             cellString(row, header("作者")),
			AuthorCategory:     cellString(row, header("作者分类")),
			AuthorIdentityType: cellString(row, header("作者身份类型")),
		})
	}
	return out
}

func headerIndex(headers []string) func(string) int {
	index := make(map[string]int, len(headers))
	for i, name := range headers {
		name = strings.TrimSpace(name)
		if _, ok := index[name]; !ok {
			index[name] = i
		}
	}
	return func(name string) int {
		return index[strings.TrimSpace(name)]
	}
}

func cellString(row []string, col int) string {
	if col < 0 || col >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[col])
}

func cellInt(row []string, col int) int {
	value := cellString(row, col)
	if value == "" {
		return 0
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return parsed
}

func loadTaskItemRounds(ctx context.Context, db *pgxpool.Pool, taskID int64) (map[string]int, error) {
	rows, err := db.Query(ctx, `
select item_id, seq
from cdp_task_items
where task_id = $1
`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var itemID string
		var seq int
		if err := rows.Scan(&itemID, &seq); err != nil {
			return nil, err
		}
		out[itemID] = seq
	}
	return out, rows.Err()
}

func jsonValid(raw []byte) bool {
	return len(raw) > 0
}

func prefix(values []string, n int) []string {
	if len(values) <= n {
		return values
	}
	return values[:n]
}

func databaseConfig() string {
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		return dsn
	}
	host := getenv("PGHOST", "8.149.131.7")
	port := getenv("PGPORT", "5432")
	user := getenv("PGUSER", "test_myself")
	password := getenv("PGPASSWORD", "dakJAHK231dhasnmsaj")
	database := getenv("PGDATABASE", "cdp-admin")
	sslMode := getenv("PGSSLMODE", "disable")
	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		host, port, user, password, database, sslMode)
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ERROR: "+format+"\n", args...)
	os.Exit(1)
}
