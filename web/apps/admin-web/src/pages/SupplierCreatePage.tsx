import { PageHeader, PageState } from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import { Link, useParams } from "react-router";
import { NotFoundView } from "./NotFoundPage";
import {
  isSupplyPlatform,
  supplyPlatformLabel,
  upstreamDetailPath,
  type SupplyPlatform,
} from "./ChannelDetailPage";

/** 添加上游的 UI-only 表单蓝图。
 *
 *  这是字段与布局评审页，不是提交表单：所有控件均为 disabled，页面没有
 *  `onSubmit`、Action 调用或凭据输入。这样可以在没有后端契约时先确认上游
 *  名称、网址、账号、充值比例、联系人与分组映射的字段顺序。
 */
export function SupplierCreatePage() {
  const { serviceType = "" } = useParams();

  if (!isSupplyPlatform(serviceType)) {
    return (
      <NotFoundView
        pathname={`/platforms/${serviceType || "（空平台）"}/suppliers/new`}
        detail={`平台 ${serviceType || "（空平台）"} 不支持上游登记`}
      />
    );
  }

  return <SupplierCreateShell platform={serviceType} />;
}
function SupplierCreateShell({ platform }: { platform: SupplyPlatform }) {
  const label = supplyPlatformLabel(platform);
  const backTo = `/platforms/${platform}?tab=suppliers`;

  return (
    <section className="min-w-0">
      <Link
        to={backTo}
        aria-label={`返回 ${label} 上游管理`}
        className="mb-3 inline-flex min-h-9 items-center rounded-md px-2 text-sm font-medium text-accent hover:bg-accent-soft focus-visible:outline-2 focus-visible:outline-accent"
      >
        <span aria-hidden="true">←</span>
        <span className="ml-1">返回上游管理</span>
      </Link>

      <PageHeader
        title="添加上游"
        status={<Badge tone="warning">仅 UI 预览</Badge>}
        description={`${label} · 上游登记字段与布局蓝图`}
      />

      <div className="flex min-w-0 flex-col gap-4">
        <p
          role="status"
          className="rounded-md border-2 border-warning bg-warning/15 px-3 py-2 text-xs text-fg"
        >
          只读表单蓝图：当前不会保存、提交或调用任何 Action。请勿在这里填写真实密码、
          API Key、Token 或其它敏感凭据；正式登记需要获批的后端契约与审批链。
        </p>

        <div className="grid min-w-0 grid-cols-1 gap-4 lg:grid-cols-2">
          <FieldCard title="上游资料" hint="用于标识上游并建立跨平台汇总归属">
            <PreviewField label="上游实际名称" placeholder="未接入 · 例如供应商或中转名称" />
            <PreviewField label="上游网址" placeholder="未接入 · https://…" />
            <PreviewField label="购买/控制台地址" placeholder="未接入 · 可选" />
            <PreviewField label="接入平台" value={label} />
            <PreviewField label="上游联系人" placeholder="未接入 · 姓名 / 组织" />
            <PreviewField label="联系人方式" placeholder="未接入 · 邮箱 / 工单 / IM" />
          </FieldCard>

          <FieldCard title="账号与凭据" hint="只登记引用与状态，不回显任何秘密值">
            <PreviewField label="上游账号" placeholder="未接入 · 账号或邮箱" />
            <PreviewField label="密码" value="仅 CredentialRef（不可查看）" type="password" />
            <PreviewField label="API Key / Token" value="仅 CredentialRef（不可查看）" type="password" />
            <PreviewField label="CredentialRef" placeholder="未接入 · 由密钥服务生成" />
            <PreviewField label="二次验证（MFA）" value="未接入" />
            <PreviewField label="权限范围" value="未接入 · 服务端 Action 最终裁决" />
          </FieldCard>

          <FieldCard title="计费与充值比例" hint="比例是计量型成本的解释证据，不在前端重复计算">
            <PreviewField label="默认币种" value="未接入" />
            <PreviewField label="充值比例" placeholder="未接入 · 例如 0.92" />
            <PreviewField label="充值成本率" value="未接入 · 由后端投影" />
            <PreviewField label="比例生效时间" value="未接入" />
            <PreviewField label="比例变更原因" placeholder="未接入 · 需要审计理由" />
            <PreviewField label="业务日切时区" value="未接入" />
          </FieldCard>

          <FieldCard title="接入分组与成本口径" hint="一个上游可挂多个平台渠道与分组">
            <PreviewField label="上游分组实际名" placeholder="未接入 · 保存后逐组维护" />
            <PreviewField label="分组倍率" value="未接入 · 仅展示，不并入充值比例" />
            <PreviewField label="接入方式" value="未接入 · 上游中转 / 官方 API / 订阅账号" />
            <PreviewField label="支持模型" value="未接入 · 渠道保障后补齐" />
            <PreviewField label="成本归属" value="未接入 · 按渠道/账号核算" />
            <PreviewField label="备注" placeholder="未接入 · 可选" />
          </FieldCard>
        </div>

        <section className="rounded-lg border border-edge bg-surface p-4 shadow-sm">
          <h3 className="text-sm font-semibold text-fg">提交前检查（预览）</h3>
          <div className="mt-3 grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-4">
            {[
              ["平台归属", "未接入"],
              ["凭据引用", "未接入"],
              ["充值比例", "未接入"],
              ["联系人", "未接入"],
            ].map(([label, value]) => (
              <div key={label} className="rounded-md border border-edge bg-surface-muted px-3 py-2">
                <p className="text-xs text-fg-muted">{label}</p>
                <p className="mt-1 text-sm text-fg-muted">{value}</p>
              </div>
            ))}
          </div>
          <PageState
            kind="unavailable"
            compact
            title="提交与保存尚未接入"
            description="正式登记将通过受审批的 Action 完成；当前页面只用于确认字段，不会产生任何写入。"
          />
        </section>

        <p className="text-xs text-fg-muted">
          返回上游管理后，可从登记簿进入上游详情：
          <Link to={upstreamDetailPath(platform, "preview-upstream")} className="ml-1 text-accent hover:underline">
            查看详情结构预览
          </Link>
          。该链接同样不代表已有真实上游记录。
        </p>
      </div>
    </section>
  );
}

function FieldCard({
  title,
  hint,
  children,
}: {
  title: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <section className="min-w-0 rounded-lg border border-edge bg-surface p-4 shadow-sm">
      <header className="mb-3">
        <h3 className="text-sm font-semibold text-fg">{title}</h3>
        {hint ? <p className="mt-1 text-xs text-fg-muted">{hint}</p> : null}
      </header>
      <div className="flex flex-col gap-3">{children}</div>
    </section>
  );
}

function PreviewField({
  label,
  value,
  placeholder,
  type = "text",
}: {
  label: string;
  value?: string;
  placeholder?: string;
  type?: "text" | "password";
}) {
  return (
    <label className="flex min-w-0 flex-col gap-1 text-xs text-fg-muted">
      <span>{label}</span>
      <input
        type={type}
        value={value ?? ""}
        placeholder={placeholder}
        disabled
        aria-label={label}
        className="min-h-9 w-full rounded-md border border-edge bg-surface-muted px-2.5 py-1.5 text-sm text-fg-muted placeholder:text-fg-muted"
        readOnly
      />
    </label>
  );
}
