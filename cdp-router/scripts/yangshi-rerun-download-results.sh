#!/usr/bin/env bash
set -euo pipefail

# yangshi-rerun-download-results.sh
#
# Local script for this one-off rerun:
# - Reads new batchNo values from new-batchnos-all.txt when exporting all
#   shards, with a fallback to the older new-batchnos.txt filename.
# - Looks up the corresponding task rows in production DB.
# - Exports one local Yangshi-format Excel file per new task.
# - Does not upload Yangshi OSS and does not call callback endpoints.
#
# Usage:
#   ./scripts/yangshi-rerun-download-results.sh       # all new batchNo values
#   ./scripts/yangshi-rerun-download-results.sh 1     # only shard 1
#
# Optional environment:
#   PGHOST               default: 8.149.131.7
#   PGPORT               default: 5432
#   PGUSER               default: test_myself
#   PGDATABASE           default: cdp-admin
#   PGSSLMODE            default: disable
#   WORK_DIR             default: ./exports/yangshi-rerun-20260902-20260908
#   ALLOW_PARTIAL        default: 0; set 1 to export even when SUCCESS < total_count
#   ALLOW_EMPTY          default: 0; set 1 to export header-only workbooks
#   OVERWRITE            default: 0

PGHOST="${PGHOST:-8.149.131.7}"
PGPORT="${PGPORT:-5432}"
PGUSER="${PGUSER:-test_myself}"
PGPASSWORD="${PGPASSWORD:-dakJAHK231dhasnmsaj}"
PGDATABASE="${PGDATABASE:-cdp-admin}"
PGSSLMODE="${PGSSLMODE:-disable}"
WORK_DIR="${WORK_DIR:-./exports/yangshi-rerun-20260902-20260908}"
ALLOW_PARTIAL="${ALLOW_PARTIAL:-0}"
ALLOW_EMPTY="${ALLOW_EMPTY:-0}"
OVERWRITE="${OVERWRITE:-0}"
SHARD_NO="${1:-}"

export PGHOST PGPORT PGUSER PGPASSWORD PGDATABASE PGSSLMODE

if [ -n "$SHARD_NO" ]; then
  case "$SHARD_NO" in
    1|2|3|4)
      NEW_BATCHNOS="$WORK_DIR/new-batchnos-shard-${SHARD_NO}.txt"
      ;;
    *)
      echo "ERROR: shard_no must be 1, 2, 3, or 4." >&2
      exit 1
      ;;
  esac
else
  NEW_BATCHNOS="$WORK_DIR/new-batchnos-all.txt"
  if [ ! -f "$NEW_BATCHNOS" ]; then
    NEW_BATCHNOS="$WORK_DIR/new-batchnos.txt"
  fi
fi
TASKS_JSON="$WORK_DIR/new-tasks${SHARD_NO:+-shard-${SHARD_NO}}.json"
COMPLETION_CHECK="$WORK_DIR/completion-check${SHARD_NO:+-shard-${SHARD_NO}}.tsv"
EXPORT_DIR="$WORK_DIR/excel${SHARD_NO:+/shard-${SHARD_NO}}"
HELPER="$WORK_DIR/yangshi-rerun-helper"

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "ERROR: required command not found: $1" >&2
    exit 1
  fi
}

build_helper() {
  mkdir -p "$WORK_DIR"
  go build -o "$HELPER" ./scripts/yangshi_rerun_export.go
}

sanitize_filename() {
  printf '%s' "$1" |
    tr '\r\n\t' '___' |
    sed 's#[/\\:*?"<>|]#_#g; s/[[:space:]]\\{1,\\}/ /g; s/^ //; s/ $//'
}

fetch_new_tasks() {
  mkdir -p "$EXPORT_DIR"
  "$HELPER" -mode lookup-many -batch-file "$NEW_BATCHNOS" >"$TASKS_JSON"
}

write_completion_check() {
  jq -r '
    ["new_batch_no","task_id","task_name","source_date","status","total_count","item_count","success_count","non_success_result_count"],
    (.[] | [
      .requested_batch_no,
      (.task_id // ""),
      (.task_name // ""),
      (.source_date // ""),
      (.status // "MISSING"),
      (.total_count // 0),
      (.item_count // 0),
      (.success_count // 0),
      (.non_success_result_count // 0)
    ])
    | @tsv
  ' "$TASKS_JSON" >"$COMPLETION_CHECK"
}

export_excels() {
  local total missing exported skipped failed
  total="$(jq 'length' "$TASKS_JSON")"
  missing="$(jq '[.[] | select(.task_id == null)] | length' "$TASKS_JSON")"
  exported=0
  skipped=0
  failed=0

  echo "BatchNo count: $total"
  echo "Missing task rows: $missing"
  echo "Completion check: $COMPLETION_CHECK"

  if [ "$missing" != "0" ]; then
    echo "ERROR: some new batchNo values do not exist in cdp_tasks. See $COMPLETION_CHECK." >&2
    exit 1
  fi

  while IFS= read -r row; do
    local task_id task_name source_date batch_no total_count success_count base_name file_name output dated_export_dir
    task_id="$(jq -r '.task_id' <<<"$row")"
    task_name="$(jq -r '.task_name' <<<"$row")"
    source_date="$(jq -r '.source_date' <<<"$row")"
    batch_no="$(jq -r '.batch_no' <<<"$row")"
    total_count="$(jq -r '.total_count' <<<"$row")"
    success_count="$(jq -r '.success_count' <<<"$row")"

    if [ "$ALLOW_PARTIAL" != "1" ] && [ "$success_count" -lt "$total_count" ]; then
      echo "SKIP incomplete: $batch_no success=$success_count total=$total_count"
      skipped=$((skipped + 1))
      continue
    fi
    if [ "$ALLOW_EMPTY" != "1" ] && [ "$success_count" = "0" ]; then
      echo "SKIP empty: $batch_no"
      skipped=$((skipped + 1))
      continue
    fi

    base_name="$(sanitize_filename "$task_name+$source_date")"
    file_name="$base_name.xlsx"
    dated_export_dir="$EXPORT_DIR/$source_date"
    mkdir -p "$dated_export_dir"
    output="$dated_export_dir/$file_name"
    if [ -s "$output" ] && [ "$OVERWRITE" != "1" ]; then
      echo "SKIP existing file: $output"
      skipped=$((skipped + 1))
      continue
    fi

    echo "Exporting task_id=$task_id batchNo=$batch_no -> $file_name"
    if [ "$ALLOW_EMPTY" = "1" ]; then
      if "$HELPER" -mode export -task-id "$task_id" -output "$output" -allow-empty; then
        exported=$((exported + 1))
      else
        failed=$((failed + 1))
        echo "ERROR: export failed for $batch_no" >&2
        exit 1
      fi
    else
      if "$HELPER" -mode export -task-id "$task_id" -output "$output"; then
        exported=$((exported + 1))
      else
        failed=$((failed + 1))
        echo "ERROR: export failed for $batch_no" >&2
        exit 1
      fi
    fi
  done < <(jq -c '.[]' "$TASKS_JSON")

  echo "Done. Exported=$exported skipped=$skipped failed=$failed output=$EXPORT_DIR"
}

main() {
  require_cmd jq
  require_cmd go
  if [ ! -f "$NEW_BATCHNOS" ]; then
    echo "ERROR: missing $NEW_BATCHNOS. Run yangshi-rerun-submit.sh first." >&2
    exit 1
  fi
  build_helper
  fetch_new_tasks
  write_completion_check
  export_excels
}

main "$@"
