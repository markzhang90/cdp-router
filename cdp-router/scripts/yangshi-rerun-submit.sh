#!/usr/bin/env bash
set -euo pipefail

# yangshi-rerun-submit.sh
#
# Local script for this one-off rerun:
# - Reads the fixed 2026-09-01..2026-09-08 source task set from production DB.
# - Generates deterministic new batchNo values, so rerunning the script will not
#   create a second set of tasks. The default YRERUN2 prefix intentionally
#   avoids the earlier YRERUN shard that was submitted with the wrong platform.
# - Submits copied task payloads through POST /api/v1/tasks with the Yangshi
#   API key, so production dispatch uses the Yangshi profile (mpzdata).
# - Submits strictly sequentially: one task must return success before the next
#   request is sent. Any failed submission stops the script immediately.
# - Preserves the original callbackUrl; normal Yangshi callback behavior is
#   expected after task completion.
#
# Usage:
#   ./scripts/yangshi-rerun-submit.sh <shard_no>
#
# Shards:
#   1..4, each shard contains 14 tasks from the fixed ordered source set.
#
# Optional environment:
#   API_KEY                default: hardcoded one-off Yangshi production key
#   BASE_URL               default: https://openapi.mainland.vip
#   PGHOST                 default: 8.149.131.7
#   PGPORT                 default: 5432
#   PGUSER                 default: test_myself
#   PGDATABASE             default: cdp-admin
#   PGSSLMODE              default: disable
#   WORK_DIR               default: ./exports/yangshi-rerun-20260902-20260908
#   RUN_TAG                default: YRERUN2
#   DRY_RUN                default: 0; set 1 to only write source/request files
#   SUBMIT_DELAY_SECONDS   default: 1
#   ALLOW_COUNT_MISMATCH   default: 0
#   PRESERVE_CALLBACK_URL  default: 1; keep normal Yangshi callback behavior
#   NOOP_CALLBACK_URL      default: http://127.0.0.1:9/yangshi-rerun/noop
#   FORCE_ITEM_SEQ_ONE     default: 0; Yangshi adapter handles provider seq

BASE_URL="${BASE_URL:-https://openapi.mainland.vip}"
PGHOST="${PGHOST:-8.149.131.7}"
PGPORT="${PGPORT:-5432}"
PGUSER="${PGUSER:-test_myself}"
PGPASSWORD="${PGPASSWORD:-dakJAHK231dhasnmsaj}"
PGDATABASE="${PGDATABASE:-cdp-admin}"
PGSSLMODE="${PGSSLMODE:-disable}"
API_KEY="${API_KEY:-mpzdata-prod-token-20260730}"
WORK_DIR="${WORK_DIR:-./exports/yangshi-rerun-20260902-20260908}"
RUN_TAG="${RUN_TAG:-YRERUN2}"
NOOP_CALLBACK_URL="${NOOP_CALLBACK_URL:-http://127.0.0.1:9/yangshi-rerun/noop}"
DRY_RUN="${DRY_RUN:-0}"
SUBMIT_DELAY_SECONDS="${SUBMIT_DELAY_SECONDS:-1}"
ALLOW_COUNT_MISMATCH="${ALLOW_COUNT_MISMATCH:-0}"
PRESERVE_CALLBACK_URL="${PRESERVE_CALLBACK_URL:-1}"
FORCE_ITEM_SEQ_ONE="${FORCE_ITEM_SEQ_ONE:-0}"

export PGHOST PGPORT PGUSER PGPASSWORD PGDATABASE PGSSLMODE

EXPECTED_TASK_COUNT=56
EXPECTED_ITEM_COUNT=2758
SHARD_COUNT=4
SHARD_SIZE=14
SHARD_NO="${1:-}"

