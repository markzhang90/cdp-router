import { useEffect, useMemo, useState } from "react";
import { Button, Table, Tag, Tooltip, Typography, message } from "antd";
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
      { title: "名称", dataIndex: "displayName", width: 180, render: (value: string) => compactText(value) },
      {
        title: "状态",
        dataIndex: "enabled",
        width: 100,
        render: (enabled: boolean) => (enabled ? <Tag color="success">启用</Tag> : <Tag>停用</Tag>)
      },
      { title: "结果模式", dataIndex: "resultMode", width: 160 },
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
        scroll={{ x: 1560 }}
      />
    </section>
  );
}
