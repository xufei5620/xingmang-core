import { useMutation } from "@tanstack/react-query";
import { Button, Dialog, FormField, Input } from "@xingmang/ui-primitives";
import { useId, useState } from "react";
import {
  refundProxyAsset,
  refundSubscriptionBatch,
  SUBSCRIPTION_MANAGE_PERMISSION,
  terminateProxyAsset,
  terminateSubscriptionBatch,
  type MoneyItem,
  type ProxyAssetItem,
  type SubscriptionBatchItem,
} from "../api/finance";
import { formatScaledMinorUnits } from "../lib/money";
import {
  lifecycleRefundFields,
  lifecycleTerminateFields,
  MAX_AMOUNT_INPUT_LENGTH,
  validateLifecycleForm,
  type LifecycleAction,
  type LifecycleFormErrors,
  type LifecycleFormValues,
} from "../lib/subscriptionForms";
import { ActionErrorNote } from "./ActionErrorNote";
import type { ActionResult } from "./ActionResultNote";

/** 生命周期动作作用在哪一类资源上。
 *
 *  两类资源的 Action id 与**资源 id 的字段名都不同**
 *  （`subscription_batch_id` / `proxy_asset_id`），所以这个判别式一路带到
 *  提交那一行，不在中途折叠成一个笼统的「id」。 */
export type LifecycleResource = "subscription_batch" | "proxy_asset";

/** 一次退款 / 终止要看的那些现状，从只读 DTO 里摘出来。
 *
 *  刻意做成一个中间形状而不是让对话框直接吃 `SubscriptionBatchItem` 与
 *  `ProxyAssetItem` 两个类型：这两张表的字段名对不上（`starts_on` /
 *  `opened_on`），而对话框要说的话是同一句。差异在 `subjectFromBatch` /
 *  `subjectFromProxy` 里一次性抹平，抹平的方式在那两个函数里看得见。 */
export interface LifecycleSubject {
  resource: LifecycleResource;
  id: string;
  /** 人读的标识，用在按钮 title 与确认句里（这里用起止日期，因为 UUID
   *  在确认文案里帮不了人判断「是不是这一笔」）。 */
  label: string;
  currency: string;
  /** 当前已登记的**累计**退款额。 */
  refunded: MoneyItem;
  costBasis: MoneyItem;
  startsOn: string;
  expiresOn: string;
  refundedOn: string | null;
  terminatedOn: string | null;
}

export function subjectFromBatch(batch: SubscriptionBatchItem): LifecycleSubject {
  return {
    resource: "subscription_batch",
    id: batch.id,
    label: `${batch.starts_on} → ${batch.expires_on}`,
    currency: batch.currency,
    refunded: batch.refunded,
    costBasis: batch.cost_basis,
    startsOn: batch.starts_on,
    expiresOn: batch.expires_on,
    refundedOn: batch.refunded_on,
    terminatedOn: batch.terminated_on,
  };
}

export function subjectFromProxy(proxy: ProxyAssetItem): LifecycleSubject {
  return {
    resource: "proxy_asset",
    id: proxy.id,
    // 代理的「开通日」在 DTO 里叫 opened_on，语义与批次的 starts_on 同位
    label: `${proxy.opened_on} → ${proxy.expires_on}`,
    currency: proxy.currency,
    refunded: proxy.refunded,
    costBasis: proxy.cost_basis,
    startsOn: proxy.opened_on,
    expiresOn: proxy.expires_on,
    refundedOn: proxy.refunded_on,
    terminatedOn: proxy.terminated_on,
  };
}

export interface SubscriptionLifecycleDialogProps {
  subject: LifecycleSubject;
  action: LifecycleAction;
  /** 上游账号的业务日切时区。有值时写进日期那一格的说明——这些日期按它
   *  解释，不按浏览器时区（宪法 14 条）。 */
  businessDayTz?: string;
  disabled?: boolean;
  onDone: (result: ActionResult) => void;
}

const EMPTY_FORM: LifecycleFormValues = {
  refundedMajor: "",
  // **刻意不预填今天**：浏览器时区与业务日切时区可能不是同一天，
  // 预填出来的那个日期看起来完全正常，却会把成本记到隔壁那一天去。
  effectiveOn: "",
  reason: "",
  confirmed: false,
};

/** 订阅批次 / 代理资产的退款与终止入口（四个 L1 Action 的界面）。
 *
 *  **这四个动作不可逆**，所以入口不是列表行里一个图标：触发器是有名字的按钮，
 *  点开之后先把后果一条条写出来，再要一次勾选确认，勾之前提交不会发出任何请求。
 *
 *  为什么**不用** `ApprovalReasonField`：那个组件的标签是「理由（提交审批用）」,
 *  提示语写着「会先落成一张审批单」。这四个是 L1，内核不会受理成审批单，
 *  提交即执行——用它等于在界面上说一句不会发生的事。这里的理由进的是**审计
 *  事件**（Handler 的 `action.RecordReason`），说明文案照这个事实写。 */
