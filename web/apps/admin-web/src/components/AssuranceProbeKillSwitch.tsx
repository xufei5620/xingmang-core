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
  /** 当前状态用于对话框里的说明文案，来自检测任务列表任意一行的
   *  `kill_switch_state`（该字段按平台统一，见 XM-ASSURE1-core 设计稿
   *  §5.1）。列表为空（该平台尚无任何声明）时传 null——**Kill Switch
   *  的当前值在这种情况下读不到**：`GET /api/v1/connectors/config` 至今
   *  没有把 `probe_enabled`/`probe_credential_ref` 这两个新列投影进 JSON
   *  响应（后端 `internal/platform/httpapi/credentials.go` 的
   *  `connectorConfigItem`），这是本片交付时发现的一处后端缺口，见交接
   *  文档 follow_ups——本片按"不改 Go"的范围判断，选择诚实展示"当前状态
   *  未知"而不是新开一条后端改动。 */
  currentState: string | null;
  client?: ApiClient;
}

function killSwitchStateLabel(state: string | null): { label: string; tone: "success" | "warning" | "neutral" } {
  if (state === "enabled") return { label: "已启用", tone: "success" };
  if (state === "disabled") return { label: "未启用", tone: "warning" };
  if (state === "not_applicable_fake") return { label: "fake 模式（不适用）", tone: "neutral" };
  return { label: "未知（尚无检测任务，读不到当前状态）", tone: "neutral" };
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
export function AssuranceProbeKillSwitch({ platform, currentState, client }: AssuranceProbeKillSwitchProps) {
  if (!currentUserHasRole(PROBE_KILL_SWITCH_ROLE)) return null;
  return <KillSwitchDialog platform={platform} currentState={currentState} client={client} />;
}

function KillSwitchDialog({ platform, currentState, client }: AssuranceProbeKillSwitchProps) {
  const [open, setOpen] = useState(false);
  const [nextEnabled, setNextEnabled] = useState(currentState !== "enabled");
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

  const display = killSwitchStateLabel(currentState);

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
          setNextEnabled(currentState !== "enabled");
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
