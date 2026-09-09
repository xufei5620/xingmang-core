import { describe, expect, it } from "vitest";
import type { ApprovalItem, ApprovalStatus } from "../api/approvals";
import {
  abilitiesFor,
  displayStatus,
  groupByRisk,
  isEffectivelyExpired,
  paramRows,
  statusLabel,
  voteProgress,
} from "./approvals";

const NOW = new Date("2026-09-07T10:00:00Z");

function approval(overrides: Partial<ApprovalItem> = {}): ApprovalItem {
  return {
    id: "11111111-2222-3333-4444-555555555555",
    action_id: "registry.connection.set_status",
    action_version: "1",
    risk_level: "L3",
    params: { connection_id: "c-1", status: "ACTIVE" },
    params_hash: "abc",
    requester_id: "staff_bob",
    requester_type: "HUMAN",
    reason: "上游换域名",
    status: "PENDING" as ApprovalStatus,
    created_at: "2026-09-07T08:00:00Z",
    expires_at: "2026-09-08T08:00:00Z",
    decisions: [],
    votes_required: 2,
    votes_cast: 0,
    privileged_vote_required: false,
    privileged_vote_cast: false,
    ...overrides,
  };
}

describe("groupByRisk", () => {
  it("按 L4 → L3 → L2 分组", () => {
    const groups = groupByRisk([
      approval({ id: "a", risk_level: "L2" }),
      approval({ id: "b", risk_level: "L4" }),
      approval({ id: "c", risk_level: "L3" }),
    ]);
    expect(groups.map((g) => g.riskLevel)).toEqual(["L4", "L3", "L2"]);
  });

  it("不认识的等级排在后面且不丢弃", () => {
    const groups = groupByRisk([
      approval({ id: "a", risk_level: "L5" }),
      approval({ id: "b", risk_level: "L3" }),
    ]);
    expect(groups.map((g) => g.riskLevel)).toEqual(["L3", "L5"]);
    // 静默吞掉一整组待审批比显示一个陌生的组名危险得多。
    expect(groups.flatMap((g) => g.items).map((i) => i.id).sort()).toEqual(["a", "b"]);
  });

  it("组内先到期的排前面", () => {
    const groups = groupByRisk([
      approval({ id: "later", expires_at: "2026-09-09T08:00:00Z" }),
      approval({ id: "sooner", expires_at: "2026-09-08T08:00:00Z" }),
    ]);
    expect(groups[0]?.items.map((i) => i.id)).toEqual(["sooner", "later"]);
  });

  it("空列表得到空分组，而不是三个空组", () => {
    expect(groupByRisk([])).toEqual([]);
  });
});

describe("isEffectivelyExpired", () => {
  // ExpirePending 定时任务在 XM-0030c 之前没有人调，库里的 status 会一直
  // 停在 PENDING。界面照抄 status 的话，队列里会混着一批谁也批不动的单。
  it("库里说 PENDING 但已过 expires_at 时判为已过期", () => {
    const item = approval({ status: "PENDING", expires_at: "2026-09-07T09:00:00Z" });
    expect(isEffectivelyExpired(item, NOW)).toBe(true);
    expect(displayStatus(item, NOW)).toBe("EXPIRED");
    expect(statusLabel(displayStatus(item, NOW))).toBe("已过期");
  });

  it("还没到期就不判过期", () => {
    const item = approval({ status: "PENDING", expires_at: "2026-09-07T11:00:00Z" });
    expect(isEffectivelyExpired(item, NOW)).toBe(false);
    expect(displayStatus(item, NOW)).toBe("PENDING");
  });

  it("已批准但过了期同样判为已过期", () => {
    const item = approval({ status: "APPROVED", expires_at: "2026-09-07T09:00:00Z" });
    expect(isEffectivelyExpired(item, NOW)).toBe(true);
  });

  it.each(["EXECUTED", "REJECTED", "CANCELLED", "EXPIRED"] as const)(
    "终态 %s 不再谈过期",
    (status) => {
      // 一张已执行的单的 expires_at 早就过去了，说它「已过期」是错的。
      const item = approval({ status, expires_at: "2026-09-01T00:00:00Z" });
      expect(isEffectivelyExpired(item, NOW)).toBe(false);
      expect(displayStatus(item, NOW)).toBe(status);
    },
  );

  it("expires_at 解析不出来时不硬判过期", () => {
    const item = approval({ status: "PENDING", expires_at: "不是时间" });
    expect(isEffectivelyExpired(item, NOW)).toBe(false);
  });
});

