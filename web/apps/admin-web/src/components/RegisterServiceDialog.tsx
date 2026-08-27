import { useMutation } from "@tanstack/react-query";
import { Button, Dialog, FormField, Input } from "@xingmang/ui-primitives";
import { useEffect, useId, useRef, useState } from "react";
import { createService } from "../api/platform";
import {
  buildServiceCreateParams,
  EMPTY_SERVICE_FORM,
  hasErrors,
  serviceFieldLabel,
  validateServiceForm,
  type ServiceFormErrors,
  type ServiceFormField,
  type ServiceFormValues,
} from "../lib/serviceForm";
import { ActionErrorNote } from "./ActionErrorNote";

/** registry.service.create 声明的 Permission（registry/actions.go）。 */
const PERMISSION = "registry.service.manage";

export interface RegisterServiceDialogProps {
  /** 当前身份所属环境；空串表示前端不知道（见 ServicesPage 的解析顺序）。 */
  environment: string;
  /** 登记成功后回调，参数是 action_run_id。 */
  onRegistered: (runId: string) => void;
}

/** 「登记服务」对话框——看板上的第一个写路径。
 *
 *  走的是 Action 执行入口而不是某个 REST 写接口：所有写操作唯一入口是
 *  Action（ADR-003），所以每次提交都会拿到一个 run_id。
 *
 *  注意 run_id **不等于**「审计里一定有这条事件」：业务写、ActionRun、审计追加
 *  三段目前不是原子的，审计还是 fail-open（#38/#40）。回执文案照这个事实写。 */
export function RegisterServiceDialog({ environment, onRegistered }: RegisterServiceDialogProps) {
  const [open, setOpen] = useState(false);
  const [values, setValues] = useState<ServiceFormValues>(EMPTY_SERVICE_FORM);
  const [errors, setErrors] = useState<ServiceFormErrors>({});
  const fieldPrefix = useId();

  const mutation = useMutation({
    mutationFn: (form: ServiceFormValues) =>
      createService(buildServiceCreateParams(form, environment)),
    onSuccess: (run) => {
      setOpen(false);
      setValues(EMPTY_SERVICE_FORM);
      setErrors({});
      onRegistered(run.runId);
    },
  });

  const set = (field: ServiceFormField) => (value: string) => {
    setValues((prev) => ({ ...prev, [field]: value }));
    // 边改边清掉这一项的报错：让人看着自己把错误改没了，比提交后再刷一遍好
    setErrors((prev) => {
      if (!(field in prev)) return prev;
      const next = { ...prev };
      delete next[field];
      return next;
    });
  };

  // 提交失败时把焦点交给错误摘要。
  //
  // 键盘/读屏用户点完「登记」之后，焦点还停在按钮上，而报错出现在表单里、
  // 甚至在弹窗顶部——不主动移过去，他们只会听到一片沉默，以为什么都没发生。
  const summaryRef = useRef<HTMLDivElement>(null);
  const [attempts, setAttempts] = useState(0);
  const failedFields = (Object.keys(errors) as ServiceFormField[]).filter((f) => errors[f]);
  const showSummary = attempts > 0 && (failedFields.length > 0 || Boolean(mutation.error));
  useEffect(() => {
    // attempts 变化 = 又点了一次提交；mutation.error 变化 = 服务端刚回了个失败。
    // 两种情形都要重新把焦点带到摘要上
    if (showSummary) summaryRef.current?.focus();
  }, [attempts, mutation.error, showSummary]);

  const submit = () => {
    setAttempts((n) => n + 1);
    const found = validateServiceForm(values);
    setErrors(found);
    if (hasErrors(found) || !environment) return;
    mutation.mutate(values);
  };

  const field = (name: ServiceFormField, required = false, hint?: string) => {
    const id = `${fieldPrefix}-${name}`;
    return (
      <FormField
        label={serviceFieldLabel(name)}
        htmlFor={id}
        required={required}
        {...(errors[name] ? { error: errors[name] } : {})}
        {...(hint ? { hint } : {})}
      >
        <Input
          value={values[name]}
          invalid={Boolean(errors[name])}
          onChange={(e) => set(name)(e.target.value)}
        />
      </FormField>
    );
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        // 关掉时把上一次的错误一起丢掉：下次打开不该顶着一条陈年 403
        if (!next) {
          setErrors({});
          setAttempts(0);
          mutation.reset();
        }
      }}
      trigger={<Button size="sm">登记服务</Button>}
      title="登记服务"
      description="通过 registry.service.create@1 登记一个被管理系统实例。"
    >
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        {field("service_type", true, "如 sub2api、newapi")}
        {field("instance_id", true, "全局唯一，如 sub2api-prod")}
        {field("endpoint", true, "对外地址，必须 https")}
        {field("owner", true, "负责人或团队")}

        <FormField
          label="环境"
          hint="由当前身份决定，不可更改：后端只允许在自己所属环境执行（规格 §20.5）"
        >
          {/* 做成只读输入框而不是一行文字：它在视觉上属于表单的一部分，
              人要能一眼确认「我这是往哪个环境写」 */}
          <Input value={environment || "未知"} readOnly aria-label="环境" />
        </FormField>
        {environment ? null : (
          <p className="text-xs text-danger" role="alert">
            前端无法确定当前身份的环境（未配置 VITE_XM_ENVIRONMENT，服务清单与指标也都是空的）。
            填错环境只会换来一个 403，所以这里不猜。
          </p>
        )}

        <details className="rounded-md border border-edge p-2">
          <summary className="cursor-pointer text-xs text-fg-muted">可选字段</summary>
          <div className="mt-2 flex flex-col gap-3">
            {field("internal_endpoint", false, "内网地址，http(s) 均可")}
            {field("health_check_path", false, "如 /healthz")}
            {field("native_console_url", false, "原生后台入口")}
            {field("runbook_path", false, "运行手册路径")}
          </div>
        </details>

        {/* 焦点落点。tabIndex=-1：只用于程序移焦，不进 Tab 顺序 */}
        <div
          ref={summaryRef}
          tabIndex={-1}
          className="flex flex-col gap-1 outline-none focus-visible:ring-2 focus-visible:ring-accent"
        >
          {showSummary && failedFields.length > 0 ? (
            // 只列字段名，不复述每条错误文案：详细文案就在字段下方，
            // 重复一遍反而让读屏用户听两遍同样的话
            <p role="alert" className="text-xs text-danger">
              还有 {failedFields.length} 处需要修正：
              {failedFields.map((f) => serviceFieldLabel(f)).join("、")}
            </p>
          ) : null}
          <ActionErrorNote error={mutation.error} permission={PERMISSION} />
        </div>

        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" size="sm" onClick={() => setOpen(false)}>
            取消
          </Button>
          <Button type="submit" size="sm" loading={mutation.isPending} disabled={!environment}>
            登记
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
