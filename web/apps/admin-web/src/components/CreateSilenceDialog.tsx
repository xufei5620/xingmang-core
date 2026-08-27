import { useMutation } from "@tanstack/react-query";
import { Button, Dialog, FormField, Input, Select } from "@xingmang/ui-primitives";
import { useId, useState } from "react";
import { ALERT_RULES, createSilence, SILENCE_PERMISSION } from "../api/alerts";
import { SILENCE_DURATION_OPTIONS } from "../lib/alerts";
import { ActionErrorNote } from "./ActionErrorNote";

/** 全局静默在下拉里的取值。
 *
 *  用一个专门的哨兵值而不是空串：Radix Select 把空串当成「未选择」，
 *  于是「全局静默」会显示成 placeholder，人以为自己还没选。
 *  提交前再换回后端要的空串。 */
const GLOBAL_RULE_VALUE = "__global__";

const DEFAULT_DURATION = "60";

interface FormValues {
  ruleKey: string;
  durationMinutes: string;
  reason: string;
}

const EMPTY_FORM: FormValues = {
  ruleKey: GLOBAL_RULE_VALUE,
  durationMinutes: DEFAULT_DURATION,
  reason: "",
};

export interface CreateSilenceDialogProps {
  /** 预选的规则键。从某一行「静默这类告警」进来时带上它。 */
  defaultRuleKey?: string;
  onCreated: (runId: string) => void;
}

/** 「创建静默窗口」对话框（alerts.silence.create@1，L1）。
 *
 *  静默是**主动让告警闭嘴**，所以这个表单只有一条硬规则：理由必填。
 *  没有理由的静默在事后复盘时与「有人手滑」不可区分——库层 CHECK、
 *  Action Handler、这里的表单三处都拦它，因为这是最值得多拦两道的东西。 */
export function CreateSilenceDialog({ defaultRuleKey, onCreated }: CreateSilenceDialogProps) {
  const [open, setOpen] = useState(false);
  const [values, setValues] = useState<FormValues>(EMPTY_FORM);
  const [reasonError, setReasonError] = useState<string | null>(null);
  const reasonId = useId();

  const mutation = useMutation({
    mutationFn: (form: FormValues) =>
      createSilence({
        rule_key: form.ruleKey === GLOBAL_RULE_VALUE ? "" : form.ruleKey,
        duration_minutes: Number(form.durationMinutes),
        reason: form.reason.trim(),
      }),
    onSuccess: (run) => {
      setOpen(false);
      onCreated(run.runId);
    },
  });

  const submit = () => {
    const reason = values.reason.trim();
    if (reason === "") {
      setReasonError("请写清楚为什么静默——事后复盘要靠它区分「有意」和「手滑」");
      return;
    }
    setReasonError(null);
    mutation.mutate(values);
  };

  const ruleOptions = [
    { value: GLOBAL_RULE_VALUE, label: "全部规则（全局静默）" },
    ...ALERT_RULES.map((r) => ({ value: r.key, label: `${r.label}（${r.key}）` })),
  ];

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (next) {
          setValues({
            ...EMPTY_FORM,
            ...(defaultRuleKey ? { ruleKey: defaultRuleKey } : {}),
          });
          setReasonError(null);
          mutation.reset();
        }
      }}
      trigger={
        <Button variant="secondary" size="sm">
          创建静默窗口
        </Button>
      }
      title="创建静默窗口"
      description="通过 alerts.silence.create@1 建一个窗口。窗口内命中的告警进「已静默」且不投递；窗口过期后条件仍成立会转回「未处理」并重新投递。"
    >
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <FormField label="静默范围" required hint="选「全部规则」等于临时关掉这个环境的全部告警投递">
          <Select
            aria-label="静默范围"
            options={ruleOptions}
            value={values.ruleKey}
            onValueChange={(ruleKey) => setValues((prev) => ({ ...prev, ruleKey }))}
          />
        </FormField>

        <FormField label="静默时长" required hint="上限 7 天：更长的诉求应当去改规则或停用采集，那两条路都有变更记录">
          <Select
            aria-label="静默时长"
            options={SILENCE_DURATION_OPTIONS}
            value={values.durationMinutes}
            onValueChange={(durationMinutes) =>
              setValues((prev) => ({ ...prev, durationMinutes }))
            }
          />
        </FormField>

        <FormField
          label="理由"
          htmlFor={reasonId}
          required
          {...(reasonError ? { error: reasonError } : {})}
          hint="会进审计链，事后复盘第一个要看的就是它"
        >
          <Input
            value={values.reason}
            invalid={Boolean(reasonError)}
            placeholder="例：上游 Sub2API 维护窗口，已与对方确认"
            onChange={(e) => setValues((prev) => ({ ...prev, reason: e.target.value }))}
          />
        </FormField>

        <ActionErrorNote error={mutation.error} permission={SILENCE_PERMISSION} />

        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" size="sm" onClick={() => setOpen(false)}>
            取消
          </Button>
          <Button type="submit" size="sm" loading={mutation.isPending}>
            创建
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
