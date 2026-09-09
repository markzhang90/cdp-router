import { Tag, Tooltip } from "antd";

type TaskResultStatsProps = {
  successCount: number;
  failedCount: number;
  missingCount: number;
  compact?: boolean;
};

function safeCount(value: number): number {
  return Number.isFinite(value) && value > 0 ? value : 0;
}

export function TaskResultStats({ successCount, failedCount, missingCount, compact = false }: TaskResultStatsProps) {
  const success = safeCount(successCount);
  const failed = safeCount(failedCount);
  const missing = safeCount(missingCount);
  const hasIssue = failed > 0 || missing > 0;

  return (
    <Tooltip title={`导入有效 ${success}，导入失败 ${failed}，缺失 ${missing}`}>
      <span className={`task-result-stats${compact ? " task-result-stats--compact" : ""}`}>
        <Tag color="success">有效 {success}</Tag>
        {hasIssue ? (
          <>
            <Tag color={failed > 0 ? "error" : "default"}>失败 {failed}</Tag>
            <Tag color={missing > 0 ? "warning" : "default"}>缺失 {missing}</Tag>
          </>
        ) : null}
      </span>
    </Tooltip>
  );
}
