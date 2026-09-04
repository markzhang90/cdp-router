import { useEffect, useMemo, useState } from "react";
import { Button, Progress, Table, Tag, Tooltip, Typography, message } from "antd";
import type { ColumnsType } from "antd/es/table";
import { BarChart3, CheckCircle2, RefreshCw, XCircle } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { listProviders } from "../api/client";
import { PageHeader } from "../components/PageHeader";
import type { Provider } from "../types/admin";
import { compactText } from "../utils/format";

function ConfiguredTag({ configured }: { configured: boolean }) {
  return configured ? (
    <Tag color="success" icon={<CheckCircle2 size={13} />}>
      已配置
    </Tag>
  ) : (
    <Tag icon={<XCircle size={13} />}>未配置</Tag>
  );
}

function formatNumber(value: number): string {
  return new Intl.NumberFormat("zh-CN").format(value || 0);
}

function quotaPercent(provider: Provider): number {
  if (provider.dailyQuestionUnlimited || provider.dailyQuestionQuota <= 0) return 100;
  return Math.round((Math.max(0, provider.dailyQuestionRemaining) / provider.dailyQuestionQuota) * 100);
}

function quotaProgressColor(percent: number): string {
  if (percent <= 20) return "#d14343";
  if (percent <= 50) return "#c98912";
  return "#16776b";
}

function ProviderQuotaBar({ provider }: { provider: Provider }) {
  if (provider.dailyQuestionUnlimited || provider.dailyQuestionQuota <= 0) {
    return (
      <div className="provider-quota-cell">
        <div className="provider-quota-cell__meta">
          <strong>无上限</strong>
          <span>今日已用 {formatNumber(provider.dailyQuestionUsed)}</span>
        </div>
      </div>
    );
  }

  const percent = quotaPercent(provider);
  return (
    <div className="provider-quota-cell">
      <Progress
        percent={percent}
        showInfo={false}
        size="small"
        strokeColor={quotaProgressColor(percent)}
        trailColor="#edf2ef"
      />
      <div className="provider-quota-cell__meta">
        <strong>剩 {formatNumber(provider.dailyQuestionRemaining)}</strong>
        <span>
          已用 {formatNumber(provider.dailyQuestionUsed)} / {formatNumber(provider.dailyQuestionQuota)}
        </span>
      </div>
    </div>
  );
}

export function ProviderListPage() {
  const navigate = useNavigate();
  const [loading, setLoading] = useState(false);
  const [rows, setRows] = useState<Provider[]>([]);

  const load = async () => {
    setLoading(true);
    try {
      const data = await listProviders();
      setRows(data.providers);
    } catch (error) {
      message.error(error instanceof Error ? error.message : "加载供应商失败");
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void load();
  }, []);

  const columns = useMemo<ColumnsType<Provider>>(
    () => [
      {
        title: "供应商编码",
        dataIndex: "providerCode",
        fixed: "left",
        width: 170,
        render: (value: string) => (
          <Button type="link" className="table-link" onClick={() => navigate(`/providers/${encodeURIComponent(value)}`)}>
            {value}
          </Button>
        )
      },
      { title: "名称", dataIndex: "displayName", width: 160, render: (value: string) => compactText(value) },
      { title: "公司名称", dataIndex: "companyName", width: 200, render: (value: string) => compactText(value) },
      { title: "开始结算时间", dataIndex: "settlementStartAt", width: 150, render: (value: string) => compactText(value) },
      {
        title: "状态",
        dataIndex: "enabled",
        width: 100,
        render: (enabled: boolean) => (enabled ? <Tag color="success">启用</Tag> : <Tag>停用</Tag>)
      },
      { title: "结果模式", dataIndex: "resultMode", width: 160 },
      {
        title: "今日剩余额度",
        dataIndex: "dailyQuestionRemaining",
        width: 220,
        render: (_, provider) => <ProviderQuotaBar provider={provider} />
      },
      { title: "超时", dataIndex: "timeoutSeconds", width: 100, render: (value: number) => `${value}s` },
      { title: "QPS", dataIndex: "qpsLimit", width: 90 },
      { title: "并发", dataIndex: "concurrencyLimit", width: 90 },
      {
        title: "OSS 前缀",
        dataIndex: "bucketPathPrefix",
        width: 280,
        render: (value: string) => (
          <Typography.Text copyable className="mono-text">
            {compactText(value)}
          </Typography.Text>
        )
      },
      { title: "提交接口", dataIndex: "submitUrlConfigured", width: 110, render: (value: boolean) => <ConfiguredTag configured={value} /> },
      { title: "状态接口", dataIndex: "statusUrlConfigured", width: 110, render: (value: boolean) => <ConfiguredTag configured={value} /> },
      { title: "文件接口", dataIndex: "filesUrlConfigured", width: 110, render: (value: boolean) => <ConfiguredTag configured={value} /> },
      {
        title: "操作",
        fixed: "right",
        width: 96,
        render: (_, provider) => (
          <Tooltip title="数据详情">
            <Button icon={<BarChart3 size={16} />} onClick={() => navigate(`/providers/${encodeURIComponent(provider.providerCode)}`)} />
          </Tooltip>
        )
      }
    ],
    [navigate]
  );

  return (
    <section>
      <PageHeader
        title="供应商"
        description="查看下游供应商配置状态，进入详情分析每日任务和问答产能。"
        extra={
          <Button icon={<RefreshCw size={16} />} loading={loading} onClick={load}>
            刷新
          </Button>
        }
      />
      <Table
        className="task-table"
        columns={columns}
        dataSource={rows}
        loading={loading}
        pagination={false}
        rowKey="providerCode"
        scroll={{ x: 2130 }}
      />
    </section>
  );
}
