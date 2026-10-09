import { useEffect, useState } from "react";
import { Alert, Button, Card, Descriptions, Input, Modal, Space, Switch, Table, Tag, Tooltip, Typography, message } from "antd";
import { Plus, RefreshCw, Save, Trash2 } from "lucide-react";
import { ApiError, getValidationRules, listProviders, saveProviderValidationRule, saveValidationRules } from "../api/client";
import { getStoredUser } from "../api/session";
import { PageHeader } from "../components/PageHeader";
import { formatFullDateTime } from "../utils/format";
import type { Provider, ValidationRules } from "../types/admin";

export function ValidationRulesPage() {
  const [rules, setRules] = useState<ValidationRules | null>(null);
  const [providers, setProviders] = useState<Provider[]>([]);
  const [keywords, setKeywords] = useState<string[]>([]);
  const [input, setInput] = useState("");
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const canEdit = getStoredUser()?.role === "admin";
  const dirty = !!rules && (JSON.stringify(keywords) !== JSON.stringify(rules.keywords) || !!input.trim());
  const busy = loading || saving;
  const load = async () => {
    setLoading(true);
    try {
      const [value, providerData] = await Promise.all([getValidationRules(), listProviders()]);
      setProviders(providerData.providers);
      setRules(value);
      setKeywords(value.keywords);
      setInput("");
      setError("");
    } catch (e) { setError(e instanceof Error ? e.message : "加载失败"); }
    finally { setLoading(false); }
  };
  useEffect(() => { void load(); }, []);

  const save = async (nextKeywords: string[]) => {
    if (!rules) return;
    setSaving(true);
    try {
      const value = await saveValidationRules(rules.version, nextKeywords);
      setRules(value);
      setKeywords(value.keywords);
      setInput("");
      setError("");
      message.success(`已保存，预计 ${value.refreshIntervalSeconds} 秒内对新导入生效`);
    } catch (e) {
      setError(e instanceof ApiError && e.status === 409 ? "配置已被其他人修改，请刷新后重新编辑。" : e instanceof Error ? e.message : "保存失败");
    } finally { setSaving(false); }
  };
  const toggleProvider = (provider: Provider, enabled: boolean) => {
    if (!rules || busy) return;
    Modal.confirm({
      title: `${enabled ? "开启" : "关闭"} ${provider.displayName || provider.providerCode} 的风险拦截？`,
      content: enabled ? "所有回答继续执行三条校验。有风险的待回调任务将停止自动回调，必须人工强制回调。" : "所有回答仍执行三条校验并保留风险。包含已有风险的待回调任务，满足其他条件后也可自动回调。",
      okText: enabled ? "开启风险拦截" : "允许带风险回调",
      okButtonProps: { danger: !enabled },
      onOk: async () => {
        setSaving(true);
        try {
          const value = await saveProviderValidationRule(rules.version, provider.providerCode, enabled);
          setRules(value);
          setError("");
          message.success(`已保存，预计 ${value.refreshIntervalSeconds} 秒内对待回调任务生效`);
        } catch (e) {
          setError(e instanceof ApiError && e.status === 409 ? "配置已被其他人修改，请刷新后重新编辑。" : e instanceof Error ? e.message : "保存失败");
        } finally { setSaving(false); }
      }
    });
  };
  const collectKeywords = () => {
    const additions = input.split(/\r?\n/).map((word) => word.normalize("NFKC").trim().toLowerCase()).filter(Boolean);
    if (additions.some((word) => Array.from(word).length > 200)) {
      setError("每个关键词不能超过 200 字，请调整后再添加或保存。");
      return null;
    }
    const nextKeywords = [...new Set([...keywords, ...additions])];
    if (nextKeywords.length > 2000) {
      setError("关键词最多 2000 个，请减少后再添加或保存。");
      return null;
    }
    return nextKeywords;
  };
  const handleAdd = () => {
    const nextKeywords = collectKeywords();
    if (!nextKeywords) return;
    if (nextKeywords.length === keywords.length) message.info("输入的关键词已在清单中");
    setKeywords(nextKeywords);
    setInput("");
    setError("");
  };
  const handleSave = () => {
    const nextKeywords = collectKeywords();
    if (!nextKeywords) return;
    if (!nextKeywords.length) {
      Modal.confirm({ title: "确认清空关键词清单？", content: "保存后关键词规则不再命中回答，所有供应商仍执行长度及引用数校验。", okText: "清空并保存", okButtonProps: { danger: true }, onOk: () => save([]) });
    } else { void save(nextKeywords); }
  };
  const refresh = () => {
    if (dirty) Modal.confirm({ title: "放弃未保存的修改？", okText: "刷新", onOk: load });
    else void load();
  };
  return <section className="validation-rules-page">
    <PageHeader title="数据校验规则" description="所有供应商的回答均执行校验，按供应商控制风险是否拦截自动回调。" extra={
      <Space>
        <Tooltip title="重新读取已保存配置"><Button aria-label="刷新配置" icon={<RefreshCw size={16} />} loading={loading} disabled={busy} onClick={refresh} /></Tooltip>
        {canEdit && <Button type="primary" icon={<Save size={16} />} disabled={!rules || !dirty || loading} loading={saving} onClick={handleSave}>保存关键词</Button>}
      </Space>
    } />
    {error && <Alert type="error" showIcon message={error} />}
    <Card title="供应商风险拦截" size="small">
      <Space direction="vertical" className="validation-rules-editor" size="middle">
        <Typography.Text type="secondary">所有供应商均校验回答。风险拦截开启：有风险必须人工回调；关闭：允许带风险自动回调。保存后定时刷新，对尚未回调的任务生效，风险记录始终保留。</Typography.Text>
        <Table<Provider> size="small" rowKey="providerCode" loading={loading} dataSource={providers}
          pagination={{ defaultPageSize: 10, hideOnSinglePage: true }} columns={[
            { title: "供应商", dataIndex: "displayName", render: (name: string, row) => name || row.providerCode },
            { title: "供应商编码", dataIndex: "providerCode" },
            { title: "风险拦截", key: "validation", width: 140, render: (_, row) => {
              const enabled = !rules?.riskCallbackAllowedProviderCodes?.includes(row.providerCode);
              return canEdit ? <Switch aria-label={`${row.providerCode} 风险拦截`} checked={enabled} checkedChildren="开启" unCheckedChildren="关闭" disabled={!rules || busy} onChange={(checked) => toggleProvider(row, checked)} /> : <Tag color={enabled ? "success" : "default"}>{enabled ? "开启" : "关闭"}</Tag>;
            } }
          ]} />
      </Space>
    </Card>
    <Card title="校验规则" size="small">
      <Table pagination={false} size="small" rowKey="name" columns={[
        { title: "规则", dataIndex: "name" }, { title: "判定", dataIndex: "condition" }, { title: "标记", dataIndex: "result" }
      ]} dataSource={[
        { name: "回答长度", condition: "正文少于 20 个字母或数字字符", result: "无效" },
        { name: "关键词", condition: "正文包含任一关键词（忽略大小写及全半角差异）", result: "无效" },
        { name: "引用", condition: "当前回答溯源引用数 ≤ 0", result: "风险" }
      ]} />
    </Card>
    <Card title={<Space>关键词清单<Tag>{keywords.length} / 2000</Tag>{dirty && <Tag color="gold">未保存</Tag>}</Space>} size="small" loading={loading && !rules}
      extra={canEdit && <Button danger size="small" icon={<Trash2 size={14} />} disabled={!rules || busy || (!keywords.length && !input.trim())} onClick={() => { setKeywords([]); setInput(""); setError(""); }}>清空清单</Button>}
    >
      <Space direction="vertical" className="validation-rules-editor" size="middle">
        <Typography.Text type="secondary">正文命中清单中的任一关键词时标记为无效。每个关键词不超过 200 字；添加、删除或清空后，点击“保存关键词”生效。</Typography.Text>
        {canEdit ? <>
          <Input.TextArea aria-label="新增关键词，每行一个" value={input} onChange={(event) => setInput(event.target.value)} disabled={!rules || busy} autoSize={{ minRows: 3, maxRows: 8 }} placeholder="输入关键词，每行一个，支持批量粘贴" />
          <Space wrap>
            <Button icon={<Plus size={16} />} disabled={!rules || busy || !input.trim()} onClick={handleAdd}>添加到清单</Button>
            <Typography.Text type="secondary">忽略大小写和全半角差异，重复关键词自动合并。</Typography.Text>
          </Space>
        </> : <Typography.Text type="secondary">当前账号只读，管理员可配置和删除关键词。</Typography.Text>}
        <Table
          size="small"
          rowKey="keyword"
          loading={loading}
          dataSource={keywords.map((keyword) => ({ keyword }))}
          locale={{ emptyText: "暂无关键词" }}
          pagination={{ defaultPageSize: 10, showSizeChanger: true, pageSizeOptions: [10, 20, 50], hideOnSinglePage: true, showTotal: (total) => `共 ${total} 个关键词` }}
          columns={[
            { title: "关键词", dataIndex: "keyword", render: (keyword: string) => <Typography.Text className="validation-rule-keyword">{keyword}</Typography.Text> },
            ...(canEdit ? [{ title: "操作", key: "actions", width: 72, render: (_: unknown, row: { keyword: string }) => <Tooltip title="从清单删除，保存后生效"><Button type="text" danger aria-label={`删除关键词 ${row.keyword}`} icon={<Trash2 size={16} />} disabled={busy} onClick={() => setKeywords((current) => current.filter((keyword) => keyword !== row.keyword))} /></Tooltip> }] : [])
          ]}
        />
        <Typography.Text type="secondary">保存后由各实例定时加载，通常在一个刷新周期内对新导入生效；已导入回答不会重新校验。</Typography.Text>
        <Descriptions size="small" column={{ xs: 1, sm: 2 }}>
          <Descriptions.Item label="已保存版本">{rules?.version || "—"}</Descriptions.Item>
          <Descriptions.Item label="刷新周期">{rules ? `${rules.refreshIntervalSeconds} 秒` : "—"}</Descriptions.Item>
          <Descriptions.Item label="修改人">{rules?.updatedBy || "—"}</Descriptions.Item>
          <Descriptions.Item label="保存时间">{formatFullDateTime(rules?.updatedAt || "")}</Descriptions.Item>
        </Descriptions>
      </Space>
    </Card>
  </section>;
}
