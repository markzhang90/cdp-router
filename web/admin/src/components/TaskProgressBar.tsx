import { Tooltip } from "antd";

type TaskProgressBarProps = {
  successCount: number;
  failedCount: number;
  totalCount: number;
  compact?: boolean;
};

function safeCount(value: number): number {
  return Number.isFinite(value) && value > 0 ? value : 0;
}

function precisePercent(value: number): number {
  if (!Number.isFinite(value)) return 0;
  const clamped = Math.max(0, Math.min(1, value));
  if (clamped === 1) return 100;
  return Math.floor(clamped * 10000) / 100;
}

function formatPercent(value: number): string {
  return `${new Intl.NumberFormat("zh-CN", {
    maximumFractionDigits: 2
  }).format(value)}%`;
}

export function TaskProgressBar({ successCount, failedCount, totalCount, compact = false }: TaskProgressBarProps) {
  const success = safeCount(successCount);
  const failed = safeCount(failedCount);
  const total = safeCount(totalCount);
  const denominator = total > 0 ? total : success + failed;
  const boundedSuccess = denominator > 0 ? Math.min(success, denominator) : 0;
  const boundedFailed = denominator > 0 ? Math.min(failed, Math.max(denominator - boundedSuccess, 0)) : 0;
  const successPercent = denominator > 0 ? (boundedSuccess / denominator) * 100 : 0;
  const donePercent = denominator > 0 ? precisePercent((boundedSuccess + boundedFailed) / denominator) : 0;
  const failedPercent = Math.max(0, donePercent - successPercent);
  const unfinished = Math.max(denominator - success - failed, 0);

  return (
    <Tooltip title={`成功 ${success}，失败 ${failed}，未完成 ${unfinished}，总数 ${denominator}`}>
      <div className={`task-progress-bar${compact ? " task-progress-bar--compact" : ""}`}>
        <div className="task-progress-bar__track" aria-label={`进度 ${formatPercent(donePercent)}`}>
          <span className="task-progress-bar__success" style={{ width: `${successPercent}%` }} />
          <span
            className="task-progress-bar__failed"
            style={{ left: `${successPercent}%`, width: `${failedPercent}%` }}
          />
        </div>
        <span className="task-progress-bar__value">{formatPercent(donePercent)}</span>
      </div>
    </Tooltip>
  );
}
