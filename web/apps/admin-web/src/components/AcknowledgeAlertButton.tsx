import { useMutation } from "@tanstack/react-query";
import { Button } from "@xingmang/ui-primitives";
import { ACKNOWLEDGE_PERMISSION, acknowledgeAlert, type AlertItem } from "../api/alerts";
import { canAcknowledge } from "../lib/alerts";
import { ActionErrorNote } from "./ActionErrorNote";

export interface AcknowledgeAlertButtonProps {
  alert: AlertItem;
  onAcknowledged: (runId: string) => void;
}

/** 行内「确认」按钮（alerts.alert.acknowledge@1，L0）。
 *
 *  不套对话框：确认没有参数、没有不可逆后果，多一次点击只是摩擦。
 *  静默才需要对话框——它有必填的理由。
 *
 *  错误就地显示在按钮下方而不是替换整行：一次 403 不该把这条告警从
 *  列表里抹掉，人还要看着它继续处理。 */
export function AcknowledgeAlertButton({ alert, onAcknowledged }: AcknowledgeAlertButtonProps) {
  const mutation = useMutation({
    mutationFn: () => acknowledgeAlert(alert.id),
    onSuccess: (run) => onAcknowledged(run.runId),
  });

  if (!canAcknowledge(alert)) {
    // 只有 OPEN / REOPENED 能确认（与后端 WHERE 子句同一条规则）。
    // 前端隐藏不构成安全控制，这里只是不让人点一个必然失败的按钮。
    return null;
  }

  return (
    <div className="flex flex-col items-end gap-1">
      <Button
        variant="ghost"
        size="sm"
        loading={mutation.isPending}
        onClick={() => mutation.mutate()}
      >
        确认
      </Button>
      <ActionErrorNote error={mutation.error} permission={ACKNOWLEDGE_PERMISSION} />
    </div>
  );
}
