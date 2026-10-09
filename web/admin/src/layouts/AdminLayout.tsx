import { useEffect, useState } from "react";
import { Button, Layout, Menu, message } from "antd";
import { ClipboardList, ShieldCheck, LogOut, RefreshCcw, Server, UserRoundCog } from "lucide-react";
import { Outlet, useLocation, useNavigate } from "react-router-dom";
import { getMe, logout } from "../api/client";
import { getCSRFToken, getStoredUser, setSession } from "../api/session";
import type { AdminUser } from "../types/admin";

export function AdminLayout() {
  const navigate = useNavigate();
  const location = useLocation();
  const [user, setUser] = useState<AdminUser | null>(getStoredUser());
  const selectedKey = location.pathname.startsWith("/validation-rules") ? "validation-rules" : location.pathname.startsWith("/platform-users")
    ? "platform-users"
    : location.pathname.startsWith("/providers")
      ? "providers"
      : "tasks";

  useEffect(() => {
    let alive = true;
    getMe()
      .then((me) => {
        if (!alive) return;
        setUser(me.user);
        setSession(me.csrfToken || getCSRFToken(), me.user);
      })
      .catch(() => {
        if (!alive) return;
        navigate("/login", { replace: true, state: { from: location.pathname } });
      });
    return () => {
      alive = false;
    };
  }, [location.pathname, navigate]);

  const handleLogout = async () => {
    try {
      await logout();
      navigate("/login", { replace: true });
    } catch (error) {
      message.error(error instanceof Error ? error.message : "退出失败");
    }
  };

  return (
    <Layout className="admin-shell">
      <Layout.Sider width={224} className="admin-sider">
        <div className="brand">
          <div className="brand-mark">
            <RefreshCcw size={18} />
          </div>
          <div>
            <strong>Mainland</strong>
            <span>Console</span>
          </div>
        </div>
        <Menu
          theme="dark"
          mode="inline"
          selectedKeys={[selectedKey]}
          items={[
            {
              key: "tasks",
              icon: <ClipboardList size={17} />,
              label: "任务"
            },
            { key: "validation-rules", icon: <ShieldCheck size={17} />, label: "数据校验规则" },
            {
              key: "providers",
              icon: <Server size={17} />,
              label: "供应商"
            },
            {
              key: "platform-users",
              icon: <UserRoundCog size={17} />,
              label: "接入方"
            }
          ]}
          onClick={({ key }) => navigate(`/${key}`)}
        />
      </Layout.Sider>
      <Layout>
        <Layout.Header className="topbar">
          <div className="topbar-title">任务运行控制台</div>
          <div className="topbar-user">
            <span>{user?.username ?? "-"}</span>
            <Button icon={<LogOut size={16} />} onClick={handleLogout}>
              退出
            </Button>
          </div>
        </Layout.Header>
        <Layout.Content className="admin-content">
          <Outlet />
        </Layout.Content>
      </Layout>
    </Layout>
  );
}
