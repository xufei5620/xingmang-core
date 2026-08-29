import { navLabel, PageHeader } from "@xingmang/ui-admin";
import { Badge, EmptyState } from "@xingmang/ui-primitives";
import type { ReactNode } from "react";
import { Link } from "react-router";
import { appApiConfig } from "../api/config";

/** 设置：平台治理段里「管平台自己」的那一页（ADMIN-IA 一、平台治理）。
 *
 *  文档给它的职责有四块：身份权限（只读）、密钥引用（永不明文）、
 *  告警规则与静默（随 XM-0033）、配置中心（后置）。眼下只有第一块能做，
 *  其余三块**列出来但写明还没有**——一个只剩一块内容的设置页，
 *  会让人以为平台就只有这点可配的（§12 惯例）。 */
export function SettingsPage() {
  return (
    <section>
      <PageHeader
        title={navLabel("/settings")}
        description="身份与权限只读展示；写操作一律走 Action，凭据只经 CredentialRef，永不显示明文。"
      />
      <div className="flex flex-col gap-4">
        <IdentitySection />
        <SettingsSection title="密钥引用">
          <EmptyState
            title="密钥引用尚未实现"
            description="将列出各连接使用的 CredentialRef（只显示引用名与状态，永不显示明文值）。ADMIN-IA 未给该块指派任务号。"
          />
        </SettingsSection>
        <SettingsSection title="告警规则与静默">
          <div className="flex flex-col gap-2 rounded-lg border border-edge bg-surface p-4">
            <p className="text-sm font-medium text-fg">告警与故障</p>
            <p className="text-xs leading-5 text-fg-muted">
              查看可用天数 R5 规则、当前阈值、影响预览与变更历史。规则页只读预览；
              写入仍需 Foundation-B / C3c 的审批链。
            </p>
            <Link
              to="/alerts?sub=rules"
              className="self-start text-sm font-medium text-accent hover:underline focus-visible:outline-2 focus-visible:outline-accent"
            >
              打开告警与故障规则 →
            </Link>
          </div>
        </SettingsSection>
        <SettingsSection title="配置中心">
          <EmptyState
            title="配置中心尚未实现"
            description="ADMIN-IA 明确标注为后置项，暂无排期。"
          />
        </SettingsSection>
      </div>
    </section>
  );
}

function SettingsSection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <article className="flex flex-col gap-2">
      <h3 className="text-sm font-semibold text-fg">{title}</h3>
      {children}
    </article>
  );
}

/** 身份与权限（只读）。
 *
 *  这里显示的是**前端这一侧带着哪些 scope 去请求**，不是「服务端给了你哪些权限」。
 *  两者听起来一样，实际差得很远：config.ts 里那份 DEFAULT_SCOPES 是写死的，
 *  多写一个不等于多一分权力，服务端照样 403。把这一页读成「我的权限」，
 *  就会有人拿它去判断某个操作能不能做——所以这句话必须印在页面上，
 *  而不是只留在源码注释里（宪法：前端隐藏不构成安全控制，服务端为最终裁决）。 */
function IdentitySection() {
  const { principalId, principalType, scopes } = appApiConfig;
  return (
    <SettingsSection title="身份与权限（只读）">
      <div className="flex flex-col gap-3 rounded-lg border border-edge bg-surface p-4">
        <dl className="grid grid-cols-[7rem_1fr] gap-x-4 gap-y-2 text-sm">
          <dt className="text-fg-muted">主体 ID</dt>
          <dd className="font-mono text-fg">{principalId || "—"}</dd>
          <dt className="text-fg-muted">主体类型</dt>
          <dd className="text-fg">{principalType}</dd>
          <dt className="text-fg-muted">请求 scope</dt>
          <dd className="flex flex-wrap gap-1">
            {scopes.length === 0 ? (
              <span className="text-fg-muted">—</span>
            ) : (
              scopes.map((scope) => (
                <Badge key={scope} tone="neutral">
                  {scope}
                </Badge>
              ))
            )}
          </dd>
        </dl>
        <p className="border-t border-edge pt-2 text-xs text-fg-muted">
          以上是当前前端<strong className="font-semibold">发出请求时携带</strong>的身份，
          不是服务端授予的权限：多带一个 scope 不等于放权，服务端为最终裁决者，
          越权请求照样 403。当前为开发期身份（X-Dev-* 头），XM-0008 接入 Keycloak
          后由 Access Token 取代。
        </p>
      </div>
    </SettingsSection>
  );
}
