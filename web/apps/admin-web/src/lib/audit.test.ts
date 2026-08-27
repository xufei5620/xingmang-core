import { describe, expect, it } from "vitest";
import type { AuditEventItem } from "../api/platform";
import {
  describeAuditResult,
  formatSummary,
  GENESIS_HASH,
  HASH_PREFIX_LENGTH,
  toAuditRow,
} from "./audit";

const HASH_A = "a".repeat(64);
const HASH_B = "b".repeat(64);

function event(over: Partial<AuditEventItem> = {}): AuditEventItem {
  return {
    sequence: 42,
    occurred_at: "2026-08-26T10:00:00Z",
    principal_id: "dev-operator",
    principal_type: "HUMAN",
    action_id: "registry.service.create",
    action_version: "1",
    action_run_id: "11111111-2222-3333-4444-555555555555",
    resource_type: "core.service",
    resource_id: "99999999-8888-7777-6666-555555555555",
    environment: "development",
    request_id: "req-7",
    result: "succeeded",
    error_code: "",
    before_summary: null,
    after_summary: { instance_id: "sub2api-dev", status: "active" },
    event_hash: HASH_A,
    prev_hash: HASH_B,
    ...over,
  };
}

describe("describeAuditResult", () => {
  it("成功走 success 语气，失败走 danger", () => {
    expect(describeAuditResult("succeeded")).toEqual({ label: "成功", tone: "success" });
    expect(describeAuditResult("failed")).toEqual({ label: "失败", tone: "danger" });
  });

  it("不认识的结果显式暴露成 warning，绝不默认当成成功", () => {
    const d = describeAuditResult("compensated");
    expect(d.tone).toBe("warning");
    expect(d.label).toContain("compensated");
    expect(d.tone).not.toBe("success");
  });

  it("空结果同样不当成成功", () => {
    expect(describeAuditResult("").tone).toBe("warning");
  });
});

describe("toAuditRow：动作与资源", () => {
  it("动作带上版本号——版本是动作契约的一部分", () => {
    expect(toAuditRow(event()).action).toBe("registry.service.create@1");
  });

  it("没有版本号时不硬拼一个 @", () => {
    expect(toAuditRow(event({ action_version: "" })).action).toBe("registry.service.create");
  });

  it("资源拼成 type/id；两者都没有时显示占位符", () => {
    expect(toAuditRow(event()).resource).toBe(
      "core.service/99999999-8888-7777-6666-555555555555",
    );
    expect(toAuditRow(event({ resource_type: "", resource_id: "" })).resource).toBe("—");
  });

  it("只有类型没有 id 时也不留下一个孤零零的斜杠", () => {
    expect(toAuditRow(event({ resource_id: "" })).resource).toBe("core.service");
  });
});

describe("toAuditRow：结果与错误码", () => {
  it("成功事件不显示错误码（后端可能带一个空串）", () => {
    expect(toAuditRow(event()).errorCode).toBe("");
  });

  it("失败事件把错误码带出来，供直接对上服务端日志", () => {
    const row = toAuditRow(event({ result: "failed", error_code: "PERMISSION_DENIED" }));
    expect(row.resultTone).toBe("danger");
    expect(row.errorCode).toBe("PERMISSION_DENIED");
  });

  it("成功事件即使误带了错误码也不显示——那只会让人以为出了事", () => {
    expect(toAuditRow(event({ result: "succeeded", error_code: "X" })).errorCode).toBe("");
  });
});

describe("toAuditRow：哈希链", () => {
  it("表格里显示前 8 位，完整 64 位一并给出（链要可验证）", () => {
    const row = toAuditRow(event());
    expect(row.hashShort).toBe("a".repeat(HASH_PREFIX_LENGTH));
    expect(row.hashFull).toBe(HASH_A);
    expect(row.prevHashShort).toBe("b".repeat(HASH_PREFIX_LENGTH));
    expect(row.prevHashFull).toBe(HASH_B);
  });

  it("prev_hash 全 0 认作链首，不当成断链", () => {
    expect(GENESIS_HASH).toHaveLength(64);
    expect(toAuditRow(event({ prev_hash: GENESIS_HASH })).isGenesis).toBe(true);
    expect(toAuditRow(event()).isGenesis).toBe(false);
  });

  it("哈希缺失时显示占位符而不是空白（空白会被当成没渲染出来）", () => {
    const row = toAuditRow(event({ event_hash: "", prev_hash: "" }));
    expect(row.hashShort).toBe("—");
    expect(row.prevHashShort).toBe("—");
  });
});

describe("toAuditRow：时间与摘要", () => {
  it("正文本地时间、悬停 UTC，两者都不省略时区", () => {
    const row = toAuditRow(event());
    expect(row.utcTime).toBe("2026-08-26 10:00:00 UTC");
    expect(row.localTime).toMatch(/\(UTC[+-]\d{2}:\d{2}\)$/);
  });

  it("有后态就算有详情（新建资源没有前态）", () => {
    expect(toAuditRow(event()).hasDetail).toBe(true);
  });

  it("前后态都没有时不给展开按钮", () => {
    expect(toAuditRow(event({ before_summary: null, after_summary: null })).hasDetail).toBe(false);
  });

  it("空对象摘要不算详情——展开一片 {} 只是噪音", () => {
    expect(toAuditRow(event({ after_summary: {}, before_summary: {} })).hasDetail).toBe(false);
  });
});

describe("formatSummary", () => {
  it("null 明说「无」——新建没有前态，和「前态是空对象」不是一回事", () => {
    expect(formatSummary(null)).toBe("（无）");
    expect(formatSummary(undefined)).toBe("（无）");
    expect(formatSummary({})).toBe("{}");
  });

  it("原样缩进输出，不做任何美化改写（这是进了哈希的那份内容）", () => {
    expect(formatSummary({ b: 2, a: 1 })).toBe('{\n  "b": 2,\n  "a": 1\n}');
  });
});
