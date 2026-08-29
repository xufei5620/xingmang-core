import { useMutation } from "@tanstack/react-query";
import { Button, Dialog, FormField, Input, Select } from "@xingmang/ui-primitives";
import { useEffect, useId, useRef, useState } from "react";
import {
  setUpstreamAccount,
  UPSTREAM_ACCOUNT_MANAGE_PERMISSION,
  type UpstreamAccountItem,
} from "../api/finance";
import {
  ACCESS_METHOD_OPTIONS,
  buildUpstreamParams,
  EMPTY_UPSTREAM_FORM,
  hasUpstreamErrors,
  ratioRequirement,
  SYSTEM_TYPE_OPTIONS,
  UPSTREAM_STATUS_OPTIONS,
  upstreamFieldLabel,
  validateUpstreamForm,
  type UpstreamFormErrors,
  type UpstreamFormField,
  type UpstreamFormValues,
} from "../lib/upstreamForm";
import { ActionErrorNote } from "./ActionErrorNote";

export interface UpstreamAccountDialogProps {
  /** 当前平台 ID，新登记时预填到「接入平台」。 */
  platform: string;
  /** 传了 = 修改这一条；不传 = 新登记。 */
  account?: UpstreamAccountItem;
  onDone: (runId: string) => void;
}

/** 登记 / 修改上游账号(`finance.upstream_account.set@1`,L1 直执行)。
 *
 *  这一整页是**登记簿的 UI**，不是第二份存储：所有写路径都经 Action 内核
 *  (宪法 2 条)，因此每次提交都会拿到一个 run_id，也因此这里没有任何本地缓存
 *  或乐观更新——写完重新查一次，页面上的数就一定是库里的数。
 *
 *  修改模式下 system_type 与 access_method **锁死**：它们是成本口径的分叉点,
 *  改了会让同一个账号的历史成本前后用两套算法算出来，而台账里毫无痕迹。
 *  后端会当场拒绝（finance/actions.go），这里做成只读是为了让人在填之前
 *  就看见这条规则，而不是提交后收一个 400。 */
