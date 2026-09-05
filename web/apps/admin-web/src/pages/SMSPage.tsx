import { PageHeader } from "@xingmang/ui-admin";
import { Button } from "@xingmang/ui-primitives";
import { useState } from "react";
import { useSearchParams } from "react-router";
import { ActionResultNote, type ActionResult } from "../components/ActionResultNote";
import {
  HeroCatalogPanel,
  HeroEmailsPanel,
  HeroHistoryPanel,
  HeroRentPanel,
  SMS62OrdersPanel,
} from "../components/SMSExtrasPanels";
import { SMSPanel } from "../components/SMSPanel";

/** 页签。第一个是原来的整页（供应商 / 买号 / 号码 / 台账），其余是 XM-SMS1
 *  按两家官方文档补齐后的扩展视图。按供应商标注：只有 Hero 有的就写明。 */
const TABS = [
  { id: "numbers", label: "号码" },
  { id: "catalog", label: "目录与价格（Hero）" },
  { id: "history", label: "历史与统计（Hero）" },
  { id: "emails", label: "邮箱接码（Hero）" },
  { id: "rent", label: "租用（Hero）" },
  { id: "orders62", label: "订单与商品（62）" },
] as const;

type TabID = (typeof TABS)[number]["id"];

function parseTab(raw: string | null): TabID {
  return TABS.some((t) => t.id === raw) ? (raw as TabID) : "numbers";
}

/** 接码（XM-SMS0 / XM-SMS1）。
 *
 *  与卡片页并列放在「平台治理」下：它们是同一类东西——平台自己持有的、
 *  用来注册与维持外部服务账号的资源（一个是支付工具，一个是手机号）。
 *
 *  这一页在 ADMIN-IA 里没有对应条目，与卡片页同一情况。 */
export function SMSPage() {
  const [params, setParams] = useSearchParams();
  const tab = parseTab(params.get("tab"));
  const [result, setResult] = useState<ActionResult | null>(null);

  function setTab(next: TabID) {
    // replace：页签切换是浏览行为，不该在历史里堆一串。
    setParams(
      (prev) => {
        const p = new URLSearchParams(prev);
        p.set("tab", next);
        return p;
      },
      { replace: true },
    );
  }

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <PageHeader
        title="接码中心"
        description="62-US 与 Hero-SMS 的号码购买、取码与操作台账；以及两家官方文档里的全部能力。所有写操作经 Action 执行并留审计；买号、租用、买邮箱需要 sms-operator 角色。"
      />

      <div className="border-edge flex flex-wrap gap-2 border-b pb-2" role="tablist">
        {TABS.map((t) => (
          <Button
            key={t.id}
            size="sm"
            variant={tab === t.id ? "primary" : "secondary"}
            onClick={() => setTab(t.id)}
            role="tab"
            aria-selected={tab === t.id}
          >
            {t.label}
          </Button>
        ))}
      </div>

      {tab !== "numbers" && result ? <ActionResultNote result={result} /> : null}

      {tab === "numbers" ? <SMSPanel /> : null}
      {tab === "catalog" ? <HeroCatalogPanel onWrite={setResult} /> : null}
      {tab === "history" ? <HeroHistoryPanel /> : null}
      {tab === "emails" ? <HeroEmailsPanel onWrite={setResult} /> : null}
      {tab === "rent" ? <HeroRentPanel onWrite={setResult} /> : null}
      {tab === "orders62" ? <SMS62OrdersPanel onWrite={setResult} /> : null}
    </div>
  );
}
