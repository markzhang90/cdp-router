import { useEffect, useMemo, useState } from "react";
import { Button, Drawer, Form, Input, Select, Space, Table, Tooltip, Typography, message } from "antd";
import type { ColumnsType } from "antd/es/table";
import { BarChart3, Edit3, Plus, RefreshCw, Search } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { createPlatformUser, listPlatformUsers, updatePlatformUser } from "../api/client";
import { PageHeader } from "../components/PageHeader";
import type { PlatformUser, PlatformUserListQuery, PlatformUserPayload } from "../types/admin";
import { compactText, formatDateTime } from "../utils/format";

type FormValues = PlatformUserPayload;

export function PlatformUsersPage() {
  const navigate = useNavigate();
  const [filterForm] = Form.useForm();
  const [editForm] = Form.useForm<FormValues>();
  const [query, setQuery] = useState<PlatformUserListQuery>({ page: 1, pageSize: 20 });
  const [rows, setRows] = useState<PlatformUser[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [editing, setEditing] = useState<PlatformUser | null>(null);

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
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

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
      callbackMode: "DEFAULT_HTTP"
    });
    setDrawerOpen(true);
  };

  const openEdit = (user: PlatformUser) => {
    setEditing(user);
    editForm.setFieldsValue({
      platformCode: user.platformCode,
      name: user.name,
      status: user.status,
      defaultProviderCode: user.defaultProviderCode,
      callbackMode: user.callbackMode,
      callbackUrl: user.callbackUrl
    });
    setDrawerOpen(true);
  };

  const handleSubmit = async () => {
    const values = await editForm.validateFields();
    setSaving(true);
    try {
      if (editing) {
        await updatePlatformUser(editing.id, values);
        message.success("接入方已更新");
      } else {
        await createPlatformUser(values);
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
        title: "默认供应商",
        dataIndex: "defaultProviderCode",
        width: 140
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
        width: 136,
        render: (_, user) => (
          <Space>
            <Tooltip title="数据详情">
              <Button icon={<BarChart3 size={16} />} onClick={() => navigate(`/platform-users/${user.id}`)} />
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
        description="维护业务接入方、默认供应商和任务完成回调方式。"
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
        scroll={{ x: 1560 }}
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
          <Form.Item
            name="defaultProviderCode"
            label="默认供应商"
            rules={[{ required: true, message: "请输入默认供应商编码" }]}
          >
            <Input placeholder="mpzdata" />
          </Form.Item>
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
    </section>
  );
}
