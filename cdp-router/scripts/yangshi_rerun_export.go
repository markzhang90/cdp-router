package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/GitMarkZhang/cdp-router/internal/callback"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type sourceTask struct {
	OldTaskID  int64           `json:"old_task_id"`
	OldBatchNo string          `json:"old_batch_no"`
	TaskName   string          `json:"task_name"`
	SourceDate string          `json:"source_date"`
	TotalCount int             `json:"total_count"`
	RawRequest json.RawMessage `json:"raw_request"`
	SortOrder  int             `json:"sort_order"`
	ItemCount  int             `json:"item_count"`
	MaxItemSeq int             `json:"max_item_seq"`
}

type newTask struct {
	RequestedBatchNo      string `json:"requested_batch_no"`
	TaskID                *int64 `json:"task_id"`
	BatchNo               string `json:"batch_no"`
	TaskName              string `json:"task_name"`
	SourceDate            string `json:"source_date"`
	Status                string `json:"status"`
	TotalCount            int    `json:"total_count"`
	ItemCount             int    `json:"item_count"`
	SuccessCount          int    `json:"success_count"`
	NonSuccessResultCount int    `json:"non_success_result_count"`
}

func main() {
	mode := flag.String("mode", "export", "source, lookup, platform, lookup-many, trace, or export")
	batchNo := flag.String("batch-no", "", "batchNo for lookup mode")
	apiKey := flag.String("api-key", "", "platform user API key for platform mode")
	batchFile := flag.String("batch-file", "", "newline-delimited batchNo file for lookup-many mode")
	runTag := flag.String("run-tag", getenv("RUN_TAG", "YRERUN2"), "rerun batchNo prefix for trace mode")
	taskID := flag.Int64("task-id", 0, "cdp_tasks.id to export")
	output := flag.String("output", "", "xlsx output path")
	allowEmpty := flag.Bool("allow-empty", false, "allow exporting a workbook with no SUCCESS rows")
	flag.Parse()

	ctx := context.Background()
	db, err := pgxpool.New(ctx, databaseConfig())
	if err != nil {
		fatalf("connect database: %v", err)
	}
	defer db.Close()

	switch *mode {
	case "source":
		tasks, err := loadSourceTasks(ctx, db)
		if err != nil {
			fatalf("load source tasks: %v", err)
		}
		writeJSON(tasks)
	case "lookup":
		if strings.TrimSpace(*batchNo) == "" {
			fatalf("-batch-no is required")
		}
		id, ok, err := lookupTaskID(ctx, db, *batchNo)
		if err != nil {
			fatalf("lookup task: %v", err)
		}
		if ok {
			fmt.Println(id)
		}
	case "platform":
		if strings.TrimSpace(*apiKey) == "" {
			fatalf("-api-key is required")
		}
		code, callbackMode, ok, err := lookupPlatform(ctx, db, *apiKey)
		if err != nil {
			fatalf("lookup platform: %v", err)
		}
		if ok {
			fmt.Printf("%s\t%s\n", code, callbackMode)
		}
	case "lookup-many":
		if strings.TrimSpace(*batchFile) == "" {
			fatalf("-batch-file is required")
		}
		tasks, err := lookupManyTasks(ctx, db, *batchFile)
		if err != nil {
			fatalf("lookup new tasks: %v", err)
		}
		writeJSON(tasks)
	case "trace":
		if err := traceRerunBatches(ctx, db, *runTag, *batchNo); err != nil {
			fatalf("trace rerun batches: %v", err)
		}
	case "export":
		if *taskID <= 0 {
			fatalf("-task-id is required")
		}
		if *output == "" {
			fatalf("-output is required")
		}
		if err := exportWorkbook(ctx, db, *taskID, *output, *allowEmpty); err != nil {
			fatalf("%v", err)
		}
	default:
		fatalf("unknown -mode %q", *mode)
	}
}