export function UpstreamAccountDialog({ platform, account, onDone }: UpstreamAccountDialogProps) {
  const editing = Boolean(account);
  const [open, setOpen] = useState(false);
  const [values, setValues] = useState<UpstreamFormValues>(() => initialValues(platform, account));
  const [errors, setErrors] = useState<UpstreamFormErrors>({});
  const [attempts, setAttempts] = useState(0);
  const fieldPrefix = useId();

  const mutation = useMutation({
    mutationFn: (form: UpstreamFormValues) => setUpstreamAccount(buildUpstreamParams(form)),
    onSuccess: (run) => {
      setOpen(false);
      setErrors({});
      setAttempts(0);
      // 新登记后清空，好接着登下一条；修改后保留现值，因为对话框还绑在这一行上
      if (!editing) setValues(initialValues(platform, account));
      onDone(run.runId);
    },
  });

  const set = (field: UpstreamFormField) => (value: string) => {
    setValues((prev) => ({ ...prev, [field]: value }));
    // 边改边清掉这一项的报错：让人看着自己把错误改没了，比提交后再刷一遍好
    setErrors((prev) => {
      if (!(field in prev)) return prev;
      const next = { ...prev };
      delete next[field];
      return next;
    });
  };

  // 提交失败时把焦点交给错误摘要（理由见 RegisterServiceDialog）
  const summaryRef = useRef<HTMLDivElement>(null);
  const failedFields = (Object.keys(errors) as UpstreamFormField[]).filter((f) => errors[f]);
  const showSummary = attempts > 0 && (failedFields.length > 0 || Boolean(mutation.error));
  useEffect(() => {
    if (showSummary) summaryRef.current?.focus();
  }, [attempts, mutation.error, showSummary]);

  const submit = () => {
    setAttempts((n) => n + 1);
    const found = validateUpstreamForm(values);
    setErrors(found);
    if (hasUpstreamErrors(found)) return;
    mutation.mutate(values);
  };

  const text = (field: UpstreamFormField, required: boolean, hint: string) => {
    const id = `${fieldPrefix}-${field}`;
    return (
      <FormField
        label={upstreamFieldLabel(field)}
        htmlFor={id}
        required={required}
        {...(errors[field] ? { error: errors[field] } : {})}
        hint={hint}
      >
        <Input
          value={values[field]}
          invalid={Boolean(errors[field])}
          onChange={(e) => set(field)(e.target.value)}
        />
      </FormField>
    );
  };

  const ratioNeed = ratioRequirement(values.access_method);

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (next) {
          // 每次打开都从当前这一行重新取值：上一次改了一半就关掉的内容
          // 留在框里，下次打开会被当成「库里就是这样」
          setValues(initialValues(platform, account));
        } else {
          setErrors({});
          setAttempts(0);
          mutation.reset();
        }
      }}
      trigger={
        editing ? (
          <Button size="sm" variant="secondary">
            修改
          </Button>
        ) : (
          <Button size="sm">登记上游账号</Button>
        )
      }
      title={editing ? "修改上游账号" : "登记上游账号"}
      description={
        editing
          ? "通过 finance.upstream_account.set@1 整行更新。表单里看到什么就写进去什么——留空的字段会被清空。"
          : "通过 finance.upstream_account.set@1 登记一个上游账号。凭据只填引用，平台永不持有明文。"
      }
    >
      <form
        className="flex max-h-[60vh] flex-col gap-3 overflow-y-auto"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        {editing ? (
          <p className="rounded-md border border-edge bg-surface-muted px-2 py-1.5 text-xs text-fg-muted">
            系统类型与接入方式不可修改：它们是成本口径的分叉点，改了会让同一个账号的历史成本
            前后用两套算法算出来。要换口径请新登记一条并停用旧的。
          </p>
        ) : null}

        <FormField
          label={upstreamFieldLabel("system_type")}
          required
          {...(errors.system_type ? { error: errors.system_type } : {})}
          hint="决定用哪个连接器取成本数"
        >
          {editing ? (
            <Input value={values.system_type} readOnly aria-label="系统类型（不可修改）" />
          ) : (
            <Select
              options={SYSTEM_TYPE_OPTIONS}
              value={values.system_type}
              onValueChange={set("system_type")}
              invalid={Boolean(errors.system_type)}
              aria-label="系统类型"
            />
          )}
        </FormField>

        <FormField
          label={upstreamFieldLabel("access_method")}
          required
          {...(errors.access_method ? { error: errors.access_method } : {})}
          hint="决定成本怎么算：计量型按实扣 ÷ 倍率，订阅型按批次摊销"
        >
          {editing ? (
            <Input value={values.access_method} readOnly aria-label="接入方式（不可修改）" />
          ) : (
            <Select
              options={ACCESS_METHOD_OPTIONS}
              value={values.access_method}
              onValueChange={set("access_method")}
              invalid={Boolean(errors.access_method)}
              aria-label="接入方式"
            />
          )}
        </FormField>

        {text("upstream_name", false, "平台手工登记的显示名称；留空会明确显示未接入")}
        {text("upstream_contact", false, "业务联系人或沟通渠道；不要填写密码、Token 或其他凭据")}
        {text("upstream_group", false, "这个账号当前接入的上游分组；留空 = 未接入")}
        {text(
          "credential_ref",
          true,
          "形如 secret://sub2api/prod-key。这里填的是引用，不是密钥本身",
        )}
        {text("base_url", false, "必须 https，且不能带 user:pass@ 段。订阅型可以留空")}

        <FormField
          label={upstreamFieldLabel("recharge_ratio")}
          htmlFor={`${fieldPrefix}-recharge_ratio`}
          required={ratioNeed === "required"}
          {...(errors.recharge_ratio ? { error: errors.recharge_ratio } : {})}
          hint={ratioHint(ratioNeed)}
        >
          <Input
            value={values.recharge_ratio}
            invalid={Boolean(errors.recharge_ratio)}
            disabled={ratioNeed === "forbidden"}
            onChange={(e) => set("recharge_ratio")(e.target.value)}
            // inputMode 而不是 type="number"：数字输入框会按浏览器区域设置
            // 解释小数点，并允许 1e3 这样的写法，而后端要的是定点十进制字符串
            inputMode="decimal"
          />
        </FormField>

        <FormField
          label={upstreamFieldLabel("group_rate")}
          htmlFor={`${fieldPrefix}-group_rate`}
          {...(errors.group_rate ? { error: errors.group_rate } : {})}
          hint="定点十进制，最多 9 位小数，必须为正；仅作分组展示，绝不参与成本计算"
        >
          <Input
            value={values.group_rate}
            invalid={Boolean(errors.group_rate)}
            onChange={(e) => set("group_rate")(e.target.value)}
            inputMode="decimal"
          />
        </FormField>

        <FormField
          label={upstreamFieldLabel("platform_id")}
          htmlFor={`${fieldPrefix}-platform_id`}
          {...(errors.platform_id ? { error: errors.platform_id } : {})}
          hint="哪个自营平台在用这个上游账号。留空 = 未配对，它的成本归不到任何平台"
        >
          <Input
            value={values.platform_id}
            invalid={Boolean(errors.platform_id)}
            onChange={(e) => set("platform_id")(e.target.value)}
          />
        </FormField>

        <FormField
          label={upstreamFieldLabel("status")}
          {...(errors.status ? { error: errors.status } : {})}
          hint="停用只是不再采集，已入账的历史成本不受影响"
        >
          <Select
            options={UPSTREAM_STATUS_OPTIONS}
            value={values.status}
            onValueChange={set("status")}
            invalid={Boolean(errors.status)}
            aria-label="状态"
          />
        </FormField>

        <details className="rounded-md border border-edge p-2">
          <summary className="cursor-pointer text-xs text-fg-muted">币种与日切时区</summary>
          <div className="mt-2 flex flex-col gap-3">
            {text("currency", false, "三位大写 ISO 4217 码。留空按 USD")}
            {text(
              "business_day_tz",
              false,
              "固定偏移如 +08:00，不接受 IANA 时区名。留空按 +08:00",
            )}
          </div>
        </details>

        {/* 焦点落点。tabIndex=-1：只用于程序移焦，不进 Tab 顺序 */}
        <div
          ref={summaryRef}
          tabIndex={-1}
          className="flex flex-col gap-1 outline-none focus-visible:ring-2 focus-visible:ring-accent"
        >
          {showSummary && failedFields.length > 0 ? (
            <p role="alert" className="text-xs text-danger">
              还有 {failedFields.length} 处需要修正:
              {failedFields.map((f) => upstreamFieldLabel(f)).join("、")}
            </p>
          ) : null}
          {/* 错误信息原样透传：后端已保证不回显敏感值（凭据只进 Ref）,
              自己再包一层「操作失败」只会把「倍率必须为正」这类可操作的话盖掉 */}
          <ActionErrorNote error={mutation.error} permission={UPSTREAM_ACCOUNT_MANAGE_PERMISSION} />
        </div>

        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" size="sm" onClick={() => setOpen(false)}>
            取消
          </Button>
          <Button type="submit" size="sm" loading={mutation.isPending}>
            {editing ? "保存" : "登记"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

function ratioHint(need: ReturnType<typeof ratioRequirement>): string {
  switch (need) {
    case "required":
      return "定点十进制，最多 9 位小数，必须为正。倍率是除数：成本 = 上游实扣 ÷ 倍率";
    case "forbidden":
      return "订阅型渠道不配倍率，成本走批次摊销";
    default:
      return "官方 API 的成本口径 v1 待定，倍率可填可不填";
  }
}

/** 从现有行取初值；新登记时预填当前平台与两个缺省值。
 *
 *  修改模式**必须**预填全部字段：这个 Action 是整行替换,
 *  漏预填一个字段等于在保存时把它清空（见 buildUpstreamParams 的说明）。 */
function initialValues(platform: string, account?: UpstreamAccountItem): UpstreamFormValues {
  if (!account) return { ...EMPTY_UPSTREAM_FORM, platform_id: platform };
  return {
    upstream_account_id: account.id,
    system_type: account.system_type,
    access_method: account.access_method,
    upstream_name: account.upstream_name,
    upstream_contact: account.upstream_contact,
    upstream_group: account.upstream_group,
    base_url: account.base_url,
    credential_ref: account.credential_ref,
    recharge_ratio: account.recharge_ratio,
    group_rate: account.group_rate,
    currency: account.currency,
    business_day_tz: account.business_day_tz,
    platform_id: account.platform_id,
    status: account.status,
  };
}