SOURCE_JSON="$WORK_DIR/source-tasks.json"
SHARD_JSON="$WORK_DIR/source-tasks-shard-${SHARD_NO}.json"
ALL_BATCHNOS="$WORK_DIR/new-batchnos-all.txt"
NEW_BATCHNOS="$WORK_DIR/new-batchnos.txt"
SHARD_BATCHNOS="$WORK_DIR/new-batchnos-shard-${SHARD_NO}.txt"
SUBMIT_RESULTS="$WORK_DIR/submit-results-shard-${SHARD_NO}.tsv"
REQUEST_DIR="$WORK_DIR/requests"
RESPONSE_DIR="$WORK_DIR/responses"
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

validate_shard_no() {
  case "$SHARD_NO" in
    1|2|3|4)
      ;;
    *)
      echo "ERROR: shard_no is required and must be 1, 2, 3, or 4." >&2
      echo "Usage: ./scripts/yangshi-rerun-submit.sh <shard_no>" >&2
      exit 1
      ;;
  esac
}

hash10() {
  if command -v shasum >/dev/null 2>&1; then
    printf '%s' "$1" | shasum -a 256 | awk '{print substr($1,1,10)}'
  else
    printf '%s' "$1" | sha256sum | awk '{print substr($1,1,10)}'
  fi
}

new_batch_no() {
  local old_batch_no="$1"
  local source_date="$2"
  local date_compact suffix hash
  date_compact="${source_date//-/}"
  suffix="${old_batch_no##*-}"
  hash="$(hash10 "$old_batch_no")"
  printf '%s-%s-%s-%s' "$RUN_TAG" "$date_compact" "$suffix" "$hash"
}

task_exists() {
  local batch_no="$1"
  "$HELPER" -mode lookup -batch-no "$batch_no"
}

fetch_source_tasks() {
  mkdir -p "$WORK_DIR" "$REQUEST_DIR" "$RESPONSE_DIR"
  echo "Fetching fixed source task set from $PGHOST/$PGDATABASE ..."
  "$HELPER" -mode source >"$SOURCE_JSON"
}

validate_source_tasks() {
  local task_count item_count mismatch_count max_seq
  task_count="$(jq 'length' "$SOURCE_JSON")"
  item_count="$(jq '[.[].item_count] | add // 0' "$SOURCE_JSON")"
  mismatch_count="$(jq '[.[] | select(.item_count != .total_count)] | length' "$SOURCE_JSON")"
  max_seq="$(jq '[.[].max_item_seq // 0] | max // 0' "$SOURCE_JSON")"

  echo "Source task count: $task_count"
  echo "Source item count: $item_count"
  echo "Source total_count mismatches: $mismatch_count"
  echo "Source max item seq: $max_seq"

  if [ "$mismatch_count" != "0" ]; then
    echo "ERROR: source item_count and total_count are not consistent." >&2
    exit 1
  fi
  if [ "$ALLOW_COUNT_MISMATCH" != "1" ] &&
    { [ "$task_count" != "$EXPECTED_TASK_COUNT" ] || [ "$item_count" != "$EXPECTED_ITEM_COUNT" ]; }; then
    echo "ERROR: source set does not match expected $EXPECTED_TASK_COUNT tasks / $EXPECTED_ITEM_COUNT items." >&2
    echo "Set ALLOW_COUNT_MISMATCH=1 only after manually reviewing $SOURCE_JSON." >&2
    exit 1
  fi
}

write_shard_source_tasks() {
  local start end shard_count shard_items
  start=$(( (SHARD_NO - 1) * SHARD_SIZE ))
  end=$(( start + SHARD_SIZE ))
  jq --argjson start "$start" --argjson end "$end" '.[$start:$end]' "$SOURCE_JSON" >"$SHARD_JSON"
  shard_count="$(jq 'length' "$SHARD_JSON")"
  shard_items="$(jq '[.[].item_count] | add // 0' "$SHARD_JSON")"
  echo "Shard: $SHARD_NO/$SHARD_COUNT tasks=$shard_count items=$shard_items source_index=$((start + 1))-$end"
  if [ "$shard_count" != "$SHARD_SIZE" ]; then
    echo "ERROR: shard $SHARD_NO has $shard_count tasks, want $SHARD_SIZE." >&2
    exit 1
  fi
}