export function SubscriptionLifecycleDialog({
  subject,
  action,
  businessDayTz,
  disabled = false,
  onDone,
}: SubscriptionLifecycleDialogProps) {
  const [open, setOpen] = useState(false);
  const [values, setValues] = useState<LifecycleFormValues>(EMPTY_FORM);
  const [errors, setErrors] = useState<LifecycleFormErrors>({});
  const prefix = useId();
  const copy = LIFECYCLE_COPY[action][subject.resource];
  const alreadyTerminated = Boolean(subject.terminatedOn);

  const mutation = useMutation({
    mutationFn: (form: LifecycleFormValues) => submitLifecycle(subject, action, form),
    onSuccess: (run) => {
      setOpen(false);
      setValues(EMPTY_FORM);
      setErrors({});
      onDone({ title: copy.doneTitle, runId: run.runId });
    },
  });

  // 终止只能做一次（仓储的 ErrAlreadyTerminated）。而那条拒绝在界面上只会
  // 显示成一句「执行失败」——服务端说不清的事，入口自己先收起来，并说明原因。
  if (action === "terminate" && alreadyTerminated) {
    return (
      <span className="text-xs text-fg-muted">
        已于 {subject.terminatedOn} 终止；终止只能做一次，终止日登记后不可再改。
      </span>
    );
  }

  const set = <K extends keyof LifecycleFormValues>(field: K, value: LifecycleFormValues[K]) => {
    setValues((previous) => ({ ...previous, [field]: value }));
    setErrors((previous) => {
      if (!(field in previous)) return previous;
      const next = { ...previous };
      delete next[field];
      return next;
    });
  };

  const submit = () => {
    const found = validateLifecycleForm(values, action, {
      refundedMinor: subject.refunded.amount_minor,
      refundedScale: subject.refunded.scale,
      currency: subject.currency,
    });
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    mutation.mutate(values);
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        // 开与关都清空：一次没提交完的退款金额留在那里，下次打开会被当成
        // 「上次填到一半的」还是「系统建议的」分不清，而这一格是钱
        setValues(EMPTY_FORM);
        setErrors({});
        mutation.reset();
      }}
      trigger={
        <Button
          size="sm"
          variant={action === "terminate" ? "danger" : "secondary"}
          disabled={disabled}
          title={disabled ? `需要 ${SUBSCRIPTION_MANAGE_PERMISSION}` : copy.triggerHint}
        >
          {copy.trigger}
        </Button>
      }
      title={copy.title}
      description={copy.description}
    >
      <form
        className="flex max-h-[65vh] flex-col gap-3 overflow-y-auto"
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <div className="rounded-md border border-warning bg-warning/10 px-3 py-2 text-xs text-fg">
          <p className="font-medium">{copy.consequenceTitle}</p>
          <ul className="mt-1 flex list-disc flex-col gap-1 pl-4">
            {copy.consequences.map((line) => (
              <li key={line}>{line}</li>
            ))}
          </ul>
        </div>

        <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-xs">
          <Fact label="有效期" value={subject.label} />
          <Fact label="当前成本基数" value={formatMoney(subject.costBasis)} />
          <Fact
            label="已登记累计退款"
            value={
              subject.refundedOn
                ? `${formatMoney(subject.refunded)}（生效日 ${subject.refundedOn}）`
                : formatMoney(subject.refunded)
            }
          />
          <Fact label="终止状态" value={subject.terminatedOn ?? "未终止"} />
        </dl>

        {action === "refund" ? (
          <FormField
            label="累计退款总额"
            htmlFor={`${prefix}-refunded`}
            required
            error={errors.refundedMajor}
            hint={`人类主单位输入；提交为 scale-6 整数字符串。填的是累计总额（含此前已登记的 ${formatMoney(subject.refunded)}），不是本次新增。`}
          >
            <Input
              value={values.refundedMajor}
              maxLength={MAX_AMOUNT_INPUT_LENGTH}
              inputMode="decimal"
              invalid={Boolean(errors.refundedMajor)}
              onChange={(event) => set("refundedMajor", event.target.value)}
            />
          </FormField>
        ) : null}

        <FormField
          label={copy.dateLabel}
          htmlFor={`${prefix}-date`}
          required
          error={errors.effectiveOn}
          hint={dateHint(copy.dateHint, subject, businessDayTz)}
        >
          <Input
            type="date"
            value={values.effectiveOn}
            invalid={Boolean(errors.effectiveOn)}
            onChange={(event) => set("effectiveOn", event.target.value)}
          />
        </FormField>

        <FormField
          label="理由"
          htmlFor={`${prefix}-reason`}
          required
          error={errors.reason}
          hint="原样进审计事件，是事后唯一说得清「这笔钱为什么动」的记录；由人自己写，界面不预填也不代拟。"
        >
          <Input
            value={values.reason}
            invalid={Boolean(errors.reason)}
            onChange={(event) => set("reason", event.target.value)}
          />
        </FormField>

        <div className="flex flex-col gap-1">
          <label
            htmlFor={`${prefix}-confirm`}
            className="inline-flex min-h-11 cursor-pointer items-start gap-2 text-xs text-fg"
          >
            <input
              id={`${prefix}-confirm`}
              type="checkbox"
              checked={values.confirmed}
              aria-invalid={Boolean(errors.confirmed) || undefined}
              onChange={(event) => set("confirmed", event.target.checked)}
              className="mt-0.5 size-4 shrink-0 accent-accent"
            />
            <span>{copy.confirmLabel(subject)}</span>
          </label>
          {errors.confirmed ? (
            <p role="alert" className="text-xs text-danger">
              {errors.confirmed}
            </p>
          ) : null}
        </div>

        <ActionErrorNote error={mutation.error} permission={SUBSCRIPTION_MANAGE_PERMISSION} />

        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" size="sm" onClick={() => setOpen(false)}>
            取消
          </Button>
          <Button
            type="submit"
            size="sm"
            variant={action === "terminate" ? "danger" : "primary"}
            loading={mutation.isPending}
          >
            {copy.submit}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

