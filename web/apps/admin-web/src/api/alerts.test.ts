import { describe, expect, it, vi } from "vitest";
import {
  ACTIVE_ALERT_STATUSES,
  ALERT_RULES,
  ALERT_STATUS_ALL,
  acknowledgeAlert,
  createSilence,
  listAlerts,
  ruleLabel,
} from "./alerts";
import type { ApiClient } from "./client";
import type { PlatformApiConfig } from "./config";

const config: PlatformApiConfig = {
  baseUrl: "",
  principalId: "dev-operator",
  principalType: "HUMAN",
  scopes: ["ops.read", "alerts.alert.manage", "alerts.silence.manage"],
};

function fakeClient(body: unknown): ApiClient {
  return {
    get: vi.fn().mockResolvedValue(body),
    post: vi.fn().mockResolvedValue(body),
  } as unknown as ApiClient;
}

/** 取出第一次 post 的三个实参。
 *
 *  tsconfig 开了 noUncheckedIndexedAccess，`mock.calls[0]` 的类型带 undefined。
 *  在这里显式断言一次，好过在每个用例里各写一遍非空断言。 */
function firstPost(client: ApiClient): [string, { params: Record<string, unknown> }, { requestId?: string }] {
  const call = (client.post as ReturnType<typeof vi.fn>).mock.calls[0];
  if (!call) throw new Error("没有发出任何 POST 请求");
  return call as [string, { params: Record<string, unknown> }, { requestId?: string }];
}

describe("listAlerts", () => {
  it("items 为 null 时按空数组处理，页面不会炸", async () => {
    const client = fakeClient({ items: null });
    await expect(listAlerts({}, client, config)).resolves.toEqual([]);
  });

  it("不传 status 时不带该查询参数（后端默认返回全部活跃告警）", async () => {
    const client = fakeClient({ items: [] });
    await listAlerts({}, client, config);
    expect(client.get).toHaveBeenCalledWith("/api/v1/alerts", {
      searchParams: { environment: undefined, status: undefined },
    });
  });

  it("状态数组拼成逗号分隔", async () => {
    const client = fakeClient({ items: [] });
    await listAlerts({ status: ["OPEN", "REOPENED"] }, client, config);
    expect(client.get).toHaveBeenCalledWith("/api/v1/alerts", {
      searchParams: { environment: undefined, status: "OPEN,REOPENED" },
    });
  });

  it("status=all 原样传给后端（含已解决的路径）", async () => {
    const client = fakeClient({ items: [] });
    await listAlerts({ status: ALERT_STATUS_ALL }, client, config);
    expect(client.get).toHaveBeenCalledWith("/api/v1/alerts", {
      searchParams: { environment: undefined, status: "all" },
    });
  });

  it("environment 跟随配置——前端不自己猜环境（跨环境读取后端会拒）", async () => {
    const client = fakeClient({ items: [] });
    await listAlerts({}, client, { ...config, environment: "production" });
    expect(client.get).toHaveBeenCalledWith("/api/v1/alerts", {
      searchParams: { environment: "production", status: undefined },
    });
  });

  it("limit 传了才带上", async () => {
    const client = fakeClient({ items: [] });
    await listAlerts({ limit: 50 }, client, config);
    expect(client.get).toHaveBeenCalledWith("/api/v1/alerts", {
      searchParams: { environment: undefined, status: undefined, limit: "50" },
    });
  });
});

describe("活跃状态清单", () => {
  it("包含 SILENCED——静默不是终态", () => {
    // 去掉它的话，界面上会出现「静默期间告警凭空消失、窗口一过又凭空出现」。
    expect(ACTIVE_ALERT_STATUSES).toContain("SILENCED");
    expect(ACTIVE_ALERT_STATUSES).not.toContain("RESOLVED");
    expect(ACTIVE_ALERT_STATUSES).toHaveLength(4);
  });
});

describe("写路径", () => {
  it("确认走 alerts.alert.acknowledge@1，只传 alert_id", async () => {
    const client = fakeClient({ action_run_id: "run-1" });
    const run = await acknowledgeAlert("alert-42", {}, client);
    expect(run.runId).toBe("run-1");
    const [path, body, opts] = firstPost(client);
    expect(path).toBe("/api/v1/actions/alerts.alert.acknowledge/versions/1/execute");
    expect(body).toEqual({ params: { alert_id: "alert-42" } });
    // request_id 走请求头而不是请求体：后端用 DisallowUnknownFields 解析，
    // 塞进 body 会 400（规格 §5.8）
    expect(opts.requestId).toBeTruthy();
  });

  it("静默走 alerts.silence.create@1，三个字段一个不多一个不少", async () => {
    const client = fakeClient({ action_run_id: "run-2" });
    await createSilence(
      { rule_key: "metric.sync.failed", duration_minutes: 60, reason: "上游维护" },
      {},
      client,
    );
    const [path, body] = firstPost(client);
    expect(path).toBe("/api/v1/actions/alerts.silence.create/versions/1/execute");
    // 后端 Schema 是白名单语义：多一个字段就是 400
    expect(body).toEqual({
      params: { rule_key: "metric.sync.failed", duration_minutes: 60, reason: "上游维护" },
    });
  });

  it("全局静默传空串 rule_key（这是一个明确的选择，不是漏填）", async () => {
    const client = fakeClient({ action_run_id: "run-3" });
    await createSilence({ rule_key: "", duration_minutes: 30, reason: "全站发布窗口" }, {}, client);
    const [, body] = firstPost(client);
    expect(body.params.rule_key).toBe("");
  });
});

describe("规则清单", () => {
  it("五条规则，键的形态与后端 CHECK 一致", () => {
    expect(ALERT_RULES).toHaveLength(5);
    for (const rule of ALERT_RULES) {
      // 库层 CHECK：^[a-z0-9][a-z0-9_.-]{0,127}$
      expect(rule.key).toMatch(/^[a-z0-9][a-z0-9_.-]{0,127}$/);
      expect(rule.label.length).toBeGreaterThan(0);
    }
    const keys = ALERT_RULES.map((r) => r.key);
    expect(new Set(keys).size).toBe(keys.length);
  });

  it("未知规则键原样显示，不换成「未知规则」", () => {
    // 后端加了新规则而前端还没跟上时，显示原始键仍然是有用的信息；
    // 显示成「未知规则」则是在丢事实。
    expect(ruleLabel("metric.sync.failed")).toBe("指标同步失败");
    expect(ruleLabel("brand.new.rule")).toBe("brand.new.rule");
  });
});