func traceRerunBatches(ctx context.Context, db *pgxpool.Pool, runTag string, batchNo string) error {
	tasks, err := loadSourceTasks(ctx, db)
	if err != nil {
		return err
	}
	fmt.Println("new_batch_no\told_batch_no\told_task_id\ttask_name\tsource_date\ttotal_count\tshard_no")
	matched := false
	for index, task := range tasks {
		newBatchNo := rerunBatchNo(runTag, task.OldBatchNo, task.SourceDate)
		if strings.TrimSpace(batchNo) != "" && newBatchNo != batchNo {
			continue
		}
		matched = true
		shardNo := index/14 + 1
		fmt.Printf("%s\t%s\t%d\t%s\t%s\t%d\t%d\n",
			newBatchNo,
			task.OldBatchNo,
			task.OldTaskID,
			task.TaskName,
			task.SourceDate,
			task.TotalCount,
			shardNo,
		)
	}
	if strings.TrimSpace(batchNo) != "" && !matched {
		return fmt.Errorf("no source task matches rerun batchNo %q with run tag %q", batchNo, runTag)
	}
	return nil
}

func rerunBatchNo(runTag string, oldBatchNo string, sourceDate string) string {
	dateCompact := strings.ReplaceAll(sourceDate, "-", "")
	suffix := oldBatchNo
	if index := strings.LastIndex(suffix, "-"); index >= 0 && index+1 < len(suffix) {
		suffix = suffix[index+1:]
	}
	hash := sha256.Sum256([]byte(oldBatchNo))
	return fmt.Sprintf("%s-%s-%s-%x", runTag, dateCompact, suffix, hash[:5])
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

func loadSourceTasks(ctx context.Context, db *pgxpool.Pool) ([]sourceTask, error) {
	rows, err := db.Query(ctx, `
with target(task_name, sort_order) as (
  values
    ('新康泰克-症状功效(9.1-11.1)', 1),
    ('新康泰克-通用品类(9.1-11.1)', 2),
    ('扶他林任务【症状功效词】深度思考(9.1-9.30)', 3),
    ('辅舒良正式任务【8-9】【品类词&场景词】（9.1-9.30）', 4),
    ('辅舒良正式任务【8-9】【通用品类词】（9.1-9.30）', 5),
    ('芬必得任务【2平台】开深度（9.1-9.30）', 6),
    ('芬必得任务【3平台】开深度（9.1-9.30）', 7),
    ('扶他林任务【功效&场景词】深度思考（9.1-9.30）', 8)
)
select
  c.id,
  c.batch_no,
  c.task_name,
  to_char((c.created_at at time zone 'Asia/Shanghai')::date, 'YYYY-MM-DD') as source_date,
  c.total_count,
  c.raw_request,
  t.sort_order,
  count(i.id)::int as item_count,
  coalesce(max(i.seq), 0)::int as max_item_seq
from target t
join cdp_tasks c on c.task_name = t.task_name
left join cdp_task_items i on i.task_id = c.id
where c.deleted_at is null
  and c.created_at >= timestamp with time zone '2026-09-01 00:00:00+08'
  and c.created_at <  timestamp with time zone '2026-09-09 00:00:00+08'
group by c.id, c.batch_no, c.task_name, c.created_at, c.total_count, c.raw_request, t.sort_order
order by source_date, t.sort_order
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []sourceTask
	for rows.Next() {
		var task sourceTask
		if err := rows.Scan(
			&task.OldTaskID,
			&task.OldBatchNo,
			&task.TaskName,
			&task.SourceDate,
			&task.TotalCount,
			&task.RawRequest,
			&task.SortOrder,
			&task.ItemCount,
			&task.MaxItemSeq,
		); err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func lookupTaskID(ctx context.Context, db *pgxpool.Pool, batchNo string) (int64, bool, error) {
	var id int64
	err := db.QueryRow(ctx, `
select id
from cdp_tasks
where batch_no = $1
  and deleted_at is null
limit 1
`, batchNo).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

func lookupPlatform(ctx context.Context, db *pgxpool.Pool, apiKey string) (string, string, bool, error) {
	var code string
	var callbackMode string
	err := db.QueryRow(ctx, `
select platform_code, callback_mode
from platform_users
where api_key = $1
  and status = 'ACTIVE'
limit 1
`, apiKey).Scan(&code, &callbackMode)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return code, callbackMode, true, nil
}

func lookupManyTasks(ctx context.Context, db *pgxpool.Pool, batchFile string) ([]newTask, error) {
	content, err := os.ReadFile(batchFile)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(content), "\n")
	tasks := make([]newTask, 0, len(lines))
	for _, line := range lines {
		batchNo := strings.TrimSpace(line)
		if batchNo == "" {
			continue
		}
		task, err := lookupNewTask(ctx, db, batchNo)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}

func lookupNewTask(ctx context.Context, db *pgxpool.Pool, batchNo string) (newTask, error) {
	task := newTask{RequestedBatchNo: batchNo, Status: "MISSING"}
	var taskID int64
	err := db.QueryRow(ctx, `
select
  t.id,
  t.batch_no,
  t.task_name,
  to_char(coalesce(t.biz_date, (t.created_at at time zone 'Asia/Shanghai')::date), 'YYYY-MM-DD') as source_date,
  t.status,
  t.total_count,
  count(ti.id)::int as item_count,
  count(tir.id) filter (where tir.parse_status = 'SUCCESS')::int as success_count,
  count(tir.id) filter (where tir.parse_status <> 'SUCCESS')::int as non_success_result_count
from cdp_tasks t
left join cdp_task_items ti on ti.task_id = t.id
left join cdp_task_item_results tir on tir.task_item_id = ti.id
where t.batch_no = $1
  and t.deleted_at is null
group by t.id, t.batch_no, t.task_name, t.biz_date, t.created_at, t.status, t.total_count
`, batchNo).Scan(
		&taskID,
		&task.BatchNo,
		&task.TaskName,
		&task.SourceDate,
		&task.Status,
		&task.TotalCount,
		&task.ItemCount,
		&task.SuccessCount,
		&task.NonSuccessResultCount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return task, nil
	}
	if err != nil {
		return task, err
	}
	task.TaskID = &taskID
	return task, nil
}

func exportWorkbook(ctx context.Context, db *pgxpool.Pool, taskID int64, output string, allowEmpty bool) error {
	items, err := loadSuccessfulItems(ctx, db, taskID)
	if err != nil {
		return fmt.Errorf("load task item results: %w", err)
	}
	if len(items) == 0 && !allowEmpty {
		return fmt.Errorf("task %d has no SUCCESS item results", taskID)
	}

	content, err := callback.BuildYangshiResultWorkbook(items)
	if err != nil {
		return fmt.Errorf("build workbook: %w", err)
	}
	if err := os.WriteFile(output, content, 0o644); err != nil {
		return fmt.Errorf("write workbook: %w", err)
	}
	fmt.Printf("exported task_id=%d success_rows=%d output=%s\n", taskID, len(items), output)
	return nil
}

func loadSuccessfulItems(ctx context.Context, db *pgxpool.Pool, taskID int64) ([]biz.TaskItemResultView, error) {
	rows, err := db.Query(ctx, `
select
  item_id,
  coalesce(platform_code, ''),
  seq,
  coalesce(question, ''),
  coalesce(answer_text, ''),
  coalesce(thinking_process, ''),
  generated_at,
  coalesce(generated_at_text, ''),
  coalesce(recommended_questions, '[]'::jsonb),
  coalesce(device_id, ''),
  coalesce(recording_url, ''),
  coalesce(references_json, '[]'::jsonb),
  coalesce(reference_count, 0),
  parse_status,
  coalesce(parse_error, '')
from cdp_task_item_results
where task_id = $1
  and parse_status = 'SUCCESS'
order by item_id, seq, id
`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []biz.TaskItemResultView
	for rows.Next() {
		var item biz.TaskItemResultView
		var generatedAt *time.Time
		if err := rows.Scan(
			&item.ItemID,
			&item.PlatformCode,
			&item.Seq,
			&item.Question,
			&item.AnswerText,
			&item.ThinkingProcess,
			&generatedAt,
			&item.GeneratedAtText,
			&item.RecommendedQuestions,
			&item.DeviceID,
			&item.RecordingURL,
			&item.ReferencesJSON,
			&item.ReferenceCount,
			&item.ParseStatus,
			&item.ParseError,
		); err != nil {
			return nil, err
		}
		if generatedAt != nil {
			item.GeneratedAt = *generatedAt
		}
		item.ResultStatus = "IMPORTED"
		if len(item.RecommendedQuestions) == 0 || !json.Valid(item.RecommendedQuestions) {
			item.RecommendedQuestions = json.RawMessage("[]")
		}
		if len(item.ReferencesJSON) == 0 || !json.Valid(item.ReferencesJSON) {
			item.ReferencesJSON = json.RawMessage("[]")
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func writeJSON(value any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		fatalf("encode json: %v", err)
	}
}

func getenv(key string, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ERROR: "+format+"\n", args...)
	os.Exit(1)
}
