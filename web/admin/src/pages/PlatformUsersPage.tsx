import { useEffect, useMemo, useState } from "react";
import { Button, Drawer, Form, Input, Segmented, Select, Space, Switch, Table, Tabs, Tag, Tooltip, Typography, message } from "antd";
import type { ColumnsType } from "antd/es/table";
import { ArrowDown, ArrowUp, BarChart3, Edit3, Plus, RefreshCw, Search, SlidersHorizontal, Trash2 } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { createPlatformUser, getDispatchOptions, listPlatformUsers, updatePlatformUser } from "../api/client";
import { PageHeader } from "../components/PageHeader";
import type { DispatchProfilePayload, DispatchStrategyOption, PlatformUser, PlatformUserListQuery, PlatformUserPayload, Provider } from "../types/admin";
import { compactText, formatDateTime } from "../utils/format";

type FormValues = Omit<PlatformUserPayload, "dispatchProfile"> & {
  dispatchProfile?: DispatchProfilePayload;
};

const createDefaultDispatchProfile = (): DispatchProfilePayload => ({
  strategy: "ROUND_ROBIN",
  enabled: true,
  providerCodes: [],
  fallbackSelectionPolicy: "ROUND_ROBIN",
  rules: []
});

const strategyLabel: Record<string, string> = {
  ROUND_ROBIN: "顺序分配",
  RULE_BASED: "规则分配"
};

const fallbackSelectionPolicyOptions = [
  { label: "按顺序下发", value: "ROUND_ROBIN" },
  { label: "按使用率最低下发", value: "LOWEST_USAGE_RATE" }
];

function defaultFallbackSelectionPolicy(strategy: string) {
  return strategy === "ROUND_ROBIN" ? "ROUND_ROBIN" : "LOWEST_USAGE_RATE";
}

function createDispatchRuleCode(ruleNumber: number) {
  return `rule_${ruleNumber}_${Date.now().toString(36)}`;
}

function nextDispatchRuleNumber(rules: Array<{ name?: string }> = []) {
  return (
    rules.reduce((max, rule, index) => {
      const match = rule.name?.match(/^规则(\d+)$/);
      const parsed = match ? Number(match[1]) : index + 1;
      return Number.isFinite(parsed) ? Math.max(max, parsed) : max;
    }, 0) + 1
  );
}

function DispatchLabelsInput({ value, onChange }: { value?: string[]; onChange?: (next: string[]) => void }) {
  const [draft, setDraft] = useState("");
  const labels = Array.isArray(value) ? value : [];

  const addLabel = () => {
    const label = draft.trim();
    if (!label || labels.includes(label)) {
      setDraft("");
      return;
    }
    onChange?.([...labels, label]);
    setDraft("");
  };

  const removeLabel = (label: string) => {
    onChange?.(labels.filter((item) => item !== label));
  };

  return (
    <div className="dispatch-label-input">
      {labels.map((label) => (
        <Tag key={label} closable onClose={() => removeLabel(label)}>
          {label}
        </Tag>
      ))}
      <Input
        bordered={false}
        value={draft}
        placeholder={labels.length > 0 ? "" : "输入标签后按回车"}
        onChange={(event) => setDraft(event.target.value)}
        onKeyDown={(event) => {
          if (event.key !== "Enter") return;
          event.preventDefault();
          addLabel();
        }}
      />
    </div>
  );
}