function Fact({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0">
      <dt className="text-fg-muted">{label}</dt>
      <dd className="text-fg tabular-nums">{value}</dd>
    </div>
  );
}

function formatMoney(m: MoneyItem): string {
  return formatScaledMinorUnits(m.amount_minor, m.currency, m.scale);
}

function dateHint(base: string, subject: LifecycleSubject, tz: string | undefined): string {
  const period = `必须落在有效期 ${subject.startsOn}..${subject.expiresOn} 内（服务端判定，起止含两端）。`;
  const zone = tz ? `日期按业务日切时区 ${tz} 解释，不按浏览器时区。` : "";
  return `${base}${period}${zone}不预填今天：浏览器上的「今天」未必是业务日上的今天。`;
}

/** 按资源分发到对应的 Action 包装函数。
 *
 *  四个分支各自写全资源 id 的字段名，**不做成一个变量**：两个 Action 的
 *  id 字段不同名，把它抽成 `[idField]: subject.id` 之后，改错一个名字不会
 *  在类型上报错，只会在运行时换来一句 Schema 校验失败。 */
function submitLifecycle(
  subject: LifecycleSubject,
  action: LifecycleAction,
  values: LifecycleFormValues,
) {
  if (action === "refund") {
    const fields = lifecycleRefundFields(values);
    return subject.resource === "subscription_batch"
      ? refundSubscriptionBatch({ subscription_batch_id: subject.id, ...fields })
      : refundProxyAsset({ proxy_asset_id: subject.id, ...fields });
  }
  const fields = lifecycleTerminateFields(values);
  return subject.resource === "subscription_batch"
    ? terminateSubscriptionBatch({ subscription_batch_id: subject.id, ...fields })
    : terminateProxyAsset({ proxy_asset_id: subject.id, ...fields });
}

interface LifecycleCopy {
  trigger: string;
  triggerHint: string;
  title: string;
  description: string;
  consequenceTitle: string;
  consequences: string[];
  dateLabel: string;
  dateHint: string;
  confirmLabel: (subject: LifecycleSubject) => string;
  submit: string;
  /** 回执标题。用动词过去式说清**发生了什么**，不写「操作成功」。 */
  doneTitle: string;
}

/** 退款与终止的后果说明。
 *
 *  每一条都能在后端指出出处，不是我替业务想的：
 *  - 「累计而非增量」「重放不会让退款变两倍」——`subscriptionBatchRefundDef` 注释；
 *  - 「决定从哪天起重算剩余未摊天（§12.1）」——同上；
 *  - 「只增不减」——`SetBatchRefund` / `SetProxyRefund` 的 `ErrRefundNotDecreasing`；
 *  - 「结转一笔损失，是报表上单列的会计事件」——`subscriptionBatchTerminateDef` 注释；
 *  - 「只能做一次、终止日不可再改」——`TerminateBatch` 的 `ErrAlreadyTerminated`；
 *  - 「终止日须在有效期内，期外的提前失效什么也没提前」——`AmortizationTerm.Validate`。 */
