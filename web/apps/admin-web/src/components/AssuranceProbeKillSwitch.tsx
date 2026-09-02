import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Badge, Button, Dialog, FormField, Input } from "@xingmang/ui-primitives";
import { useState } from "react";
import { currentUserHasRole } from "../auth/session";
import {
  PROBE_KILL_SWITCH_PERMISSION,
  PROBE_KILL_SWITCH_ROLE,
  setProbeKillSwitch,
} from "../api/assuranceProbes";
import type { ApiClient } from "../api/client";
import type { ActionRun } from "../api/platform";
import { ActionErrorNote } from "./ActionErrorNote";

export interface AssuranceProbeKillSwitchProps {
  platform: string;
  /** 当前状态用于对话框里的说明文案，优先来自检测任务列表任意一行的
   *  `kill_switch_state`（该字段按平台统一，见 XM-ASSURE1-core 设计稿
   *  §5.1）。列表为空（该平台尚无任何声明）时传 null，退回 `fallbackConfig`
   *  派生（见下）。 */
  currentState: string | null;
  /** 该平台零声明时的兜底数据源：`GET /api/v1/connectors/config` 现在会
   *  投影 `probe_enabled`/`probe_credential_registered`（XM-ASSURE1-glue,
   *  补的是后端 `internal/platform/httpapi/credentials.go` 的
   *  `connectorConfigItem`——之前这两个新列没有出现在 JSON 响应里，见
   *  XM-ASSURE1-ui 交接文档 risks #2）。
   *
   *  **这不是 `kill_switch_state` 的完整替身**：后端 `killSwitchState()`
   *  还会再叠加一个进程级全局开关（`XM_ASSURE_PROBE_ENABLED`，两个进程
   *  各自解析，不落库），这个全局位前端今天没有任何端点能读到，
   *  因此这里派生的状态只反映"这个平台自己的 Kill Switch 是否打开",
   *  在全局开关恰好被关闭那种少见的运维场景下可能比服务端实际允许探测的
   *  判定更乐观——本片认为这好于"零声明时永远显示未知"，但不是同一件事，
   *  留给验收线确认这个取舍。找不到该平台的配置行（从未配置过连接器，
   *  或读取本身因权限不足被拒）时传 `null`，继续展示"未知"。 */
  fallbackConfig?: { mode: string; probeEnabled: boolean; probeCredentialRegistered: boolean } | null;
  client?: ApiClient;
}

/** 由零声明兜底数据源派生 kill_switch_state 的同名三态——公式与后端
 *  `internal/platform/assurance/service.go` 的 `killSwitchState()` 一致，
 *  但没有全局开关那个因子（见上面 `fallbackConfig` 的文档注释）。 */
function killSwitchStateFromConfig(cfg: { mode: string; probeEnabled: boolean } | null | undefined): string | null {
  if (!cfg) return null;
  if (cfg.mode !== "real") return "not_applicable_fake";
  return cfg.probeEnabled ? "enabled" : "disabled";
}

function killSwitchStateLabel(state: string | null): { label: string; tone: "success" | "warning" | "neutral" } {
  if (state === "enabled") return { label: "已启用", tone: "success" };
  if (state === "disabled") return { label: "未启用", tone: "warning" };
  if (state === "not_applicable_fake") return { label: "fake 模式（不适用）", tone: "neutral" };
  return { label: "未知（读不到当前状态）", tone: "neutral" };
}

/** 检测任务 Kill Switch 入口（`assurance.probe.kill_switch.set@1`）。
 *
 *  只对持有 `assurance-probe-admin` 角色的人显示——这是团队交接消息明确
 *  要求"隐藏"的唯一一处控件（其余写操作按钮全部常驻，403 时才提示缺权限,
 *  这是本仓一贯的"前端隐藏不构成安全控制"纪律）。角色只在本地登录模式下
 *  能被前端读到（见 auth/session.ts `currentUserHasRole` 的说明）；
 *  oidc/dev-header 模式下这个便利入口暂时对所有人隐藏，即使账号确有权限,
 *  这是已知的、待跟进的局限，不是访问被拒——本片选择默认隐藏而不是默认
 *  显示，以贴合"隐藏为默认"的字面要求。 */