export function PlatformUsersPage() {
  const navigate = useNavigate();
  const [filterForm] = Form.useForm();
  const [editForm] = Form.useForm<FormValues>();
  const [dispatchForm] = Form.useForm<FormValues>();
  const [query, setQuery] = useState<PlatformUserListQuery>({ page: 1, pageSize: 20 });
  const [rows, setRows] = useState<PlatformUser[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [dispatchSaving, setDispatchSaving] = useState(false);
  const [dispatchOptionsLoading, setDispatchOptionsLoading] = useState(false);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [dispatchDrawerOpen, setDispatchDrawerOpen] = useState(false);
  const [editing, setEditing] = useState<PlatformUser | null>(null);
  const [dispatchEditing, setDispatchEditing] = useState<PlatformUser | null>(null);
  const [dispatchStrategies, setDispatchStrategies] = useState<DispatchStrategyOption[]>([]);
  const [dispatchProviders, setDispatchProviders] = useState<Provider[]>([]);

  const load = async (nextQuery = query) => {
    setLoading(true);
    try {
      const data = await listPlatformUsers(nextQuery);
      setRows(data.users);
      setTotal(data.total);
      setQuery({ ...nextQuery, page: data.page, pageSize: data.pageSize });
    } catch (error) {
      message.error(error instanceof Error ? error.message : "加载接入方失败");
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void load(query);
    void loadDispatchOptions();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const loadDispatchOptions = async () => {
    setDispatchOptionsLoading(true);
    try {
      const data = await getDispatchOptions();
      setDispatchStrategies(data.strategies);
      setDispatchProviders(data.providers);
    } catch (error) {
      message.error(error instanceof Error ? error.message : "加载调度选项失败");
    } finally {
      setDispatchOptionsLoading(false);
    }
  };

  const handleSearch = () => {
    const values = filterForm.getFieldsValue();
    void load({
      keyword: values.keyword,
      status: values.status,
      page: 1,
      pageSize: query.pageSize
    });
  };

  const openCreate = () => {
    setEditing(null);
    editForm.resetFields();
    editForm.setFieldsValue({
      status: "ACTIVE",
      callbackMode: "DEFAULT_HTTP",
      dispatchProfile: createDefaultDispatchProfile()
    });
    setDrawerOpen(true);
  };

  const buildDispatchProfilePayload = (user: PlatformUser): DispatchProfilePayload => ({
    code: user.dispatchProfile?.code,
    name: user.dispatchProfile?.name,
    strategy: user.dispatchProfile?.strategy || "ROUND_ROBIN",
    enabled: user.dispatchProfile?.enabled ?? true,
    providerCodes: user.dispatchProfile?.providerCodes ?? [],
    fallbackSelectionPolicy: user.dispatchProfile?.fallbackSelectionPolicy || defaultFallbackSelectionPolicy(user.dispatchProfile?.strategy || "ROUND_ROBIN"),
    rules: (user.dispatchProfile?.rules ?? []).map((rule, index) => ({
      ...rule,
      code: rule.code || createDispatchRuleCode(index + 1),
      name: rule.name || `规则${index + 1}`,
      labels: rule.labels ?? [],
      providerCodes: rule.providerCodes ?? []
    }))
  });

  const buildPlatformFormValues = (user: PlatformUser): FormValues => ({
    platformCode: user.platformCode,
    name: user.name,
    status: user.status,
    callbackMode: user.callbackMode,
    callbackUrl: user.callbackUrl,
    dispatchProfile: buildDispatchProfilePayload(user)
  });

  const normalizePayload = (values: FormValues, fallbackDispatchProfile?: DispatchProfilePayload): PlatformUserPayload => {
    const dispatchProfile = values.dispatchProfile ?? fallbackDispatchProfile ?? createDefaultDispatchProfile();
    const strategy = dispatchProfile.strategy || "ROUND_ROBIN";
    const fallbackSelectionPolicy = dispatchProfile.fallbackSelectionPolicy || defaultFallbackSelectionPolicy(strategy);
    return {
      platformCode: values.platformCode,
      name: values.name,
      status: values.status || "ACTIVE",
      callbackMode: values.callbackMode || "DEFAULT_HTTP",
      callbackUrl: values.callbackUrl,
      dispatchProfile: {
        code: dispatchProfile.code,
        name: dispatchProfile.name,
        strategy,
        enabled: dispatchProfile.enabled ?? true,
        providerCodes: dispatchProfile.providerCodes ?? [],
        fallbackSelectionPolicy,
        rules:
          strategy === "RULE_BASED"
            ? (dispatchProfile.rules ?? []).map((rule, index) => ({
                ...rule,
                code: rule.code || createDispatchRuleCode(index + 1),
                name: rule.name || `规则${index + 1}`,
                sortOrder: (index + 1) * 10,
                labels: rule.labels ?? [],
                providerCodes: rule.providerCodes ?? [],
                enabled: rule.enabled ?? true
              }))
            : []
      }
    };
  };

  const openEdit = (user: PlatformUser) => {
    setEditing(user);
    editForm.resetFields();
    editForm.setFieldsValue(buildPlatformFormValues(user));
    setDrawerOpen(true);
  };

  const openDispatchConfig = (user: PlatformUser) => {
    setDispatchEditing(user);
    dispatchForm.resetFields();
    dispatchForm.setFieldsValue(buildPlatformFormValues(user));
    setDispatchDrawerOpen(true);
    void loadDispatchOptions();
  };

  const handleSubmit = async () => {
    const values = await editForm.validateFields();
    const payload = normalizePayload(values, editing ? buildDispatchProfilePayload(editing) : undefined);
    setSaving(true);
    try {
      if (editing) {
        await updatePlatformUser(editing.id, payload);
        message.success("接入方已更新");
      } else {
        await createPlatformUser(payload);
        message.success("接入方已创建，API Key 已自动生成");
      }
      setDrawerOpen(false);
      void load(query);
    } catch (error) {
      message.error(error instanceof Error ? error.message : "保存接入方失败");
    } finally {
      setSaving(false);
    }
  };

  const handleDispatchSubmit = async () => {
    if (!dispatchEditing) return;
    const values = await dispatchForm.validateFields();
    const payload = normalizePayload(
      {
        ...buildPlatformFormValues(dispatchEditing),
        dispatchProfile: values.dispatchProfile
      },
      buildDispatchProfilePayload(dispatchEditing)
    );
    setDispatchSaving(true);
    try {
      await updatePlatformUser(dispatchEditing.id, payload);
      message.success("调度配置已保存");
      setDispatchDrawerOpen(false);
      setDispatchEditing(null);
      void load(query);
    } catch (error) {
      message.error(error instanceof Error ? error.message : "保存调度配置失败");
    } finally {
      setDispatchSaving(false);
    }
  };

  const providerOptions = useMemo(
    () =>
      dispatchProviders.map((provider) => ({
        label: `${provider.providerCode} · ${provider.displayName}${provider.dailyQuestionQuota > 0 ? ` · ${provider.dailyQuestionQuota}/日` : " · 无上限"}`,
        value: provider.providerCode
      })),
    [dispatchProviders]
  );

  const strategyOptions = useMemo(
    () =>
      dispatchStrategies.length > 0
        ? dispatchStrategies.map((strategy) => ({ label: strategyLabel[strategy.code] ?? strategy.name, key: strategy.code }))
        : [
            { label: strategyLabel.ROUND_ROBIN, key: "ROUND_ROBIN" },
            { label: strategyLabel.RULE_BASED, key: "RULE_BASED" }
          ],
    [dispatchStrategies]
  );

  const renderDispatchFields = () => (
    <>
      <Form.Item name={["dispatchProfile", "strategy"]} hidden rules={[{ required: true, message: "请选择调度模式" }]}>
        <Input />
      </Form.Item>
      <Form.Item name={["dispatchProfile", "enabled"]} label="任务调度" valuePropName="checked">
        <Switch checkedChildren="启用" unCheckedChildren="停用" />
      </Form.Item>
      <Form.Item noStyle shouldUpdate={(prev, current) => prev.dispatchProfile?.strategy !== current.dispatchProfile?.strategy}>
        {({ getFieldValue, setFieldValue }) => {
          const strategy = getFieldValue(["dispatchProfile", "strategy"]) || "ROUND_ROBIN";
          return (
            <Tabs
              className="dispatch-mode-tabs"
              activeKey={strategy}
              onChange={(key) => {
                setFieldValue(["dispatchProfile", "strategy"], key);
                setFieldValue(["dispatchProfile", "fallbackSelectionPolicy"], defaultFallbackSelectionPolicy(key));
              }}
              items={strategyOptions.map((option) => ({
                ...option,
                children:
                  option.key === "RULE_BASED" ? (
                    <Space direction="vertical" size={14} className="dispatch-config-stack">
                      <Form.Item
                        name={["dispatchProfile", "providerCodes"]}
                        label="FALLBACK 供应商池"
                        rules={[{ required: true, message: "请选择 FALLBACK 供应商" }]}
                      >
                        <Select
                          mode="multiple"
                          loading={dispatchOptionsLoading}
                          placeholder="选择兜底供应商"
                          options={providerOptions}
                          onFocus={loadDispatchOptions}
                        />
                      </Form.Item>
                      <Form.Item
                        name={["dispatchProfile", "fallbackSelectionPolicy"]}
                        label="FALLBACK 下发方式"
                        rules={[{ required: true, message: "请选择 FALLBACK 下发方式" }]}
                      >
                        <Segmented options={fallbackSelectionPolicyOptions} />
                      </Form.Item>
                      <Form.List name={["dispatchProfile", "rules"]}>
                        {(fields, { add, remove, move }) => (
                          <Space direction="vertical" size={12} className="dispatch-config-stack">
                            <div className="dispatch-rule-list-header">
                              <Typography.Text strong>规则列表</Typography.Text>
                              <Button
                                icon={<Plus size={16} />}
                                onClick={() => {
                                  const ruleNumber = nextDispatchRuleNumber(getFieldValue(["dispatchProfile", "rules"]));
                                  add({
                                    code: createDispatchRuleCode(ruleNumber),
                                    name: `规则${ruleNumber}`,
                                    enabled: true,
                                    sortOrder: (fields.length + 1) * 10,
                                    labels: [],
                                    providerCodes: []
                                  });
                                }}
                              >
                                添加规则
                              </Button>
                            </div>
                            {fields.map((field, index) => (
                              <div className="rule-editor" key={field.key}>
                                <div className="dispatch-rule-toolbar">
                                  <Space size={8}>
                                    <span className="dispatch-rule-index">{index + 1}</span>
                                    <Typography.Text strong>规则 {index + 1}</Typography.Text>
                                  </Space>
                                  <Space size={4}>
                                    <Tooltip title="上移">
                                      <Button
                                        type="text"
                                        disabled={index === 0}
                                        icon={<ArrowUp size={16} />}
                                        onClick={() => move(field.name, field.name - 1)}
                                      />
                                    </Tooltip>
                                    <Tooltip title="下移">
                                      <Button
                                        type="text"
                                        disabled={index === fields.length - 1}
                                        icon={<ArrowDown size={16} />}
                                        onClick={() => move(field.name, field.name + 1)}
                                      />
                                    </Tooltip>
                                    <Tooltip title="删除规则">
                                      <Button type="text" danger icon={<Trash2 size={16} />} onClick={() => remove(field.name)} />
                                    </Tooltip>
                                  </Space>
                                </div>
                                <div className="dispatch-rule-grid">
                                  <Form.Item name={[field.name, "code"]} hidden>
                                    <Input />
                                  </Form.Item>
                                  <Form.Item name={[field.name, "enabled"]} label="启用规则" valuePropName="checked" initialValue>
                                    <Switch checkedChildren="启用" unCheckedChildren="停用" />
                                  </Form.Item>
                                  <Form.Item name={[field.name, "name"]} label="规则名称">
                                    <Input placeholder={`规则${index + 1}`} />
                                  </Form.Item>
                                  <Form.Item
                                    className="dispatch-rule-field-wide"
                                    name={[field.name, "taskNameRegex"]}
                                    label="任务名称匹配（关键词或者正则表达式）"
                                    rules={[{ required: true, message: "请输入任务名称匹配规则" }]}
                                  >
                                    <Input placeholder="人工校验 或 .*人工校验.*" />
                                  </Form.Item>
                                  <Form.Item className="dispatch-rule-field-wide" name={[field.name, "labels"]} label="标签">
                                    <DispatchLabelsInput />
                                  </Form.Item>
                                  <Form.Item
                                    className="dispatch-rule-field-wide"
                                    name={[field.name, "providerCodes"]}
                                    label="可执行供应商池"
                                    rules={[{ required: true, message: "请选择供应商" }]}
                                  >
                                    <Select
                                      mode="multiple"
                                      loading={dispatchOptionsLoading}
                                      placeholder="选择供应商"
                                      options={providerOptions}
                                      onFocus={loadDispatchOptions}
                                    />
                                  </Form.Item>
                                </div>
                              </div>
                            ))}
                          </Space>
                        )}
                      </Form.List>
                    </Space>
                  ) : (
                    <Form.Item
                      name={["dispatchProfile", "providerCodes"]}
                      label="默认供应商池"
                      rules={[{ required: true, message: "请选择默认供应商" }]}
                    >
                      <Select
                        mode="multiple"
                        loading={dispatchOptionsLoading}
                        placeholder="按选择顺序依次下发"
                        options={providerOptions}
                        onFocus={loadDispatchOptions}
                      />
                    </Form.Item>
                  )
              }))}
            />
          );
        }}
      </Form.Item>
    </>
  );

  const columns = useMemo<ColumnsType<PlatformUser>>(
    () => [
      {
        title: "接入方编码",
        dataIndex: "platformCode",
        fixed: "left",
        width: 170,
        render: (value: string, user) => (
          <Button type="link" className="table-link" onClick={() => navigate(`/platform-users/${user.id}`)}>
            {value}
          </Button>
        )
      },
      {
        title: "名称",
        dataIndex: "name",
        width: 180
      },
      {
        title: "API Key",
        dataIndex: "apiKey",
        width: 300,
        render: (value: string) => (
          <Typography.Text copyable className="mono-text">
            {compactText(value)}
          </Typography.Text>
        )
      },
      {
        title: "调度策略",
        dataIndex: ["dispatchProfile", "strategy"],
        width: 140,
        render: (_: string, user) => (user.dispatchProfile?.strategy ? strategyLabel[user.dispatchProfile.strategy] ?? user.dispatchProfile.strategy : "-")
      },
      {
        title: "供应商池",
        dataIndex: ["dispatchProfile", "providerCodes"],
        width: 260,
        render: (_: string[], user) => {
          const providerCodes = user.dispatchProfile?.providerCodes ?? [];
          if (providerCodes.length === 0) return "-";
          return (
            <Space size={[4, 4]} wrap>
              {providerCodes.map((code) => (
                <Tag key={code}>{code}</Tag>
              ))}
            </Space>
          );
        }
      },
      {
        title: "调度",
        dataIndex: ["dispatchProfile", "enabled"],
        width: 100,
        render: (_: boolean, user) => (user.dispatchProfile?.enabled ? "启用" : "停用")
      },
      {
        title: "回调方式",
        dataIndex: "callbackMode",
        width: 150,
        render: (value: string) => (value === "CUSTOM" ? "定制回调" : "默认 HTTP")
      },
      {
        title: "回调地址",
        dataIndex: "callbackUrl",
        width: 260,
        render: (value: string) => compactText(value || "-")
      },
      {
        title: "状态",
        dataIndex: "status",
        width: 110,
        render: (value: string) => (value === "ACTIVE" ? "启用" : "停用")
      },
      {
        title: "更新时间",
        dataIndex: "updatedAt",
        width: 160,
        render: formatDateTime
      },
      {
        title: "操作",
        fixed: "right",
        width: 176,
        render: (_, user) => (
          <Space>
            <Tooltip title="数据详情">
              <Button icon={<BarChart3 size={16} />} onClick={() => navigate(`/platform-users/${user.id}`)} />
            </Tooltip>
            <Tooltip title="调度配置">
              <Button icon={<SlidersHorizontal size={16} />} onClick={() => openDispatchConfig(user)} />
            </Tooltip>
            <Tooltip title="编辑">
              <Button icon={<Edit3 size={16} />} onClick={() => openEdit(user)} />
            </Tooltip>
          </Space>
        )
      }
    ],
    [navigate]
  );

  return (
    <section>
      <PageHeader
        title="接入方管理"
        description="维护业务接入方、任务调度和任务完成回调方式。"
        extra={
          <Space>
            <Button icon={<RefreshCw size={16} />} onClick={() => load(query)}>
              刷新
            </Button>
            <Button type="primary" icon={<Plus size={16} />} onClick={openCreate}>
              新增接入方
            </Button>
          </Space>
        }
      />

      <div className="filter-bar">
        <Form form={filterForm} layout="inline">
          <Form.Item name="keyword">
            <Input allowClear placeholder="编码 / 名称" prefix={<Search size={15} />} />
          </Form.Item>
          <Form.Item name="status">
            <Select
              allowClear
              placeholder="状态"
              style={{ width: 130 }}
              options={[
                { label: "启用", value: "ACTIVE" },
                { label: "停用", value: "DISABLED" }
              ]}
            />
          </Form.Item>
          <Form.Item>
            <Button type="primary" icon={<Search size={16} />} onClick={handleSearch}>
              查询
            </Button>
          </Form.Item>
        </Form>
      </div>

      <Table
        className="task-table"
        columns={columns}
        dataSource={rows}
        loading={loading}
        pagination={{
          current: query.page,
          pageSize: query.pageSize,
          total,
          showSizeChanger: true,
          onChange: (page, pageSize) => load({ ...query, page, pageSize })
        }}
        rowKey="id"
        scroll={{ x: 1600 }}
      />

      <Drawer
        title={editing ? "编辑接入方" : "新增接入方"}
        width={520}
        open={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        extra={
          <Space>
            <Button onClick={() => setDrawerOpen(false)}>取消</Button>
            <Button type="primary" loading={saving} onClick={handleSubmit}>
              保存
            </Button>
          </Space>
        }
      >
        <Form form={editForm} layout="vertical">
          <Form.Item name="platformCode" label="接入方编码" rules={[{ required: true, message: "请输入接入方编码" }]}>
            <Input placeholder="mainland" />
          </Form.Item>
          <Form.Item name="name" label="名称" rules={[{ required: true, message: "请输入名称" }]}>
            <Input placeholder="Mainland Default" />
          </Form.Item>
          {!editing ? renderDispatchFields() : null}
          <Form.Item name="status" label="状态">
            <Select
              options={[
                { label: "启用", value: "ACTIVE" },
                { label: "停用", value: "DISABLED" }
              ]}
            />
          </Form.Item>
          <Form.Item name="callbackMode" label="回调方式">
            <Select
              options={[
                { label: "默认 HTTP", value: "DEFAULT_HTTP" },
                { label: "定制回调", value: "CUSTOM" }
              ]}
            />
          </Form.Item>
          <Form.Item name="callbackUrl" label="回调地址">
            <Input placeholder="https://example.com/callback" />
          </Form.Item>
        </Form>
      </Drawer>

      <Drawer
        title={`调度配置${dispatchEditing ? ` · ${dispatchEditing.platformCode}` : ""}`}
        width={680}
        open={dispatchDrawerOpen}
        onClose={() => setDispatchDrawerOpen(false)}
        extra={
          <Space>
            <Button onClick={() => setDispatchDrawerOpen(false)}>取消</Button>
            <Button type="primary" loading={dispatchSaving} onClick={handleDispatchSubmit}>
              保存
            </Button>
          </Space>
        }
      >
        <Form form={dispatchForm} layout="vertical">
          {renderDispatchFields()}
        </Form>
      </Drawer>
    </section>
  );
}