const LIFECYCLE_COPY: Record<LifecycleAction, Record<LifecycleResource, LifecycleCopy>> = {
  refund: {
    subscription_batch: {
      trigger: "记退款",
      triggerHint: "登记这笔批次的累计退款额；退款额只增不减，登记后无法调低",
      title: "记一笔订阅批次退款",
      description: "finance.subscription_batch.refund@1：登记累计退款额，冲减成本基础。",
      consequenceTitle: "提交前先看清这三条：",
      consequences: [
        "这一格填的是累计退款总额，不是本次新增；服务端按它整笔替换已登记值（因此重复提交同一个数不会让退款翻倍）。",
        "成本基础按新的累计退款额重算，退款生效日决定从哪天起重算剩余未摊天。",
        "累计退款额只增不减：登记之后无法调低，服务端会拒绝。",
      ],
      dateLabel: "退款生效日",
      dateHint: "决定从哪天起冲减剩余未摊天；",
      confirmLabel: (subject) =>
        `我确认要为批次 ${subject.label} 登记累计退款额，并知道这个数登记后只能增不能减。`,
      submit: "提交退款登记",
      doneTitle: "订阅批次已登记退款",
    },
    proxy_asset: {
      trigger: "记退款",
      triggerHint: "登记这份代理的累计退款额；退款额只增不减，登记后无法调低",
      title: "记一笔代理资产退款",
      description: "finance.proxy_asset.refund@1：登记累计退款额，冲减成本基础。",
      consequenceTitle: "提交前先看清这三条：",
      consequences: [
        "这一格填的是累计退款总额，不是本次新增；服务端按它整笔替换已登记值（因此重复提交同一个数不会让退款翻倍）。",
        "成本基础按新的累计退款额重算，退款生效日决定从哪天起重算剩余未摊天。",
        "累计退款额只增不减：登记之后无法调低，服务端会拒绝。",
      ],
      dateLabel: "退款生效日",
      dateHint: "决定从哪天起冲减剩余未摊天；",
      confirmLabel: (subject) =>
        `我确认要为代理 ${subject.label} 登记累计退款额，并知道这个数登记后只能增不能减。`,
      submit: "提交退款登记",
      doneTitle: "代理资产已登记退款",
    },
  },
  terminate: {
    subscription_batch: {
      trigger: "终止",
      triggerHint: "提前失效这笔批次并结转损失；终止只能做一次，不可撤销",
      title: "终止订阅批次",
      description:
        "finance.subscription_batch.terminate@1：提前失效并结转损失。这一步不可撤销。",
      consequenceTitle: "终止不可撤销。提交前先看清这三条：",
      consequences: [
        "自终止日起这笔批次不再产生摊销。",
        "剩余未摊销的部分会结转成一笔损失，记进损失科目——那是报表上单列的一个会计事件。",
        "终止只能做一次：终止日登记之后不可再改，平台没有「撤销终止」的 Action。要改只能找人直接改库。",
      ],
      dateLabel: "终止日",
      dateHint: "决定结转多少损失；期外的「提前失效」什么也没提前，因此",
      confirmLabel: (subject) =>
        `我确认要终止批次 ${subject.label}，并知道它会结转一笔损失、且无法撤销。`,
      submit: "确认终止",
      doneTitle: "订阅批次已终止，损失已结转",
    },
    proxy_asset: {
      trigger: "终止",
      triggerHint: "提前失效这份代理并结转损失；终止只能做一次，不可撤销",
      title: "终止代理资产",
      description: "finance.proxy_asset.terminate@1：提前失效并结转损失。这一步不可撤销。",
      consequenceTitle: "终止不可撤销。提交前先看清这四条：",
      consequences: [
        "自终止日起这份代理不再产生摊销。",
        "剩余未摊销的部分会结转成一笔损失，记进损失科目——那是报表上单列的一个会计事件。",
        "终止只能做一次：终止日登记之后不可再改，平台没有「撤销终止」的 Action。要改只能找人直接改库。",
        "只是这份代理今天不在服务，用「修改代理」取消挂载即可——那是可逆的，且不结转损失。",
      ],
      dateLabel: "终止日",
      dateHint: "决定结转多少损失；期外的「提前失效」什么也没提前，因此",
      confirmLabel: (subject) =>
        `我确认要终止代理 ${subject.label}，并知道它会结转一笔损失、且无法撤销。`,
      submit: "确认终止",
      doneTitle: "代理资产已终止，损失已结转",
    },
  },
};