export function AssuranceProbeKillSwitch({ platform, currentState, fallbackConfig, client }: AssuranceProbeKillSwitchProps) {
  if (!currentUserHasRole(PROBE_KILL_SWITCH_ROLE)) return null;
  return <KillSwitchDialog platform={platform} currentState={currentState} fallbackConfig={fallbackConfig} client={client} />;
}

function KillSwitchDialog({ platform, currentState, fallbackConfig, client }: AssuranceProbeKillSwitchProps) {
  // 检测任务表有数据就用它的 kill_switch_state（真正的、含全局开关的判定）；
  // 该平台零声明时才退回 GET /api/v1/connectors/config 派生的近似值——见
  // fallbackConfig 的文档注释。
  const effectiveState = currentState ?? killSwitchStateFromConfig(fallbackConfig ?? null);
  const [open, setOpen] = useState(false);
  const [nextEnabled, setNextEnabled] = useState(effectiveState !== "enabled");
  const [credentialRef, setCredentialRef] = useState("");
  const queryClient = useQueryClient();
  const [result, setResult] = useState<ActionRun | null>(null);

  const mutation = useMutation({
    mutationFn: () =>
      setProbeKillSwitch(
        {
          platform,
          probeEnabled: nextEnabled,
          ...(nextEnabled && credentialRef.trim() ? { probeCredentialRef: credentialRef.trim() } : {}),
        },
        {},
        client,
      ),
    onSuccess: (run) => {
      setResult(run);
      void queryClient.invalidateQueries({ queryKey: ["assurance", "probes", platform] });
    },
  });

  const display = killSwitchStateLabel(effectiveState);

  return (
    <Dialog
      trigger={
        <Button type="button" size="sm" variant="secondary" aria-label={`${platform} 检测 Kill Switch`}>
          Kill Switch
        </Button>
      }
      title={`检测 Kill Switch（${platform}）`}
      description="平台级开关：关闭后该平台的检测任务一律被拒绝执行（fake 模式不受影响，本就不检查这个开关）。"
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (next) {
          mutation.reset();
          setResult(null);
          setNextEnabled(effectiveState !== "enabled");
          setCredentialRef("");
        }
      }}
    >
      <div className="flex flex-col gap-3">
        <p className="text-xs text-fg-muted">
          当前状态：<Badge tone={display.tone}>{display.label}</Badge>
        </p>

        {result ? (
          <p role="status" className="rounded-md border border-success bg-success/10 px-3 py-2 text-xs text-success">
            已提交，run_id {result.runId || "（响应未带 run_id）"}
          </p>
        ) : (
          <>
            <div role="group" aria-label="目标状态" className="flex gap-2">
              <Button
                type="button"
                size="sm"
                variant={nextEnabled ? "primary" : "secondary"}
                aria-pressed={nextEnabled}
                onClick={() => setNextEnabled(true)}
                disabled={mutation.isPending}
              >
                启用
              </Button>
              <Button
                type="button"
                size="sm"
                variant={!nextEnabled ? "primary" : "secondary"}
                aria-pressed={!nextEnabled}
                onClick={() => setNextEnabled(false)}
                disabled={mutation.isPending}
              >
                关闭
              </Button>
            </div>

            {nextEnabled ? (
              <FormField
                label="探测凭据引用"
                htmlFor="probe-credential-ref"
                hint="首次启用必填；省略表示保留数据库里已登记的旧值。形如 secret://<scope>/<name>，只填引用名。"
              >
                <Input
                  id="probe-credential-ref"
                  value={credentialRef}
                  disabled={mutation.isPending}
                  onChange={(event) => setCredentialRef(event.target.value)}
                  autoComplete="off"
                  spellCheck={false}
                />
              </FormField>
            ) : null}

            <ActionErrorNote error={mutation.error} permission={PROBE_KILL_SWITCH_PERMISSION} />

            <div className="flex justify-end gap-2">
              <Button
                type="button"
                size="sm"
                variant="secondary"
                onClick={() => setOpen(false)}
                disabled={mutation.isPending}
              >
                取消
              </Button>
              <Button
                type="button"
                size="sm"
                variant={nextEnabled ? "primary" : "danger"}
                loading={mutation.isPending}
                onClick={() => mutation.mutate()}
              >
                确认{nextEnabled ? "启用" : "关闭"}
              </Button>
            </div>
          </>
        )}
      </div>
    </Dialog>
  );
}
