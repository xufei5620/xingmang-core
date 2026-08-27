import { useMutation } from "@tanstack/react-query";
import { Button, Dialog, FormField, Input, Select } from "@xingmang/ui-primitives";
import { useId, useState } from "react";
import { observeService, type ServiceItem } from "../api/platform";
import {
  defaultWatermark,
  SERVICE_STATUS_OPTIONS,
  validateObserveForm,
  type ObserveFormValues,
} from "../lib/serviceForm";
import { ActionErrorNote } from "./ActionErrorNote";

/** registry.service.observe 声明的 Permission（registry/actions.go）。 */
const PERMISSION = "registry.service.manage";

export interface ObserveServiceDialogProps {
  service: ServiceItem;
  onObserved: (runId: string) => void;
}

/** 「上报观测」对话框（registry.service.observe@1）。
 *
 *  这是给人看的「数据活了」那一下：上报之后服务的新鲜度会从「未初始化」
 *  变成一个真实时间戳，页面上的徽章跟着变色。手填水位只是 Foundation-A 的
 *  过渡手段——真正的水位应当由采集任务回写（jobs/sub2api_sync.go）。 */
export function ObserveServiceDialog({ service, onObserved }: ObserveServiceDialogProps) {
  const [open, setOpen] = useState(false);
  const [values, setValues] = useState<ObserveFormValues>(() => ({
    watermark: defaultWatermark(),
    status: service.status,
  }));
  const [errors, setErrors] = useState<Partial<Record<keyof ObserveFormValues, string>>>({});
  const watermarkId = useId();

  const mutation = useMutation({
    mutationFn: (form: ObserveFormValues) =>
      observeService({
        service_type: service.service_type,
        instance_id: service.instance_id,
        watermark: form.watermark.trim(),
        status: form.status,
      }),
    onSuccess: (run) => {
      setOpen(false);
      onObserved(run.runId);
    },
  });

  const submit = () => {
    const found = validateObserveForm(values);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    mutation.mutate(values);
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (next) {
          // 每次打开都重新取「现在」：复用上次打开时算的水位，等于给这次上报
          // 盖了一个过去的时间戳
          setValues({ watermark: defaultWatermark(), status: service.status });
          setErrors({});
          mutation.reset();
        }
      }}
      trigger={
        <Button variant="ghost" size="sm">
          上报观测
        </Button>
      }
      title={`上报观测 · ${service.instance_id}`}
      description="通过 registry.service.observe@1 回写数据水位与服务状态。"
    >
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <FormField
          label="数据水位"
          htmlFor={watermarkId}
          required
          {...(errors.watermark ? { error: errors.watermark } : {})}
          hint="默认取当前 UTC 时刻；真实水位应由采集任务回写"
        >
          <Input
            value={values.watermark}
            invalid={Boolean(errors.watermark)}
            onChange={(e) => setValues((prev) => ({ ...prev, watermark: e.target.value }))}
          />
        </FormField>

        <FormField
          label="服务状态"
          required
          {...(errors.status ? { error: errors.status } : {})}
        >
          <Select
            aria-label="服务状态"
            options={SERVICE_STATUS_OPTIONS}
            value={values.status}
            invalid={Boolean(errors.status)}
            onValueChange={(status) => setValues((prev) => ({ ...prev, status }))}
          />
        </FormField>

        <ActionErrorNote error={mutation.error} permission={PERMISSION} />

        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" size="sm" onClick={() => setOpen(false)}>
            取消
          </Button>
          <Button type="submit" size="sm" loading={mutation.isPending}>
            上报
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
