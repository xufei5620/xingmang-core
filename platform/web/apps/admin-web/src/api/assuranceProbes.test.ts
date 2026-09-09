import { describe, expect, it, vi } from "vitest";
import type { ApiClient } from "./client";
import {
  cancelProbe,
  declareProbe,
  getPlatformAssuranceProbeHistory,
  getPlatformAssuranceProbes,
  isProbeTemplateKey,
  PROBE_DEFAULT_EXPECTED_SHAPE,
  PROBE_KILL_SWITCH_PERMISSION,
  PROBE_KILL_SWITCH_ROLE,
  PROBE_MANAGE_PERMISSION,
  PROBE_MAX_TOKENS_CAP,
  PROBE_MAX_TOKENS_DEFAULT,
  PROBE_RUN_PERMISSION,
  probeTemplateLabel,
  runProbe,
  setProbeKillSwitch,
} from "./assuranceProbes";

function client(getBody: unknown = {}): ApiClient {
  return {
    get: vi.fn().mockResolvedValue(getBody),
    post: vi.fn().mockResolvedValue({ action_run_id: "run-1", result: {} }),
  };
}

const freshness = {
  state: "fresh",
  staleness_seconds: 0,
  threshold_seconds: 60,
  is_partial: false,
  observed_at: "2026-08-31T10:00:00Z",
  last_success: "2026-08-31T10:00:00Z",
  last_error_code: "",
};

