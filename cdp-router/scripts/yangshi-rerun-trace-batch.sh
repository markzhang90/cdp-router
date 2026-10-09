#!/usr/bin/env bash
set -euo pipefail

# yangshi-rerun-trace-batch.sh
#
# Trace a deterministic rerun batchNo back to its original source batchNo.
#
# Usage:
#   ./scripts/yangshi-rerun-trace-batch.sh YRERUN2-20260902-e0083b89-0001fd4a3c
#   ./scripts/yangshi-rerun-trace-batch.sh > exports/yangshi-rerun-20260902-20260908/rerun-batch-trace.tsv

PGHOST="${PGHOST:-8.149.131.7}"
PGPORT="${PGPORT:-5432}"
PGUSER="${PGUSER:-test_myself}"
PGPASSWORD="${PGPASSWORD:-dakJAHK231dhasnmsaj}"
PGDATABASE="${PGDATABASE:-cdp-admin}"
PGSSLMODE="${PGSSLMODE:-disable}"
WORK_DIR="${WORK_DIR:-./exports/yangshi-rerun-20260902-20260908}"
RUN_TAG="${RUN_TAG:-YRERUN2}"
RERUN_BATCH_NO="${1:-}"

export PGHOST PGPORT PGUSER PGPASSWORD PGDATABASE PGSSLMODE RUN_TAG

if ! command -v go >/dev/null 2>&1; then
  echo "ERROR: required command not found: go" >&2
  exit 1
fi

mkdir -p "$WORK_DIR"
HELPER="$WORK_DIR/yangshi-rerun-helper"
go build -o "$HELPER" ./scripts/yangshi_rerun_export.go

if [ -n "$RERUN_BATCH_NO" ]; then
  "$HELPER" -mode trace -run-tag "$RUN_TAG" -batch-no "$RERUN_BATCH_NO"
else
  "$HELPER" -mode trace -run-tag "$RUN_TAG"
fi
