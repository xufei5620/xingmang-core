import { useMutation } from "@tanstack/react-query";
import { Button, Dialog, FormField, Input } from "@xingmang/ui-primitives";
import { useEffect, useId, useRef, useState } from "react";
import {
  RECHARGE_RATIO_MANAGE_PERMISSION,
  setRechargeRatio,
  type UpstreamAccountItem,
} from "../api/finance";
import { validateRatioText } from "../lib/upstreamForm";
import { ActionErrorNote } from "./ActionErrorNote";

export interface RechargeRatioDialogProps {
  account: UpstreamAccountItem;
  onDone: (runId: string) => void;
}

/** 单独修改充值倍率(`finance.recharge_ratio.set@1`,L1 直执行)。
 *
 *  与「修改上游账号」分开是设计稿 §6.3 的要求：倍率是**唯一**会改变成本口径的
 *  字段，改完不会报错，只会让台账从那一刻起静静地错着。给它独立的 Action ID
 *  与独立的权限，「这个月倍率被谁改过几次」才是一次审计检索就能答的问题。
 *
 *  reason 因此必填——没有理由的改动在事后复盘时与手滑不可区分。 */
export function RechargeRatioDialog({ account, onDone }: RechargeRatioDialogProps) {
  const [open, setOpen] = useState(false);
  const [ratio, setRatio] = useState(account.recharge_ratio);
  const [reason, setReason] = useState("");
  const [errors, setErrors] = useState<{ ratio?: string; reason?: string }>({});
  const [attempts, setAttempts] = useState(0);
  const fieldPrefix = useId();

  const mutation = useMutation({
    mutationFn: (input: { ratio: string; reason: string }) =>
      setRechargeRatio({
        upstream_account_id: account.id,
        recharge_ratio: input.ratio.trim(),
        reason: input.reason.trim(),
      }),
    onSuccess: (run) => {
      setOpen(false);
      setErrors({});
      setAttempts(0);
      setReason("");
      onDone(run.runId);
    },
  });

  const summaryRef = useRef<HTMLDivElement>(null);
  const showSummary = attempts > 0 && Boolean(mutation.error);
  useEffect(() => {
    if (showSummary) summaryRef.current?.focus();
  }, [attempts, mutation.error, showSummary]);

  const submit = () => {
    setAttempts((n) => n + 1);
    const found: { ratio?: string; reason?: string } = {};
    const ratioProblem = validateRatioText(ratio);
    if (ratioProblem) found.ratio = ratioProblem;
    if (!reason.trim()) {
      found.reason = "必须写明为什么改：倍率一改，台账里所有后续成本都跟着变";
    }
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    mutation.mutate({ ratio, reason });
  };

  // 订阅型渠道没有倍率（§2.0），后端 SetRechargeRatio 会直接拒。
  // 做成一句话而不是禁用按钮：禁用的按钮拿不到键盘焦点，读屏用户听不到理由
  if (account.access_method === "subscription_account") {
    return (
      <span className="text-xs text-fg-muted">
        订阅型不适用
        <span className="block">成本走批次摊销</span>
      </span>
    );
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (next) {
          // 每次打开都取库里的现值：上次改了一半没提交的数字留在框里,
          // 下次打开会被当成「现在的倍率就是这个」
          setRatio(account.recharge_ratio);
          setReason("");
        } else {
          setErrors({});
          setAttempts(0);
          mutation.reset();
        }
      }}
      trigger={
        <Button size="sm" variant="secondary">
          改倍率
        </Button>
      }
      title="修改充值倍率"
      description="通过 finance.recharge_ratio.set@1 单独修改，改动带理由进审计链。"
    >
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <dl className="grid grid-cols-2 gap-2 rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs">
          <div>
            <dt className="text-fg-muted">上游网址</dt>
            <dd className="font-mono break-all text-fg">{account.base_url || "—"}</dd>
          </div>
          <div>
            <dt className="text-fg-muted">当前倍率 / 成本率</dt>
            <dd className="tabular-nums text-fg">
              {account.recharge_ratio || "未配置"}
              {account.recharge_cost_rate ? ` / ${account.recharge_cost_rate} 每额度` : null}
            </dd>
          </div>
        </dl>

        <FormField
          label="新倍率"
          htmlFor={`${fieldPrefix}-ratio`}
          required
          {...(errors.ratio ? { error: errors.ratio } : {})}
          hint="倍率是除数：成本 = 上游实扣 ÷ 倍率。倍率越大，同样的实扣折算出的成本越低"
        >
          <Input
            value={ratio}
            invalid={Boolean(errors.ratio)}
            inputMode="decimal"
            onChange={(e) => {
              setRatio(e.target.value);
              setErrors((prev) => {
                if (!("ratio" in prev)) return prev;
                const next = { ...prev };
                delete next.ratio;
                return next;
              });
            }}
          />
        </FormField>

        <FormField
          label="修改理由"
          htmlFor={`${fieldPrefix}-reason`}
          required
          {...(errors.reason ? { error: errors.reason } : {})}
          hint="会进审计链。写「上游 8 月 26 日调价」这类能对上事实的话，别写「调整」"
        >
          <Input
            value={reason}
            invalid={Boolean(errors.reason)}
            onChange={(e) => {
              setReason(e.target.value);
              setErrors((prev) => {
                if (!("reason" in prev)) return prev;
                const next = { ...prev };
                delete next.reason;
                return next;
              });
            }}
          />
        </FormField>

        <p className="text-xs text-fg-muted">
          倍率只影响改动之后的成本折算。已入账的历史行按当时的倍率快照冻结，不会被追溯改写（§6.3）。
        </p>

        <div
          ref={summaryRef}
          tabIndex={-1}
          className="outline-none focus-visible:ring-2 focus-visible:ring-accent"
        >
          <ActionErrorNote error={mutation.error} permission={RECHARGE_RATIO_MANAGE_PERMISSION} />
        </div>

        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" size="sm" onClick={() => setOpen(false)}>
            取消
          </Button>
          <Button type="submit" size="sm" loading={mutation.isPending}>
            保存倍率
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
