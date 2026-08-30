import { PageHeader } from "@xingmang/ui-admin";
import { Link } from "react-router";
import { ConnectorConfigPanel } from "../components/ConnectorConfigPanel";
import { CredentialManagementPanel } from "../components/CredentialManagementPanel";
import { ExpectedCredentialsPanel } from "../components/ExpectedCredentialsPanel";

/** 设置下的凭据管理页（XM-CRED0）。
 *
 *  三块自上而下按「上线要做的事」排：先补齐平台需要的凭据，再把平台切到
 *  real，最后是全部凭据元数据的轮换与吊销。这是一个独立的设置子页，不改变
 *  平台页签里的只读「连接与凭据」面板。 */
export function CredentialsPage() {
  return (
    <section className="min-w-0">
      <div className="mb-3 flex items-center gap-2 text-sm">
        <Link
          to="/settings"
          className="text-accent hover:underline focus-visible:outline-2 focus-visible:outline-accent"
        >
          返回设置
        </Link>
        <span aria-hidden="true" className="text-fg-muted">
          /
        </span>
        <span className="font-medium text-fg">凭据管理</span>
      </div>
      <PageHeader
        title="凭据管理"
        description="粘贴即保存、轮换与吊销凭据引用，并切换各平台的接入模式。值只在 Action 请求期间存在，保存后不会回读到页面。"
      />
      <div className="flex min-w-0 flex-col gap-6">
        <ExpectedCredentialsPanel />
        <ConnectorConfigPanel />
        <CredentialManagementPanel />
      </div>
    </section>
  );
}
