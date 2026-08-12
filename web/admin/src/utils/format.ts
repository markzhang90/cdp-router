export const ADMIN_TIME_ZONE = "Asia/Shanghai";

export function formatDateTime(value: string): string {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat("zh-CN", {
    timeZone: ADMIN_TIME_ZONE,
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false
  }).format(date);
}

export function formatFullDateTime(value: string): string {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat("zh-CN", {
    timeZone: ADMIN_TIME_ZONE,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false
  }).format(date);
}

export function percent(value: number): number {
  if (!Number.isFinite(value)) return 0;
  return Math.round(Math.max(0, Math.min(1, value)) * 100);
}

export function taskProgress(task: {
  progress: number;
  questionCount: number;
  successCount: number;
  failedCount: number;
}): number {
  if (task.questionCount > 0) {
    return (task.successCount + task.failedCount) / task.questionCount;
  }
  return task.progress;
}

export function compactText(value: string, fallback = "-"): string {
  const trimmed = value?.trim();
  return trimmed ? trimmed : fallback;
}

export function prettyJSON(value: string): string {
  if (!value) return "{}";
  try {
    return JSON.stringify(JSON.parse(value), null, 2);
  } catch {
    return value;
  }
}
