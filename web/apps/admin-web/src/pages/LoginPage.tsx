import { Button } from "@xingmang/ui-primitives";
import { useNavigate } from "react-router";
import { devLogin } from "../auth";

/** 登录前壳（XM-0006 退出标准之一）。真实登录在 XM-0008 接入
 *  Keycloak solov-staff Realm + MFA；此按钮届时替换为 OIDC 跳转。 */
export function LoginPage() {
  const navigate = useNavigate();
  return (
    <div className="flex min-h-screen items-center justify-center bg-canvas font-sans">
      <div className="w-80 rounded-lg border border-edge bg-surface p-8 shadow-md">
        <h1 className="mb-1 text-lg font-semibold text-fg">星芒统一控制平台</h1>
        <p className="mb-6 text-xs text-fg-muted">
          员工入口（solov-staff）。Keycloak 登录将于 XM-0008 接入。
        </p>
        <Button
          className="w-full"
          onClick={() => {
            devLogin();
            void navigate("/dashboard");
          }}
        >
          开发模式进入
        </Button>
      </div>
    </div>
  );
}
