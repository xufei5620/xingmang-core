import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Badge, Button, Dialog, FormField, Input, Select } from "@xingmang/ui-primitives";
import { useEffect, useId, useRef, useState } from "react";
import {
  declareProbe,
  isProbeTemplateKey,
  PROBE_MANAGE_PERMISSION,
  PROBE_MAX_TOKENS_CAP,
  PROBE_MAX_TOKENS_DEFAULT,
  PROBE_TEMPLATES,
  runProbe,
  type ProbeTemplateKey,
  type RunProbeResult,
} from "../api/assuranceProbes";
import type { ApiClient } from "../api/client";
import { listConnectorConfigs } from "../api/connectors";
import { listPlatformChannels } from "../api/platformChannels";
import { ActionErrorNote } from "./ActionErrorNote";

export interface AssuranceProbeDeclareDialogProps {
  platform: string;
  /** 恰好一个已登记 service 时才能读取渠道目录（与本仓一贯的
   *  「多实例/未登记时说明原因，不猜一个 service 出来」同一条纪律）。 */
  serviceId?: string;
  client?: ApiClient;
  onDone: (summary: { declareRunId: string; runResult: RunProbeResult | null }) => void;
}

const TEMPLATE_OPTIONS = PROBE_TEMPLATES.map((t) => ({ value: t.value, label: t.label }));

function isSupportedPlatform(platform: string): platform is "sub2api" | "newapi" {
  return platform === "sub2api" || platform === "newapi";
}

/** "发起检测"：声明一条新的检测任务并立即触发一次批次（
 *  `assurance.probe.declare@1` 紧接 `assurance.probe.run@1`）。
 *
 *  渠道来自渠道目录（真实数据，`GET /api/v1/platforms/{p}/channels`）,
 *  但目录今天只给"这条渠道有几个模型"这个数量（`models.count`），不给具体
 *  模型名称清单（XM-CHAN-FIELDS0 尚未交付，见 api/platformChannels.ts 顶部
 *  注释）——因此"目标模型"仍需手填，不能做成从目录里选的下拉，这是数据
 *  源的真实限制，不是本对话框偷懒；填写的模型名会应用到全部勾选的渠道
 *  （笛卡尔积），与设计稿 §1.2.2 的 targets 结构（每个 {channel_id, model}
 *  独立一条）兼容。 */
