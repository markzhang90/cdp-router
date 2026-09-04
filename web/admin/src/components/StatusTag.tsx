type StatusTone =
  | "neutral"
  | "submitted"
  | "provider"
  | "imported"
  | "callback"
  | "done"
  | "failed";

type StatusMeta = {
  label: string;
  tone: StatusTone;
};

const TASK_STATUS_META: Record<string, StatusMeta> = {
  CREATED: { label: "待提交", tone: "neutral" },
  SUBMITTED: { label: "已提交", tone: "submitted" },
  POLLING: { label: "查询中", tone: "submitted" },
  PROVIDER_DONE: { label: "供应商完成", tone: "provider" },
  DOWNSTREAM_DONE: { label: "供应商完成", tone: "provider" },
  RESULT_IMPORTED: { label: "已导入", tone: "imported" },
  IMPORTING: { label: "导入中", tone: "imported" },
  CLEANING: { label: "清洗中", tone: "imported" },
  CALLBACK_PENDING: { label: "待回调", tone: "callback" },
  DONE: { label: "已完成", tone: "done" },
  FAILED: { label: "失败", tone: "failed" },
  CANCELED: { label: "已取消", tone: "neutral" },
  REASSIGNED: { label: "已转派", tone: "neutral" }
};

const RESULT_STATUS_META: Record<string, StatusMeta> = {
  PENDING: { label: "待导入", tone: "neutral" },
  IMPORTED: { label: "已导入", tone: "done" },
  SUCCESS: { label: "成功", tone: "done" },
  FAILED: { label: "失败", tone: "failed" },
  ERROR: { label: "错误", tone: "failed" },
  PARSE_FAILED: { label: "解析失败", tone: "failed" }
};

export function TaskStatusTag({ status }: { status: string }) {
  return <StatusPill meta={TASK_STATUS_META} status={status} />;
}

export function ResultStatusTag({ status }: { status: string }) {
  return <StatusPill meta={RESULT_STATUS_META} status={status} />;
}

function StatusPill({ meta, status }: { meta: Record<string, StatusMeta>; status: string }) {
  const normalized = status?.trim().toUpperCase() ?? "";
  const current = normalized ? meta[normalized] ?? { label: normalized, tone: "neutral" as const } : null;

  return (
    <span className={`status-pill status-pill--${current?.tone ?? "neutral"}`} title={normalized || "-"}>
      <span className="status-pill__dot" aria-hidden="true" />
      <span className="status-pill__label">{current?.label ?? "-"}</span>
    </span>
  );
}