write_request_files_and_batchnos() {
  : >"$ALL_BATCHNOS"
  : >"$NEW_BATCHNOS"
  : >"$SHARD_BATCHNOS"
  jq -c '.[]' "$SOURCE_JSON" | while IFS= read -r row; do
    local old_batch_no source_date task_name new_batch request_file
    old_batch_no="$(jq -r '.old_batch_no' <<<"$row")"
    source_date="$(jq -r '.source_date' <<<"$row")"
    task_name="$(jq -r '.task_name' <<<"$row")"
    new_batch="$(new_batch_no "$old_batch_no" "$source_date")"
    request_file="$REQUEST_DIR/$new_batch.json"

    printf '%s\n' "$new_batch" >>"$ALL_BATCHNOS"
    printf '%s\n' "$new_batch" >>"$NEW_BATCHNOS"
    jq -c \
      --arg batch_no "$new_batch" \
      --arg task_name "$task_name" \
      --arg biz_date "$source_date" \
      --arg callback_url "$NOOP_CALLBACK_URL" \
      --arg preserve_callback_url "$PRESERVE_CALLBACK_URL" \
      --arg force_item_seq_one "$FORCE_ITEM_SEQ_ONE" \
      '.raw_request
        | .batchNo = $batch_no
        | .taskName = $task_name
        | .bizDate = $biz_date
        | if $preserve_callback_url == "1" then . else .callbackUrl = $callback_url end
        | .source = (.source // "yangshi-rerun")
        | if $force_item_seq_one == "1" then
            .items = ((.items // []) | map(.seq = 1))
          else
            .
          end' \
      <<<"$row" >"$request_file"
  done
  jq -c '.[]' "$SHARD_JSON" | while IFS= read -r row; do
    local old_batch_no source_date new_batch
    old_batch_no="$(jq -r '.old_batch_no' <<<"$row")"
    source_date="$(jq -r '.source_date' <<<"$row")"
    new_batch="$(new_batch_no "$old_batch_no" "$source_date")"
    printf '%s\n' "$new_batch" >>"$SHARD_BATCHNOS"
  done
}

check_submit_platform() {
  local platform_info platform_code callback_mode
  platform_info="$("$HELPER" -mode platform -api-key "$API_KEY")"
  if [ -z "$platform_info" ]; then
    echo "WARNING: API_KEY was not found in platform_users; continuing because /api/v1/tasks will enforce auth."
    return
  fi

  platform_code="$(cut -f1 <<<"$platform_info")"
  callback_mode="$(cut -f2 <<<"$platform_info")"
  echo "Submit platform: $platform_code callback_mode=$callback_mode"

  local platform_code_lc callback_mode_uc
  platform_code_lc="$(printf '%s' "$platform_code" | tr '[:upper:]' '[:lower:]')"
  callback_mode_uc="$(printf '%s' "$callback_mode" | tr '[:lower:]' '[:upper:]')"
  if [[ "$platform_code_lc" != *yangshi* && "$callback_mode_uc" != "YANGSHI" && "$callback_mode_uc" != "CUSTOM" ]]; then
    echo "WARNING: API_KEY does not look like the Yangshi production platform; dispatch may not use mpzdata." >&2
  fi
}

submit_tasks() {
  if [ "$DRY_RUN" = "1" ]; then
    echo "DRY_RUN=1, request files written under $REQUEST_DIR; no tasks submitted."
    return
  fi
  if [ -z "${API_KEY:-}" ]; then
    echo "ERROR: API_KEY is required for submission." >&2
    exit 1
  fi
  check_submit_platform

  if [ ! -f "$SUBMIT_RESULTS" ]; then
    printf 'new_batch_no\tnew_task_id\tnew_external_batch_id\ttask_name\tsource_date\tquestion_count\tresult\thttp_code\tmessage\n' >"$SUBMIT_RESULTS"
  fi

  echo "Submit mode: shard $SHARD_NO/$SHARD_COUNT, strict sequential, stop on first failed submission."
  jq -c '.[]' "$SHARD_JSON" | while IFS= read -r row; do
    local old_batch_no source_date task_name item_count new_batch request_file response_file existing_id response http_code body status external_batch_id task_id message
    old_batch_no="$(jq -r '.old_batch_no' <<<"$row")"
    source_date="$(jq -r '.source_date' <<<"$row")"
    task_name="$(jq -r '.task_name' <<<"$row")"
    item_count="$(jq -r '.item_count' <<<"$row")"
    new_batch="$(new_batch_no "$old_batch_no" "$source_date")"
    request_file="$REQUEST_DIR/$new_batch.json"
    response_file="$RESPONSE_DIR/$new_batch.json"

    existing_id="$(task_exists "$new_batch" | head -1 || true)"
    if [ -n "$existing_id" ]; then
      echo "SKIP existing task: $new_batch task_id=$existing_id"
      printf '%s\t%s\t\t%s\t%s\t%s\t%s\t%s\t%s\n' \
        "$new_batch" "$existing_id" "$task_name" "$source_date" "$item_count" "SKIPPED_EXISTS" "" "" >>"$SUBMIT_RESULTS"
      continue
    fi

    echo "Submitting $new_batch ($task_name / $source_date / $item_count items) ..."
    response="$(curl -sS -w '\n%{http_code}' \
      -X POST "$BASE_URL/api/v1/tasks" \
      -H "X-API-Key: $API_KEY" \
      -H 'Content-Type: application/json' \
      --data-binary "@$request_file")"
    http_code="$(tail -n 1 <<<"$response")"
    body="$(sed '$d' <<<"$response")"
    printf '%s\n' "$body" >"$response_file"

    status="$(jq -r '(.status // .code // -1)' <<<"$body" 2>/dev/null || echo "-1")"
    external_batch_id="$(jq -r '.data.externalBatchId // ""' <<<"$body" 2>/dev/null || true)"
    message="$(jq -r '(.message // "") | gsub("[\\t\\r\\n]"; " ")' <<<"$body" 2>/dev/null || true)"
    task_id="$(task_exists "$new_batch" | head -1 || true)"

    if [ "$http_code" = "200" ] && [ "$status" = "0" ]; then
      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
        "$new_batch" "$task_id" "$external_batch_id" "$task_name" "$source_date" "$item_count" "SUBMITTED" "$http_code" "$message" >>"$SUBMIT_RESULTS"
      echo "OK: $new_batch accepted; continuing to next task."
    else
      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
        "$new_batch" "$task_id" "$external_batch_id" "$task_name" "$source_date" "$item_count" "FAILED" "$http_code" "$message" >>"$SUBMIT_RESULTS"
      echo "ERROR: submit failed for $new_batch HTTP=$http_code status=$status message=$message" >&2
      exit 1
    fi

    sleep "$SUBMIT_DELAY_SECONDS"
  done
}

main() {
  require_cmd jq
  require_cmd curl
  require_cmd go
  if ! command -v shasum >/dev/null 2>&1 && ! command -v sha256sum >/dev/null 2>&1; then
    echo "ERROR: required command not found: shasum or sha256sum" >&2
    exit 1
  fi

  echo "Base URL: $BASE_URL"
  echo "Work dir: $WORK_DIR"
  echo "Run tag: $RUN_TAG"
  echo "Shard: $SHARD_NO/$SHARD_COUNT"
  echo "Preserve callbackUrl: $PRESERVE_CALLBACK_URL"
  if [ "$PRESERVE_CALLBACK_URL" != "1" ]; then
    echo "No-op callback URL: $NOOP_CALLBACK_URL"
  fi
  echo "Force items[].seq=1: $FORCE_ITEM_SEQ_ONE"

  validate_shard_no
  build_helper
  fetch_source_tasks
  validate_source_tasks
  write_shard_source_tasks
  write_request_files_and_batchnos
  submit_tasks

  echo "Done."
  echo "All batchNo list: $ALL_BATCHNOS"
  echo "Compatibility all batchNo list: $NEW_BATCHNOS"
  echo "Shard batchNo list: $SHARD_BATCHNOS"
  echo "Submit results:   $SUBMIT_RESULTS"
  echo "Request files:    $REQUEST_DIR"
}

main "$@"
