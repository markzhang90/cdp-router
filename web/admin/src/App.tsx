import { ConfigProvider } from "antd";
import zhCN from "antd/locale/zh_CN";
import { Navigate, Route, Routes } from "react-router-dom";
import { AdminLayout } from "./layouts/AdminLayout";
import { LoginPage } from "./pages/LoginPage";
import { PlatformUserDetailPage } from "./pages/PlatformUserDetailPage";
import { PlatformUsersPage } from "./pages/PlatformUsersPage";
import { ProviderDetailPage } from "./pages/ProviderDetailPage";
import { ProviderListPage } from "./pages/ProviderListPage";
import { TaskDetailPage } from "./pages/TaskDetailPage";
import { ValidationRulesPage } from "./pages/ValidationRulesPage";
import { TaskListPage } from "./pages/TaskListPage";

export function App() {
  return (
    <ConfigProvider
      locale={zhCN}
      theme={{
        token: {
          colorPrimary: "#16776b",
          colorInfo: "#16776b",
          borderRadius: 6,
          fontFamily:
            "'Noto Sans SC', 'PingFang SC', 'Hiragino Sans GB', 'Microsoft YaHei', sans-serif"
        },
        components: {
          Layout: {
            bodyBg: "#f5f7f6",
            siderBg: "#16201d",
            headerBg: "#ffffff"
          },
          Menu: {
            darkItemBg: "transparent",
            darkItemColor: "#d7e7e2",
            darkItemHoverBg: "#1c6f62",
            darkItemHoverColor: "#ffffff",
            darkItemSelectedBg: "#176d62",
            darkItemSelectedColor: "#ffffff"
          },
          Table: {
            headerBg: "#f7faf8",
            rowHoverBg: "#eef6f2"
          }
        }
      }}
    >
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route element={<AdminLayout />}>
          <Route path="/validation-rules" element={<ValidationRulesPage />} />
          <Route path="/tasks" element={<TaskListPage />} />
          <Route path="/tasks/:id" element={<TaskDetailPage />} />
          <Route path="/providers" element={<ProviderListPage />} />
          <Route path="/providers/:code" element={<ProviderDetailPage />} />
          <Route path="/platform-users" element={<PlatformUsersPage />} />
          <Route path="/platform-users/:id" element={<PlatformUserDetailPage />} />
          <Route path="/" element={<Navigate to="/tasks" replace />} />
        </Route>
        <Route path="*" element={<Navigate to="/tasks" replace />} />
      </Routes>
    </ConfigProvider>
  );
}
