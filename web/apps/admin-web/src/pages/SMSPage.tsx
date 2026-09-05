import { PageHeader } from "@xingmang/ui-admin";
import { SMSPanel } from "../components/SMSPanel";

/** 接码（XM-SMS0）。
 *
 *  与卡片页并列放在「平台治理」下：它们是同一类东西——平台自己持有的、
 *  用来注册与维持外部服务账号的资源（一个是支付工具，一个是手机号）。
 *
 *  这一页在 ADMIN-IA 里没有对应条目，与卡片页同一情况。 */
export function SMSPage() {
  return (
    <div className="flex min-w-0 flex-col gap-4">
      <PageHeader
        title="接码"
        description="62-US 与 Hero-SMS 的号码购买、取码与操作台账。所有写操作经 Action 执行并留审计；买号需要 sms-operator 角色。"
      />
      <SMSPanel />
    </div>
  );
}