export function AssuranceProbeDeclareDialog({
  platform,
  serviceId,
  client,
  onDone,
}: AssuranceProbeDeclareDialogProps) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [templateKey, setTemplateKey] = useState<ProbeTemplateKey>("model_fingerprint");
  const [targetHost, setTargetHost] = useState("");
  const [selectedChannels, setSelectedChannels] = useState<string[]>([]);
  const [model, setModel] = useState("");
  const [maxTokens, setMaxTokens] = useState(String(PROBE_MAX_TOKENS_DEFAULT));
  const [fieldError, setFieldError] = useState("");
  const [attempts, setAttempts] = useState(0);
  const summaryRef = useRef<HTMLDivElement>(null);
  const fieldPrefix = useId();
  const queryClient = useQueryClient();

  const configsQuery = useQuery({
    queryKey: ["connectors", "config"],
    queryFn: ({ signal }) => listConnectorConfigs({ signal }, client),
    enabled: open,
  });
  const currentConfig = (configsQuery.data ?? []).find((c) => c.platform === platform);

  const channelsSupported = isSupportedPlatform(platform) && Boolean(serviceId);
  const channelsQuery = useQuery({
    queryKey: ["platform-channels", platform, serviceId],
    queryFn: ({ signal }) =>
      listPlatformChannels(platform as "sub2api" | "newapi", serviceId as string, { signal }, client),
    enabled: open && channelsSupported,
  });
  const channelRows = channelsQuery.data?.items ?? [];

  useEffect(() => {
    if (open && !targetHost && currentConfig?.target_allowlist.length) {
      setTargetHost(currentConfig.target_allowlist[0] ?? "");
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, currentConfig]);

  const mutation = useMutation({
    mutationFn: async () => {
      const declareRun = await declareProbe(
        {
          platform,
          name: name.trim(),
          promptTemplateKey: templateKey,
          targetHost: targetHost.trim(),
          maxTokens: Number(maxTokens),
          targets: selectedChannels.map((channelId) => {
            const row = channelRows.find((r) => r.channelRef.externalChannelId === channelId);
            return {
              channelId,
              externalChannelId: row?.channelRef.externalChannelId ?? channelId,
              model: model.trim(),
            };
          }),
        },
        {},
        client,
      );
      const declarationId =
        declareRun.result && typeof declareRun.result === "object" && "declaration_id" in declareRun.result
          ? String((declareRun.result as Record<string, unknown>).declaration_id)
          : "";
      let runResult: RunProbeResult | null = null;
      if (declarationId) {
        const run = await runProbe({ declarationId }, {}, client);
        runResult = (run.result as RunProbeResult | undefined) ?? null;
      }
      return { declareRunId: declareRun.runId, runResult };
    },
    onSuccess: (summary) => {
      void queryClient.invalidateQueries({ queryKey: ["assurance", "probes", platform] });
      onDone(summary);
      setOpen(false);
      reset();
    },
  });

  function reset() {
    setName("");
    setTemplateKey("model_fingerprint");
    setTargetHost("");
    setSelectedChannels([]);
    setModel("");
    setMaxTokens(String(PROBE_MAX_TOKENS_DEFAULT));
    setFieldError("");
    setAttempts(0);
    mutation.reset();
  }

  function toggleChannel(channelId: string) {
    setSelectedChannels((prev) =>
      prev.includes(channelId) ? prev.filter((c) => c !== channelId) : [...prev, channelId],
    );
  }

  function validate(): string {
    if (!name.trim()) return "请填写任务名称";
    if (!isProbeTemplateKey(templateKey)) return "请选择检测模板";
    if (!targetHost.trim()) return "请填写探测目标主机";
    if (selectedChannels.length === 0) return "请至少选择一个渠道";
    if (!model.trim()) return "请填写目标模型";
    const n = Number(maxTokens);
    if (!Number.isInteger(n) || n < 1 || n > PROBE_MAX_TOKENS_CAP) {
      return `max_tokens 必须是 1..${PROBE_MAX_TOKENS_CAP} 的整数`;
    }
    return "";
  }

  function submit() {
    setAttempts((n) => n + 1);
    const found = validate();
    setFieldError(found);
    if (found) return;
    mutation.mutate();
  }

  const showSummary = attempts > 0 && (Boolean(fieldError) || Boolean(mutation.error));
  useEffect(() => {
    if (showSummary) summaryRef.current?.focus();
  }, [showSummary, attempts]);

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) reset();
      }}
      trigger={<Button size="sm">发起检测</Button>}
      title="发起检测"
      description="声明一条新的检测任务（assurance.probe.declare@1）并立即触发一次批次（assurance.probe.run@1）。检测结果为形状与延迟检测，非语义正确性保证。"
    >
      <form
        className="flex max-h-[65vh] flex-col gap-3 overflow-y-auto"
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        {showSummary ? (
          <div
            ref={summaryRef}
            role="alert"
            tabIndex={-1}
            className="rounded-md border border-danger/40 bg-danger/5 px-3 py-2 text-xs text-danger outline-none focus-visible:outline-2 focus-visible:outline-danger"
          >
            <span>{fieldError || "请检查提交内容"}</span>
            {mutation.error ? (
              <span className="mt-1 block">
                <ActionErrorNote error={mutation.error} permission={PROBE_MANAGE_PERMISSION} />
              </span>
            ) : null}
          </div>
        ) : null}

        <FormField label="任务名称" htmlFor={`${fieldPrefix}-name`} required>
          <Input
            id={`${fieldPrefix}-name`}
            value={name}
            disabled={mutation.isPending}
            onChange={(event) => setName(event.target.value)}
            placeholder="如：模型指纹"
          />
        </FormField>

        <FormField label="检测模板" required hint="四个固定模板，不接受自由文本 Prompt">
          <Select
            aria-label="检测模板"
            options={TEMPLATE_OPTIONS}
            value={templateKey}
            disabled={mutation.isPending}
            onValueChange={(value) => {
              if (isProbeTemplateKey(value)) setTemplateKey(value);
            }}
          />
        </FormField>

        <FormField
          label="探测目标主机"
          htmlFor={`${fieldPrefix}-host`}
          required
          hint={
            currentConfig
              ? `real 模式下必须落在该平台的允许主机内：${currentConfig.target_allowlist.join(", ") || "（尚未配置）"}`
              : "该平台尚未配置接入模式；fake 模式下不校验这个值"
          }
        >
          <Input
            id={`${fieldPrefix}-host`}
            value={targetHost}
            disabled={mutation.isPending}
            onChange={(event) => setTargetHost(event.target.value)}
            placeholder="api.example.com"
            autoComplete="off"
            spellCheck={false}
          />
        </FormField>

        <fieldset className="flex flex-col gap-1.5">
          <legend className="text-sm font-medium text-fg">渠道（来自渠道目录，可多选）</legend>
          {!channelsSupported ? (
            <p className="text-xs text-fg-muted">
              需要恰好一个已登记的 service 才能读取渠道目录；当前该平台没有唯一已登记的 service，请先在渠道管理页确认。
            </p>
          ) : channelsQuery.isPending ? (
            <p className="text-xs text-fg-muted">正在读取渠道目录…</p>
          ) : channelsQuery.isError ? (
            <p className="text-xs text-danger">渠道目录读取失败，无法选择渠道。</p>
          ) : channelRows.length === 0 ? (
            <p className="text-xs text-fg-muted">该平台渠道目录为空。</p>
          ) : (
            <div role="group" aria-label="选择渠道" className="flex max-h-40 flex-col gap-1 overflow-y-auto">
              {channelRows.map((row) => {
                const id = row.channelRef.externalChannelId;
                return (
                  <label key={id} className="flex items-center gap-2 text-sm text-fg">
                    <input
                      type="checkbox"
                      checked={selectedChannels.includes(id)}
                      disabled={mutation.isPending}
                      onChange={() => toggleChannel(id)}
                      className="size-4 rounded-sm border-edge accent-accent"
                    />
                    <span>{row.name || id}</span>
                    {typeof row.models?.count === "number" ? (
                      <Badge tone="neutral">{row.models.count} 个模型（仅数量）</Badge>
                    ) : null}
                  </label>
                );
              })}
            </div>
          )}
        </fieldset>

        <FormField
          label="目标模型"
          htmlFor={`${fieldPrefix}-model`}
          required
          hint="渠道目录今天只给模型数量、不给具体模型名清单（XM-CHAN-FIELDS0 尚未交付），这里手填的模型名会应用到全部勾选的渠道"
        >
          <Input
            id={`${fieldPrefix}-model`}
            value={model}
            disabled={mutation.isPending}
            onChange={(event) => setModel(event.target.value)}
            placeholder="如：claude-sonnet-4"
            autoComplete="off"
            spellCheck={false}
          />
        </FormField>

        <FormField
          label="max_tokens"
          htmlFor={`${fieldPrefix}-max-tokens`}
          required
          hint={`默认 ${PROBE_MAX_TOKENS_DEFAULT}，硬顶 ${PROBE_MAX_TOKENS_CAP}`}
        >
          <Input
            id={`${fieldPrefix}-max-tokens`}
            type="number"
            min={1}
            max={PROBE_MAX_TOKENS_CAP}
            value={maxTokens}
            disabled={mutation.isPending}
            onChange={(event) => setMaxTokens(event.target.value)}
          />
        </FormField>

        <div className="flex justify-end gap-2">
          <Button type="button" size="sm" variant="secondary" onClick={() => setOpen(false)} disabled={mutation.isPending}>
            取消
          </Button>
          <Button type="submit" size="sm" loading={mutation.isPending}>
            声明并触发检测
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
