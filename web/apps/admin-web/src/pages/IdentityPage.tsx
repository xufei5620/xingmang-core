import { navItemByPath, navLabel, PageHeader, PageState } from "@xingmang/ui-admin";
import { Tabs } from "@xingmang/ui-primitives";
import { Link, useSearchParams } from "react-router";
import { StaffAccountsPanel } from "../components/StaffAccountsPanel";

const IDENTITY_SUB_TABS = (navItemByPath("/identity")?.item.subTabs ?? []).map(
  (tab) => [tab.id, tab.label] as const,
);

const DEFAULT_SUB_TAB = "accounts";

/** 人员与权限页（XM-LOGIN）。
 *
 *  五个子页里，本片只接通「账号与身份」一页的真实数据（员工账号的建立、
 *  改角色、禁用/启用、重置密码）；权限规则、权限范围、密钥引用、会话四页
 *  仍是诚实占位——不假装已经实现（§12 惯例：不能把「没建」说成「建了」）。
 *
 *  结构照抄 pages/AuditPage.tsx 的子页签模式：外层只做「认不认识这个 ?sub=」
 *  的判断，认识就交给 Tabs，每个子页自己决定渲染什么（真数据或占位）。 */
export function IdentityPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const rawSub = searchParams.get("sub");
  const activeSub = rawSub === null || rawSub.trim() === "" ? DEFAULT_SUB_TAB : rawSub;
  const known = IDENTITY_SUB_TABS.some(([value]) => value === activeSub);

  if (!known) {
    return (
      <section>
        <PageHeader
          title={navLabel("/identity")}
          description="账号与身份、权限规则、权限范围、密钥引用与会话分开呈现；未知子页不会回落到账号列表。"
        />
        <PageState
          kind="unavailable"
          title={`「${rawSub}」子页尚未接入`}
          description="请从已定义的子页中选择；系统不会把未知地址误当成账号列表。"
          action={
            <Link
              to={`/identity?sub=${DEFAULT_SUB_TAB}`}
              className="text-sm font-medium text-accent hover:underline"
            >
              返回账号与身份
            </Link>
          }
        />
      </section>
    );
  }

  const selectSub = (value: string) => {
    const next = new URLSearchParams(searchParams);
    next.set("sub", value);
    // 换子页签用 replace：连点几格不该在浏览器历史里堆好几条
    setSearchParams(next, { replace: true });
  };

  return (
    <Tabs
      value={activeSub}
      onValueChange={selectSub}
      items={IDENTITY_SUB_TABS.map(([value, label]) => ({
        value,
        label,
        content:
          value === "accounts" ? <IdentityAccountsPage /> : <IdentityUnavailablePage label={label} />,
      }))}
    />
  );
}

function IdentityAccountsPage() {
  return (
    <section>
      <PageHeader
        title={navLabel("/identity")}
        description="管理台自带账号密码登录（local 模式）的员工账号：创建账号、改角色、禁用/启用、重置密码。"
      />
      <StaffAccountsPanel />
    </section>
  );
}

function IdentityUnavailablePage({ label }: { label: string }) {
  return (
    <section>
      <PageHeader
        title={label}
        description="本次（XM-LOGIN）只接通了「账号与身份」一页的真实数据，这一页仍是占位。"
      />
      <PageState kind="unavailable" title={`「${label}」尚未实现`} description="阶段 F-A：随后续切片实现。" />
    </section>
  );
}
