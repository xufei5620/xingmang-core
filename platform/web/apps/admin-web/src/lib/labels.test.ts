import { describe, expect, it } from "vitest";
import {
  ACTION_ERROR_CODES,
  APPROVAL_STATUSES,
  errorCodeHint,
  errorCodeNote,
  fullLabel,
  httpStatusText,
  principalTypeText,
  RISK_LEVELS,
  riskLevel,
  riskLevelText,
  withCode,
} from "./labels";

/** 对照表**内容**的对账（后端有没有新枚举）在 labels.reconcile.test.ts。
 *  这一份测的是**形制**：中文怎么摆、原码有没有留住、认不出来的码会怎样。 */

describe("withCode：中文在前，原码保留", () => {
  it("认识的码：中文在前，原码逐字在括号里", () => {
    expect(withCode("权限不足", "PERMISSION_DENIED")).toBe("权限不足（PERMISSION_DENIED）");
  });

  it("不认识的码：只回原码，一个字都不加", () => {
    // 「未知权限」之类的兜底会把一个有名有姓的问题变成没名字的问题。
    expect(withCode(undefined, "BRAND_NEW_CODE")).toBe("BRAND_NEW_CODE");
  });
});

describe("errorCodeNote：错误条上那句话", () => {
  it("认识的码：中文在前，原码逐字跟在「错误码」后面", () => {
    expect(errorCodeNote("INVALID_PARAMS")).toBe("参数不合法，错误码 INVALID_PARAMS");
    expect(errorCodeNote("ADVANCED_CONTROLS_REQUIRED")).toBe(
      "需要高级管控，错误码 ADVANCED_CONTROLS_REQUIRED",
    );
  });

  it("不认识的码原样吐出来，且不掺任何兜底词", () => {
    const note = errorCodeNote("QUOTA_EXHAUSTED");
    // 正向：原码逐字在。
    expect(note).toContain("QUOTA_EXHAUSTED");
    // 缺席：不能出现「未知 / 不明 / 其他」这类把问题抹成无名的说法。
    expect(note).not.toMatch(/未知|不明|其他/);
    // 逐字钉死：上面两条都成立、却在原码前面加了半句猜测的话，也是不合格的。
    expect(note).toBe("错误码 QUOTA_EXHAUSTED");
  });

  it("空串也不编：没有码就是没有码", () => {
    expect(errorCodeNote("")).toBe("错误码 ");
  });

  it("认不出来的码没有悬停解释，而不是给一句含糊的", () => {
    expect(errorCodeHint("QUOTA_EXHAUSTED")).toBe("");
    expect(errorCodeHint("INTERNAL").length).toBeGreaterThan(0);
  });

  it("对照：每个已知码的中文都不是把原码抄一遍", () => {
    // 少了这条，上面「不认识就原样返回」可能只是因为**所有**码都原样返回。
    for (const [code, meaning] of Object.entries(ACTION_ERROR_CODES)) {
      expect(meaning.label, code).not.toBe(code);
      expect(errorCodeNote(code), code).toContain(code);
    }
  });
});

describe("fullLabel：限定语进括号", () => {
  it("有限定语时拼成「已批准（待执行）」", () => {
    expect(fullLabel(APPROVAL_STATUSES.APPROVED)).toBe("已批准（待执行）");
  });

  it("没有限定语时就是中文名本身，不留一对空括号", () => {
    expect(fullLabel(APPROVAL_STATUSES.PENDING)).toBe("待审批");
  });

  it("APPROVED 的限定语必须说清「还没执行」", () => {
    // 「已批准」三个字最容易被读成「这件事做完了」，而动作此刻还没发生。
    expect(APPROVAL_STATUSES.APPROVED.note).toBe("待执行");
  });
});

describe("riskLevelText：等级串在前，中文补在后", () => {
  it("认识的等级：原串在最前面，一个字符不改", () => {
    expect(riskLevelText("L3")).toBe("L3 高风险");
    expect(riskLevelText("L0")).toBe("L0 最低风险");
  });

  it("不认识的等级原样显示，绝不兜底成某个已知等级", () => {
    // 兜底的两个方向都不好，而把一个更危险的新等级显示成较轻的那个尤其坏。
    expect(riskLevelText("L5")).toBe("L5");
    expect(riskLevel("L5")).toBeUndefined();
  });

  it("每个等级的 hint 都说清了「这一级会发生什么」", () => {
    // 光一个「L3」说明不了事。走审批的等级要能看出票数与有效期
    // （数字本身与 approval.DefaultPolicy() 的对账在 labels.reconcile.test.ts）。
    for (const [level, meaning] of Object.entries(RISK_LEVELS)) {
      expect(meaning.hint.length, level).toBeGreaterThan(0);
      if (meaning.requiresApproval) {
        expect(meaning.hint, level).toContain("审批");
      } else {
        expect(meaning.hint, level).toContain("不经过审批");
      }
    }
  });
});

describe("principalTypeText：身份类别", () => {
  it("认识的类别：中文在前，原码在括号里", () => {
    expect(principalTypeText("HUMAN")).toBe("人（HUMAN）");
    expect(principalTypeText("SERVER_AGENT")).toBe("服务器代理（SERVER_AGENT）");
  });

  it("不认识的类别原样显示", () => {
    // api/config.ts 的 PrincipalType 今天还留着一个后端不认的 MACHINE
    // （principal.ParseType 只收 HUMAN/SERVICE/AI/SERVER_AGENT），
    // 界面照原样显示它，而不是替它编一个中文名把这个不一致盖过去。
    expect(principalTypeText("MACHINE")).toBe("MACHINE");
  });
});

describe("httpStatusText：状态码在前，中文补在后", () => {
  it("认识的状态码补上中文", () => {
    expect(httpStatusText(404)).toBe("HTTP 404 未找到");
    expect(httpStatusText(501)).toBe("HTTP 501 功能尚未上线");
    expect(httpStatusText(0)).toBe("HTTP 0 网络不可达");
  });

  it("不认识的状态码只回数字，不猜", () => {
    expect(httpStatusText(418)).toBe("HTTP 418");
  });
});