describe("voteProgress", () => {
  it("差票时说还差几票", () => {
    expect(voteProgress(approval({ votes_cast: 1, votes_required: 2 }), NOW)).toBe(
      "还差 1 票（已 1/2）",
    );
  });

  // 只说「已 2/2 票」会让人以为该批了，而它还挂着——那看起来像故障。
  it("票数满但缺特权票时单独说清楚", () => {
    const item = approval({
      votes_cast: 2,
      votes_required: 2,
      privileged_vote_required: true,
      privileged_vote_cast: false,
    });
    expect(voteProgress(item, NOW)).toContain("还缺一张 approval.l4 特权票");
  });

  it("既差票又缺特权票时两件事一起说", () => {
    const item = approval({
      votes_cast: 0,
      votes_required: 2,
      privileged_vote_required: true,
      privileged_vote_cast: false,
    });
    const text = voteProgress(item, NOW);
    expect(text).toContain("还差 2 票");
    expect(text).toContain("approval.l4");
  });

  it("特权票已投则不再提它", () => {
    const item = approval({
      votes_cast: 1,
      votes_required: 2,
      privileged_vote_required: true,
      privileged_vote_cast: true,
    });
    expect(voteProgress(item, NOW)).toBe("还差 1 票（已 1/2）");
  });

  // Settle 对未配票数的等级一律返回 PENDING。说出来，别让人对着一张
  // 永远不动的单发呆。
  it("条件都满足却还是 PENDING 时指向策略配置", () => {
    const item = approval({ votes_cast: 2, votes_required: 2 });
    expect(voteProgress(item, NOW)).toContain("未配置票数");
  });

  it("非 PENDING 的单不谈还差几票", () => {
    const item = approval({ status: "REJECTED", votes_cast: 0, votes_required: 2 });
    expect(voteProgress(item, NOW)).toBe("0/2 票");
  });

  it("过期的单也不谈还差几票", () => {
    const item = approval({ votes_cast: 1, votes_required: 2, expires_at: "2026-09-07T09:00:00Z" });
    expect(voteProgress(item, NOW)).toBe("1/2 票");
  });
});

describe("abilitiesFor", () => {
  const me = "staff_alice";

  it("PENDING 可投票、不可执行", () => {
    const a = abilitiesFor(approval(), me, NOW);
    expect(a.canVote).toBe(true);
    expect(a.canExecute).toBe(false);
  });

  it("APPROVED 可执行、不可投票", () => {
    const a = abilitiesFor(approval({ status: "APPROVED" }), me, NOW);
    expect(a.canExecute).toBe(true);
    expect(a.canVote).toBe(false);
  });

  it.each(["REJECTED", "EXECUTED", "CANCELLED", "EXPIRED"] as const)(
    "终态 %s 三个动作都不给",
    (status) => {
      const a = abilitiesFor(approval({ status }), me, NOW);
      expect([a.canVote, a.canExecute, a.canCancel]).toEqual([false, false, false]);
    },
  );

  it("已过期的 PENDING 单不给投票按钮", () => {
    const a = abilitiesFor(approval({ expires_at: "2026-09-07T09:00:00Z" }), me, NOW);
    expect(a.canVote).toBe(false);
  });

  it("投过票的人不再给投票按钮，并说明原因", () => {
    const item = approval({
      decisions: [
        {
          approver_id: me,
          approver_type: "HUMAN",
          verdict: "APPROVE",
          comment: "",
          privileged: false,
          created_at: "2026-09-07T09:00:00Z",
        },
      ],
    });
    const a = abilitiesFor(item, me, NOW);
    expect(a.canVote).toBe(false);
    expect(a.voteBlockedReason).toContain("投过票");
    // 对照：换一个人看同一张单就该能投——确认拦住的是「我投过」。
    expect(abilitiesFor(item, "staff_carol", NOW).canVote).toBe(true);
  });

  it("L3 提交人不能自批，L2 可以", () => {
    const l3 = approval({ risk_level: "L3", requester_id: me });
    expect(abilitiesFor(l3, me, NOW).canVote).toBe(false);
    expect(abilitiesFor(l3, me, NOW).voteBlockedReason).toContain("不允许提交人");
    const l2 = approval({ risk_level: "L2", requester_id: me });
    expect(abilitiesFor(l2, me, NOW).canVote).toBe(true);
  });

  it("只有提交人能撤回，且只在 PENDING 时", () => {
    expect(abilitiesFor(approval({ requester_id: me }), me, NOW).canCancel).toBe(true);
    expect(abilitiesFor(approval({ requester_id: "staff_bob" }), me, NOW).canCancel).toBe(false);
    expect(
      abilitiesFor(approval({ requester_id: me, status: "APPROVED" }), me, NOW).canCancel,
    ).toBe(false);
  });

  // 把「判不出来」当成「不是我」会把提交人自己的撤回按钮也藏掉。
  it("身份判不出来时只按状态判，不做自我判断", () => {
    const item = approval({ requester_id: "staff_bob", risk_level: "L3" });
    const a = abilitiesFor(item, "", NOW);
    expect(a.canVote).toBe(true);
    expect(a.voteBlockedReason).toBe("");
    expect(a.canCancel).toBe(false);
  });

  // 权限在任何鉴权模式下都不下发到前端，所以这里一律不按权限藏按钮——
  // 猜错的两个方向都不好。这条钉住「不猜」这个决定本身。
  it("不按权限藏按钮：同一张单对任何身份都给投票入口", () => {
    const item = approval();
    for (const who of ["staff_alice", "staff_carol", "agent_claude", ""]) {
      expect(abilitiesFor(item, who, NOW).canVote).toBe(true);
    }
  });
});

describe("paramRows", () => {
  // params_hash 算的就是排序后的 JSON，界面照同一个顺序摆，人肉核对时对得上。
  it("按键名排序", () => {
    expect(paramRows({ zeta: 1, alpha: 2 }).map((r) => r.key)).toEqual(["alpha", "zeta"]);
  });

  it("各类值都摊成可读文本", () => {
    const rows = paramRows({ s: "x", n: 1, b: true, nil: null, obj: { a: 1 }, arr: [1, 2] });
    const byKey = Object.fromEntries(rows.map((r) => [r.key, r.value]));
    expect(byKey).toEqual({
      s: "x",
      n: "1",
      b: "true",
      nil: "null",
      obj: '{"a":1}',
      arr: "[1,2]",
    });
  });

  it("空参数得到空行，不报错", () => {
    expect(paramRows({})).toEqual([]);
  });
});