describe("assuranceProbes API contract", () => {
  it("解析检测任务表：字段逐个 camelCase 映射，缺省数组回落成空数组", async () => {
    const c = client({
      platform: "sub2api",
      probes: [
        {
          declaration_id: "decl-1",
          name: "模型指纹",
          channel_ids: null,
          channel_names: null,
          target_models: null,
          policy_text: "按需 · 无定时",
          last_run_at: null,
          last_run_status: "never_run",
          last_run_verdict: null,
          kill_switch_state: "not_applicable_fake",
          can_run_now: true,
        },
      ],
      freshness,
      assertion_disclaimer: "检测结果为形状与延迟检测，非语义正确性保证。",
      channel_breakdown_supported: true,
    });

    const result = await getPlatformAssuranceProbes("sub2api", {}, c);

    expect(c.get).toHaveBeenCalledWith("/api/v1/platforms/sub2api/assurance/probes", {});
    expect(result).toEqual({
      platform: "sub2api",
      probes: [
        {
          declarationId: "decl-1",
          name: "模型指纹",
          channelIds: [],
          channelNames: [],
          targetModels: [],
          policyText: "按需 · 无定时",
          scheduleCron: "",
          lastRunAt: null,
          lastRunStatus: "never_run",
          lastRunVerdict: null,
          killSwitchState: "not_applicable_fake",
          canRunNow: true,
          cannotRunReason: "",
          cannotRunReasonText: "",
        },
      ],
      freshness,
      assertionDisclaimer: "检测结果为形状与延迟检测，非语义正确性保证。",
      channelBreakdownSupported: true,
    });
  });

  it("检测历史：cursor/limit 只在传了才拼进 URL，next_cursor 原样透传", async () => {
    const c = client({
      platform: "sub2api",
      entries: [
        {
          observed_at: "2026-08-31T02:30:00Z",
          channel_id: "chn-1",
          model: "claude-sonnet-4",
          declaration_name: "模型指纹",
          prompt_template_key: "model_fingerprint",
          status: "ok",
          verdict: "一致",
          evidence_ref: "probe-8842",
          latency_ms: 12,
          first_token_ms: null,
          measured_first_token: false,
          tokens_used: 8,
          http_status: 200,
        },
      ],
      next_cursor: "2026-08-31T02:29:00Z",
      freshness,
      channel_breakdown_supported: true,
    });

    const result = await getPlatformAssuranceProbeHistory("sub2api", { cursor: "2026-08-31T02:30:00Z", limit: 50 }, c);

    expect(c.get).toHaveBeenCalledWith("/api/v1/platforms/sub2api/assurance/probe-history", {
      searchParams: { cursor: "2026-08-31T02:30:00Z", limit: "50" },
    });
    expect(result.nextCursor).toBe("2026-08-31T02:29:00Z");
    expect(result.entries[0]).toEqual({
      observedAt: "2026-08-31T02:30:00Z",
      channelId: "chn-1",
      externalChannelId: "",
      model: "claude-sonnet-4",
      declarationName: "模型指纹",
      promptTemplateKey: "model_fingerprint",
      status: "ok",
      verdict: "一致",
      evidenceRef: "probe-8842",
      latencyMs: 12,
      firstTokenMs: null,
      measuredFirstToken: false,
      tokensUsed: 8,
      httpStatus: 200,
      errorKind: "",
    });
  });

  it("declare@1：targets/expected_shape 编码成 JSON 字符串参数，新建时不带 declaration_id/expected_version", async () => {
    const c = client();

    await declareProbe(
      {
        platform: "sub2api",
        name: "模型指纹",
        promptTemplateKey: "model_fingerprint",
        targetHost: "api.example.com",
        maxTokens: 64,
        targets: [{ channelId: "chn-1", model: "claude-sonnet-4" }],
      },
      {},
      c,
    );

    expect(c.post).toHaveBeenCalledWith(
      "/api/v1/actions/assurance.probe.declare/versions/1/execute",
      {
        params: {
          platform: "sub2api",
          name: "模型指纹",
          prompt_template_key: "model_fingerprint",
          target_host: "api.example.com",
          targets: JSON.stringify([{ channel_id: "chn-1", external_channel_id: "", model: "claude-sonnet-4" }]),
          max_tokens: 64,
          expected_shape: JSON.stringify(PROBE_DEFAULT_EXPECTED_SHAPE),
        },
      },
      expect.objectContaining({ requestId: expect.any(String) }),
    );
  });

  it("declare@1：更新既有声明时带上 declaration_id 与 expected_version", async () => {
    const c = client();

    await declareProbe(
      {
        platform: "sub2api",
        name: "模型指纹",
        promptTemplateKey: "model_fingerprint",
        targetHost: "api.example.com",
        maxTokens: 64,
        targets: [{ channelId: "chn-1", model: "claude-sonnet-4" }],
        declarationId: "decl-1",
        expectedVersion: 2,
      },
      {},
      c,
    );

    const [, body] = (c.post as ReturnType<typeof vi.fn>).mock.calls[0]!;
    expect((body as { params: Record<string, unknown> }).params.declaration_id).toBe("decl-1");
    expect((body as { params: Record<string, unknown> }).params.expected_version).toBe(2);
  });

  it("cancel@1 只带 declaration_id/reason 两个白名单参数", async () => {
    const c = client();
    await cancelProbe({ declarationId: "decl-1", reason: "不再需要" }, {}, c);
    expect(c.post).toHaveBeenCalledWith(
      "/api/v1/actions/assurance.probe.cancel/versions/1/execute",
      { params: { declaration_id: "decl-1", reason: "不再需要" } },
      expect.objectContaining({ requestId: expect.any(String) }),
    );
  });

  it("run@1：client_run_key 只在提供时才出现在参数里", async () => {
    const c = client();
    await runProbe({ declarationId: "decl-1" }, {}, c);
    expect((c.post as ReturnType<typeof vi.fn>).mock.calls[0]![1]).toEqual({
      params: { declaration_id: "decl-1" },
    });

    await runProbe({ declarationId: "decl-1", clientRunKey: "click-1" }, {}, c);
    expect((c.post as ReturnType<typeof vi.fn>).mock.calls[1]![1]).toEqual({
      params: { declaration_id: "decl-1", client_run_key: "click-1" },
    });
  });

  it("kill_switch.set@1：省略 probeCredentialRef 时不发这个字段（保留原值），显式空串会发", async () => {
    const c = client();
    await setProbeKillSwitch({ platform: "sub2api", probeEnabled: false }, {}, c);
    expect((c.post as ReturnType<typeof vi.fn>).mock.calls[0]![1]).toEqual({
      params: { platform: "sub2api", probe_enabled: false },
    });

    await setProbeKillSwitch({ platform: "sub2api", probeEnabled: true, probeCredentialRef: "" }, {}, c);
    expect((c.post as ReturnType<typeof vi.fn>).mock.calls[1]![1]).toEqual({
      params: { platform: "sub2api", probe_enabled: true, probe_credential_ref: "" },
    });
  });

  it("暴露的常量与权限点字面量不漂移", () => {
    expect(PROBE_MANAGE_PERMISSION).toBe("assurance.probe.manage");
    expect(PROBE_RUN_PERMISSION).toBe("assurance.probe.run");
    expect(PROBE_KILL_SWITCH_PERMISSION).toBe("assurance.probe.kill_switch");
    expect(PROBE_KILL_SWITCH_ROLE).toBe("assurance-probe-admin");
    expect(PROBE_MAX_TOKENS_DEFAULT).toBe(64);
    expect(PROBE_MAX_TOKENS_CAP).toBe(512);
    expect(isProbeTemplateKey("benchmark_set")).toBe(true);
    expect(isProbeTemplateKey("free_text")).toBe(false);
    expect(probeTemplateLabel("context_length")).toBe("上下文长度");
    expect(probeTemplateLabel("unknown_key")).toBe("unknown_key");
  });
});
