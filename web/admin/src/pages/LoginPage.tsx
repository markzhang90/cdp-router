import { useState } from "react";
import { Button, Card, Form, Input, message } from "antd";
import { Lock, User } from "lucide-react";
import { useLocation, useNavigate } from "react-router-dom";
import { login } from "../api/client";
import { setSession } from "../api/session";

interface LocationState {
  from?: string;
}

export function LoginPage() {
  const navigate = useNavigate();
  const location = useLocation();
  const [loading, setLoading] = useState(false);
  const from = (location.state as LocationState | null)?.from || "/tasks";

  const handleFinish = async (values: { username: string; password: string }) => {
    setLoading(true);
    try {
      const data = await login(values.username, values.password);
      setSession(data.csrfToken, data.user);
      navigate(from, { replace: true });
    } catch (error) {
      message.error(error instanceof Error ? error.message : "登录失败");
    } finally {
      setLoading(false);
    }
  };

  return (
    <main className="login-page">
      <section className="login-panel">
        <div className="login-copy">
          <div className="login-mark">ML</div>
          <h1>Mainland Console</h1>
          <p>查看任务进度、结果导入状态和供应商补偿链路。</p>
        </div>
        <Card className="login-card" bordered={false}>
          <Form layout="vertical" requiredMark={false} onFinish={handleFinish}>
            <Form.Item label="账号" name="username" rules={[{ required: true, message: "请输入账号" }]}>
              <Input autoComplete="username" prefix={<User size={16} />} placeholder="admin" size="large" />
            </Form.Item>
            <Form.Item label="密码" name="password" rules={[{ required: true, message: "请输入密码" }]}>
              <Input.Password
                autoComplete="current-password"
                prefix={<Lock size={16} />}
                placeholder="请输入密码"
                size="large"
              />
            </Form.Item>
            <Button block htmlType="submit" loading={loading} size="large" type="primary">
              登录
            </Button>
          </Form>
        </Card>
      </section>
    </main>
  );
}
