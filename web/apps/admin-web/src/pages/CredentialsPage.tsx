import { PageHeader } from "@xingmang/ui-admin";
import { Link } from "react-router";
import { CredentialManagementPanel } from "../components/CredentialManagementPanel";

/** 设置下的凭据管理页（XM-CRED0）。
 *
 *  这是一个独立的设置子页，不改变平台页签里的只读「连接与凭据」面板。
 *  列表 Query 与三个 Action 的后端接线由后续片完成；本页先把安全可预览的
 *  交互边界冻结下来。 */
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
        description="添加、修改和吊销凭据引用。值只在 Action 请求期间存在，保存后不可读回。"
      />
      <CredentialManagementPanel />
    </section>
  );
}

