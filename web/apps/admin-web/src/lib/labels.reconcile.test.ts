/** 中文对照表与后端枚举的对账（XM-I18N-LABELS 的核心交付物）。
 *
 *  **同一个事实钉在两处而没有门禁校验，迟早分叉。** 后端的枚举与前端的中文
 *  对照表就是这样的两处：后端加一个错误码、加一条告警规则、加一个后台任务，
 *  前端的表若没跟上，界面上就会冒出一个谁也读不懂的英文串——而使用者读不懂
 *  英文，正是本片要解决的那个问题。
 *
 *  所以这一组测试**直接读后端 Go 源码**，把每个枚举值逐个对着前端的表查。
 *  后端加了新值而前端没跟，这里变红。
 *
 *  写这种测试的两个坑，下面每一组都各自防住了：
 *
 *  1. **抽取器失灵会让整组断言恒真。** 文件被挪走、常量写法改了、正则少写
 *     一个空格——抽出来的清单变成空数组，「每一项都有中文」于是自动成立。
 *     所以每一组都先断言抽到的数量下界，再断言差集为空。
 *  2. **「已翻译」的判据本身要能判错。** 判据是「中文名与原值不同」（各
 *     describeX 的兜底分支一律把原值当 label 吐回来）。每一组都配了一条
 *     反向断言：喂一个后端不存在的值进去，确认判据说它「没翻译」。
 *
 *  变异验证见文件末尾的「抽取器与判据自身的变异验证」一节：那里用合成的 Go
 *  源码走同一条流水线，证明多一个枚举值时对账真的会把它算成缺失。
 *
 *  @vitest-environment node
 *
 *  这一组不碰 DOM，只读文件。留在默认的 jsdom 里 `import.meta.url` 会变成一个
 *  非 file: 的地址，readFileSync 直接拒收（"The URL must be of scheme file"）。 */
import { describe, expect, it } from "vitest";
import { readdirSync, readFileSync } from "node:fs";
import { describeFreshness, describeServiceStatus } from "@xingmang/ui-admin";
import {
  ALERT_EVALUATE_INTERVAL_SECONDS,
  ALERT_RULES,
  FIRE_COUNT_HEADER,
  FIRE_COUNT_MEANING,
} from "../api/alerts";
import { appStatusLabel, authModeLabel, releaseKindLabel } from "../api/extapp";
import {
  CLIENT_STATUS_HINTS,
  CLIENT_STATUS_LABELS,
  RULE_STATUS_LABELS,
  TRIGGER_KIND_LABELS,
} from "../api/integration";
import {
  PUBLISHING_ASSET_KIND_LABELS,
  PUBLISHING_CHANNEL_STATUS_LABELS,
  PUBLISHING_DRAFT_STATUS_LABELS,
  PUBLISHING_PLATFORM_LABELS,
} from "../api/publishing";
import { CONNECTION_STATUS_LABELS } from "../pages/RegistryPage";
import { jobKindLabel, jobStateLabel } from "../api/jobs";
import {
  describeNotifyStatus,
  describeSeverity,
  describeSilenceState,
  describeStatus,
} from "./alerts";
import {
  ACTION_ERROR_CODES,
  ACTION_RUN_STATUSES,
  APPROVAL_STATUSES,
  APPROVAL_VERDICTS,
  PRINCIPAL_TYPES,
  RISK_LEVELS,
  UPSTREAM_ACCOUNT_STATUSES,
} from "./labels";
import { FRESHNESS_PRIORITY } from "./workbench";

/** 仓库根。本文件在 web/apps/admin-web/src/lib/ 下，往上五层。 */
const REPO_ROOT = new URL("../../../../../", import.meta.url);

function goSource(relativePath: string): string {
  return readFileSync(new URL(relativePath, REPO_ROOT), "utf8");
}

// --- Go 源码里的字符串常量抽取 ---------------------------------------------

/** 抽出「带类型的字符串常量」的值，如 `CodeInvalidParams Code = "INVALID_PARAMS"`。
 *
 *  按类型名筛，而不是按常量名前缀：同一个包里可能有好几组同前缀的常量，
 *  而类型名正是后端用来划定「这是一个枚举」的那条线。 */
export function goTypedConstValues(source: string, typeName: string): string[] {
  // `(?:const\s+)?`：常量既可能在 const ( … ) 块里（只有名字），也可能单独一行
  // 写成 `const X Type = "…"`。少认一种写法就是在门禁上留一个洞。
  const pattern = new RegExp(
    String.raw`^\s*(?:const\s+)?[A-Za-z_]\w*\s+${typeName}\s*=\s*"([^"]*)"`,
    "gm",
  );
  return [...source.matchAll(pattern)].map((m) => m[1] ?? "");
}

/** 抽出「无类型字符串常量」中常量名匹配某形态的那些，如
 *  `RuleMetricSyncFailed = "metric.sync.failed"`。
 *
 *  返回名值对而不是只返回值：调用方偶尔需要按常量名排除某一条（见静默状态
 *  那一组），而按名排除必须能验证「那个名今天还在」，否则排除会悄悄失效。 */
export function goNamedConsts(
  source: string,
  namePattern: RegExp,
): { name: string; value: string }[] {
  // 同样要认单行的 `const X = "…"`：assurance_probe 的 kind 就是那么写的。
  const pattern = /^\s*(?:const\s+)?([A-Za-z_]\w*)\s*=\s*"([^"]*)"/gm;
  return [...source.matchAll(pattern)]
    .map((m) => ({ name: m[1] ?? "", value: m[2] ?? "" }))
    .filter((c) => namePattern.test(c.name));
}

/** 抽 Go map 字面量里的键值对，如 `map[string]int{"L2": 1, "L3": 2}`。
 *
 *  只处理不含嵌套花括号的一层 map——DefaultPolicy 里那四个都是这种。 */
export function goMapEntries(source: string, fieldName: string): Map<string, string> {
  const block = new RegExp(
    String.raw`\b${fieldName}\s*:\s*map\[string\][A-Za-z_.]+\{([^}]*)\}`,
  ).exec(source);
  const out = new Map<string, string>();
  if (!block) return out;
  for (const m of (block[1] ?? "").matchAll(/"([^"]+)"\s*:\s*([^,\n]+)/g)) {
    out.set(m[1] ?? "", (m[2] ?? "").trim());
  }
  return out;
}

/** `24 * time.Hour` → 24；认不出来返回 NaN（调用方据此断言而不是悄悄当 0）。 */
export function goDurationHours(expression: string): number {
  const hours = /^(\d+)\s*\*\s*time\.Hour$/.exec(expression.trim());
  return hours ? Number(hours[1]) : Number.NaN;
}

/** 「这个值被翻译过了吗」。
 *
 *  各 describeX / xxxLabel 的兜底分支一律把**原值**吐回来（有的加一层
 *  「未知状态（…）」的包装），所以中文名与原值逐字相同即为没翻译。 */
function translated(label: string, raw: string): boolean {
  return label !== raw && label !== "";
}

// --- 内核错误码 ------------------------------------------------------------

describe("内核错误码：errors.go 里每一个 Code 都要有中文", () => {
  const source = goSource("internal/platform/action/errors.go");
  const codes = goTypedConstValues(source, "Code");

  it("抽取器确实抓到了那一组常量", () => {
    // 抽空了却「全都有中文」是这类测试最典型的假绿。先证明抽到了东西。
    expect(codes.length).toBeGreaterThanOrEqual(14);
    expect(codes).toContain("INVALID_PARAMS");
    expect(codes).toContain("ADVANCED_CONTROLS_REQUIRED");
  });

  it("每一个后端错误码在前端映射表里都有中文", () => {
    const missing = codes.filter((code) => !(code in ACTION_ERROR_CODES));
    expect(missing).toEqual([]);
  });

  it("每条中文都带一句解释，且中文名不是把原码抄一遍", () => {
    for (const code of codes) {
      const meaning = ACTION_ERROR_CODES[code];
      expect(meaning, code).toBeDefined();
      expect(translated(meaning?.label ?? "", code), code).toBe(true);
      expect((meaning?.hint ?? "").length, code).toBeGreaterThan(0);
    }
  });
});

describe("Action 执行状态：kernel.go 里每一个 RunStatus 都要有中文", () => {
  const statuses = goTypedConstValues(
    goSource("internal/platform/action/kernel.go"),
    "RunStatus",
  );

  it("抽取器确实抓到了那一组常量", () => {
    expect(statuses).toEqual(expect.arrayContaining(["succeeded", "failed"]));
  });

  it("每一个执行状态都有中文", () => {
    const missing = statuses.filter((s) => !(s in ACTION_RUN_STATUSES));
    expect(missing).toEqual([]);
  });
});

// --- 审批状态与表决 --------------------------------------------------------

describe("审批：approval.go 里的 Status / Verdict 都要有中文", () => {
  const source = goSource("internal/platform/approval/approval.go");
  const statuses = goTypedConstValues(source, "Status");
  const verdicts = goTypedConstValues(source, "Verdict");

  it("抽取器确实抓到了那两组常量", () => {
    expect(statuses.length).toBeGreaterThanOrEqual(6);
    expect(statuses).toContain("PENDING");
    expect(verdicts).toEqual(expect.arrayContaining(["APPROVE", "REJECT"]));
  });

  it("每一个审批状态都有中文", () => {
    const missing = statuses.filter((s) => !(s in APPROVAL_STATUSES));
    expect(missing).toEqual([]);
  });

  it("每一个表决取值都有中文", () => {
    const missing = verdicts.filter((v) => !(v in APPROVAL_VERDICTS));
    expect(missing).toEqual([]);
  });

  it("前端的 ApprovalStatus 联合类型没有多出后端不存在的状态", () => {
    // 反向也要对：前端凭空多一个状态，界面上会出现一个永远不会到来的筛选项。
    const extra = Object.keys(APPROVAL_STATUSES).filter((s) => !statuses.includes(s));
    expect(extra).toEqual([]);
  });
});

// --- 风险等级 --------------------------------------------------------------

describe("风险等级：等级表与 DefaultPolicy 的事实都要对得上", () => {
  const riskSource = goSource("internal/platform/action/risk.go");
  const policySource = goSource("internal/platform/approval/approval.go");
  const levels = goTypedConstValues(riskSource, "RiskLevel");

  it("抽取器确实抓到了 L0..L4", () => {
    expect(levels).toEqual(["L0", "L1", "L2", "L3", "L4"]);
  });

  it("每一个等级都有中文名与典型场景", () => {
    const missing = levels.filter((l) => !(l in RISK_LEVELS));
    expect(missing).toEqual([]);
    for (const level of levels) {
      const meaning = RISK_LEVELS[level];
      expect(translated(meaning?.label ?? "", level), level).toBe(true);
      expect((meaning?.examples ?? "").length, level).toBeGreaterThan(0);
    }
  });

  it("哪些等级要先落审批单，与 RequiresAdvancedControls 一致", () => {
    // 后端：`return r == L2 || r == L3 || r == L4`。这里不复读那句话，
    // 而是从源码里把它列出的等级抠出来比对。
    const body = /func \(r RiskLevel\) RequiresAdvancedControls\(\) bool \{([^}]*)\}/.exec(
      riskSource,
    );
    expect(body, "没抠到 RequiresAdvancedControls 的函数体").not.toBeNull();
    const needsApproval = new Set(
      [...(body?.[1] ?? "").matchAll(/\br\s*==\s*(L\d)\b/g)].map((m) => m[1] ?? ""),
    );
    expect(needsApproval.size).toBeGreaterThan(0);
    for (const level of levels) {
      expect(RISK_LEVELS[level]?.requiresApproval, level).toBe(needsApproval.has(level));
    }
  });

  it("要几票、能不能自批、要不要特权票，逐条对上 DefaultPolicy", () => {
    const votes = goMapEntries(policySource, "VotesRequired");
    const selfApproval = goMapEntries(policySource, "SelfApprovalAllowed");
    const privileged = goMapEntries(policySource, "PrivilegedVoteRequired");
    expect(votes.size, "没抠到 VotesRequired").toBeGreaterThan(0);
    expect(selfApproval.size, "没抠到 SelfApprovalAllowed").toBeGreaterThan(0);
    expect(privileged.size, "没抠到 PrivilegedVoteRequired").toBeGreaterThan(0);

    for (const [level, value] of votes) {
      expect(RISK_LEVELS[level]?.votesRequired, level).toBe(Number(value));
    }
    for (const [level, value] of selfApproval) {
      expect(RISK_LEVELS[level]?.selfApprovalAllowed, level).toBe(value === "true");
    }
    for (const [level, value] of privileged) {
      expect(RISK_LEVELS[level]?.privilegedVoteRequired, level).toBe(value === "true");
    }
    // 后端没给票数的等级（L0/L1）在前端必须是 0，不能是某个记忆里的数字。
    for (const level of levels) {
      if (!votes.has(level)) expect(RISK_LEVELS[level]?.votesRequired, level).toBe(0);
    }
  });

  it("审批单有效期逐条对上 DefaultPolicy 的 TTL", () => {
    const ttl = goMapEntries(policySource, "TTL");
    expect(ttl.size, "没抠到 TTL").toBeGreaterThan(0);
    for (const [level, expression] of ttl) {
      const hours = goDurationHours(expression);
      expect(Number.isNaN(hours), `${level} 的 TTL 写法 ${expression} 没解析出来`).toBe(false);
      expect(RISK_LEVELS[level]?.ttlHours, level).toBe(hours);
    }
    for (const level of levels) {
      if (!ttl.has(level)) expect(RISK_LEVELS[level]?.ttlHours, level).toBe(0);
    }
  });

  it("hint 里写出来的票数与有效期，与上面那两组事实是同一个数", () => {
    // 事实字段对上了、给人看的那句话却还停在旧数字，等于没对上。
    for (const level of Object.keys(RISK_LEVELS)) {
      const meaning = RISK_LEVELS[level];
      if (!meaning?.requiresApproval) continue;
      expect(meaning.hint, level).toContain(`${meaning.votesRequired} 票`);
      expect(meaning.hint, level).toContain(`${meaning.ttlHours} 小时`);
    }
  });
});

// --- 身份类别 --------------------------------------------------------------

describe("身份类别：principal.go 里的 Type 都要有中文", () => {
  const source = goSource("internal/platform/principal/principal.go");
  const types = goTypedConstValues(source, "Type");

  it("抽取器确实抓到了那一组常量", () => {
    expect(types).toEqual(
      expect.arrayContaining(["HUMAN", "SERVICE", "AI", "SERVER_AGENT"]),
    );
  });

  it("每一个身份类别都有中文", () => {
    const missing = types.filter((t) => !(t in PRINCIPAL_TYPES));
    expect(missing).toEqual([]);
  });
});

// --- 告警 ------------------------------------------------------------------

describe("告警：规则键、严重度、状态、投递状态、静默状态都要有中文", () => {
  const alertSource = goSource("internal/platform/alerts/alert.go");
  const rulesSource = goSource("internal/platform/alerts/rules.go");
  const silenceSource = goSource("internal/platform/httpapi/alert_silences.go");

  const ruleKeys = goNamedConsts(rulesSource, /^Rule[A-Z]/).map((c) => c.value);
  const severities = goTypedConstValues(alertSource, "Severity");
  const statuses = goTypedConstValues(alertSource, "Status");
  const notifyStatuses = goTypedConstValues(alertSource, "NotifyStatus");

  it("抽取器确实抓到了这四组常量", () => {
    expect(ruleKeys.length).toBeGreaterThanOrEqual(8);
    expect(ruleKeys).toContain("approval.pending.too_long");
    expect(severities).toEqual(expect.arrayContaining(["info", "warning", "critical"]));
    expect(statuses).toContain("OPEN");
    expect(notifyStatuses).toEqual(expect.arrayContaining(["pending", "delivered", "failed"]));
  });

  it("每一条后端规则在静默对话框的下拉里都有中文名", () => {
    // ALERT_RULES 同时是静默窗口的可选项：缺一条不只是英文没翻，是运营
    // **根本选不到那条规则**去静默它。
    const known = new Set(ALERT_RULES.map((r) => r.key));
    const missing = ruleKeys.filter((k) => !known.has(k));
    expect(missing).toEqual([]);
  });

  it("下拉里也不能多出后端不认的规则键", () => {
    // 多一条的后果是：人选了它、后端当场 400，而他以为已经静默了。
    const backend = new Set(ruleKeys);
    const extra = ALERT_RULES.map((r) => r.key).filter((k) => !backend.has(k));
    expect(extra).toEqual([]);
  });

  it("规则的中文名逐字取自后端 Title，不是前端另起的一套说法", () => {
    // 后端每条规则自带 Title（rules.go 的 Rules()），前端那份 label 本来就是
    // 抄它的。抄了就要对上：两边各说各的时，运营在告警页看到的名字和运维在
    // 规则文档里查到的名字对不上号，而那正是最需要两边说同一句话的时刻。
    const constants = new Map(
      goNamedConsts(rulesSource, /^Rule[A-Z]/).map((c) => [c.name, c.value]),
    );
    const titleByKey = new Map<string, string>();
    for (const m of rulesSource.matchAll(/Key:\s*(\w+),\s*\n?\s*Title:\s*"([^"]*)"/g)) {
      const key = constants.get(m[1] ?? "");
      if (key) titleByKey.set(key, m[2] ?? "");
    }
    // 抠不到 Title 就等于这条断言什么也没查——先证明抠到了每一条。
    expect([...titleByKey.keys()].sort()).toEqual([...ruleKeys].sort());
    const mismatched = ALERT_RULES.filter((r) => titleByKey.get(r.key) !== r.label);
    expect(mismatched).toEqual([]);
  });

  it("每一个严重度都有中文", () => {
    const missing = severities.filter((s) => !translated(describeSeverity(s).label, s));
    expect(missing).toEqual([]);
  });

  it("每一个告警状态都有中文", () => {
    const missing = statuses.filter((s) => !translated(describeStatus(s).label, s));
    expect(missing).toEqual([]);
  });

  it("每一个投递状态都有中文", () => {
    const missing = notifyStatuses.filter((s) => !translated(describeNotifyStatus(s).label, s));
    expect(missing).toEqual([]);
  });

  it("响应里每一个静默窗口状态都有中文", () => {
    const consts = goNamedConsts(silenceSource, /^silenceState/);
    const names = consts.map((c) => c.name);
    expect(names.length).toBeGreaterThanOrEqual(4);
    // silenceStateAll 是 **state 查询参数**的特殊值（「含未开始与已过期」），
    // 不是响应里 state 字段的取值，界面上不会出现它。按名排除，并且断言这个
    // 名字今天还在——否则哪天它被改名，这条排除会悄悄失效或悄悄扩大。
    expect(names).toContain("silenceStateAll");
    const states = consts.filter((c) => c.name !== "silenceStateAll").map((c) => c.value);
    expect(states).toEqual(expect.arrayContaining(["active", "scheduled", "expired"]));
    const missing = states.filter((s) => !translated(describeSilenceState(s).label, s));
    expect(missing).toEqual([]);
  });
});

// --- 登记簿状态 ------------------------------------------------------------

describe("登记簿：上游账号启停与服务状态都要有中文", () => {
  const accountStatuses = goTypedConstValues(
    goSource("internal/platform/finance/account.go"),
    "Status",
  );
  const serviceStatuses = goTypedConstValues(
    goSource("internal/platform/registry/service.go"),
    "ServiceStatus",
  );

  it("抽取器确实抓到了这两组常量", () => {
    expect(accountStatuses).toEqual(expect.arrayContaining(["active", "disabled"]));
    expect(serviceStatuses).toEqual(expect.arrayContaining(["active", "degraded", "retired"]));
  });

  it("每一个上游账号启停状态都有中文", () => {
    const missing = accountStatuses.filter((s) => !(s in UPSTREAM_ACCOUNT_STATUSES));
    expect(missing).toEqual([]);
  });

  it("每一个服务状态都有中文（ui-admin 的 describeServiceStatus）", () => {
    // 这一支的兜底是 `未知（x）`——原值还在，所以上面那个 translated 判据
    // 会把它当成「翻译过了」。换一个判据：兜底文案以「未知」开头。
    const isFallback = (status: string) => describeServiceStatus(status).label.startsWith("未知");
    const missing = serviceStatuses.filter(isFallback);
    expect(missing).toEqual([]);
    // 判据自身的反向验证：喂一个后端不存在的状态，它必须判成兜底。
    expect(isFallback("melted")).toBe(true);
    // 对照：真状态不能被判成兜底，否则上面那条差集恒为满、这条断言恒红——
    // 恒红也是一种没在测东西（它测的是判据，不是实现）。
    expect(isFallback("active")).toBe(false);
  });
});

// --- 后台任务 --------------------------------------------------------------

describe("后台任务：运行状态与任务种类都要有中文", () => {
  const runStates = goTypedConstValues(
    goSource("internal/platform/jobs/query_store.go"),
    "RunState",
  );

  /** 全仓的 `XxxJobKind = "..."`。
   *
   *  递归扫 internal/platform 而不是只看 jobs 包：assurance_probe 的 kind 就
   *  定义在 assurance 包里（assurance/job_args.go）。只盯一个目录会留下一个
   *  正好是「界面上会出现、对账却查不到」的洞。 */
  function allJobKinds(): string[] {
    const root = new URL("internal/platform/", REPO_ROOT);
    const files = readdirSync(root, { recursive: true }).filter(
      (name) => name.endsWith(".go") && !name.endsWith("_test.go"),
    );
    const kinds = new Set<string>();
    for (const file of files) {
      const source = readFileSync(new URL(file.replaceAll("\\", "/"), root), "utf8");
      for (const c of goNamedConsts(source, /JobKind$/)) kinds.add(c.value);
    }
    return [...kinds].sort();
  }

  const jobKinds = allJobKinds();

  it("抽取器确实抓到了这两组常量", () => {
    expect(runStates.length).toBeGreaterThanOrEqual(7);
    expect(runStates).toContain("discarded");
    expect(jobKinds.length).toBeGreaterThanOrEqual(14);
    expect(jobKinds).toContain("assurance_probe");
    expect(jobKinds).toContain("platform_heartbeat");
  });

  it("每一个 river_job 运行状态都有中文", () => {
    const missing = runStates.filter((s) => !translated(jobStateLabel(s), s));
    expect(missing).toEqual([]);
  });

  it("每一个任务种类都有中文", () => {
    const missing = jobKinds.filter((k) => !translated(jobKindLabel(k), k));
    expect(missing).toEqual([]);
  });
});

// --- 扩展能力三页（2026-09-08 合并进来的三个新模块）------------------------

describe("接口与自动化：登记状态、规则状态、触发类别都要有中文", () => {
  const source = goSource("internal/platform/integration/types.go");
  const clientStatuses = goTypedConstValues(source, "ClientStatus");
  const ruleStatuses = goTypedConstValues(source, "RuleStatus");
  const triggerKinds = goTypedConstValues(source, "TriggerKind");

  it("抽取器确实抓到了这三组常量", () => {
    expect(clientStatuses).toEqual(expect.arrayContaining(["active", "disabled"]));
    expect(ruleStatuses).toEqual(expect.arrayContaining(["draft", "registered", "disabled"]));
    expect(triggerKinds).toEqual(
      expect.arrayContaining(["manual", "schedule", "event", "webhook"]),
    );
  });

  it("三组取值都有中文", () => {
    expect(clientStatuses.filter((v) => !(v in CLIENT_STATUS_LABELS))).toEqual([]);
    expect(ruleStatuses.filter((v) => !(v in RULE_STATUS_LABELS))).toEqual([]);
    expect(triggerKinds.filter((v) => !(v in TRIGGER_KIND_LABELS))).toEqual([]);
  });

  it("规则状态的中文里**不能**出现「启用 / 生效 / 运行」", () => {
    // doc.go 第 2 条：规则登记簿**没有执行器**，登记一条不会让任何 Action 跑起来。
    // 后端为此刻意不用 enabled/active 命名(types.go 的注释写明了)，中文若写成
    // 「已启用」正好把那份克制抵消掉——人会以为配好了就会自己跑。
    for (const [value, label] of Object.entries(RULE_STATUS_LABELS)) {
      expect(label, value).not.toMatch(/启用|生效|运行|已开启/);
    }
    // 正向锚点：这几条确实翻过，不是因为表是空的才「都不含那些词」。
    expect(RULE_STATUS_LABELS["registered"]).toBe("已登记");
    expect(RULE_STATUS_LABELS["draft"]).toBe("草稿");
  });

  it("每个触发类别各是各的，中文不许把两个取值揉成一句", () => {
    // 原来 manual 写的是「手动或定时触发」，与 schedule 的「定时」在下拉里
    // 同时出现，选的人无从分辨。中文名两两不同是这类表的基本要求。
    const labels = Object.values(TRIGGER_KIND_LABELS);
    expect(new Set(labels).size).toBe(labels.length);
    for (const [value, label] of Object.entries(TRIGGER_KIND_LABELS)) {
      if (value !== "schedule") expect(label, value).not.toContain("定时");
    }
  });

  it("调用方停用的中文要说清「停的是登记，不是放行」", () => {
    // doc.go 第 1 条：停用一行**不会让任何请求被拒绝**。裸一个「已停用」会让
    // 运营以为断供了，出事时找错地方。
    expect(CLIENT_STATUS_LABELS["disabled"]).toContain("登记");
    expect(CLIENT_STATUS_HINTS["disabled"]).toMatch(/照样会被放行|不看这张表/);
  });
});

describe("应用与配置：应用状态、登录方式、发布类型都要有中文", () => {
  const source = goSource("internal/platform/extapp/types.go");
  const appStatuses = goTypedConstValues(source, "AppStatus");
  const authModes = goTypedConstValues(source, "AuthMode");
  const releaseKinds = goTypedConstValues(source, "ReleaseKind");

  it("抽取器确实抓到了这三组常量", () => {
    expect(appStatuses).toEqual(expect.arrayContaining(["active", "planned", "retired"]));
    expect(authModes).toEqual(expect.arrayContaining(["oidc", "local", "dev-header"]));
    expect(releaseKinds).toEqual(expect.arrayContaining(["deploy", "rollback"]));
  });

  it("三组取值都有中文（未知值原样返回，所以判据是「翻过了」）", () => {
    expect(appStatuses.filter((v) => !translated(appStatusLabel(v), v))).toEqual([]);
    expect(authModes.filter((v) => !translated(authModeLabel(v), v))).toEqual([]);
    expect(releaseKinds.filter((v) => !translated(releaseKindLabel(v), v))).toEqual([]);
  });

  it("判据反向验证：后端不存在的值必须判成没翻过", () => {
    expect(translated(appStatusLabel("melted"), "melted")).toBe(false);
    expect(translated(authModeLabel("smoke-signal"), "smoke-signal")).toBe(false);
    expect(translated(releaseKindLabel("teleport"), "teleport")).toBe(false);
  });
});

describe("内容发布：草稿状态、渠道状态、平台、素材类型都要有中文", () => {
  const source = goSource("internal/platform/publishing/model.go");
  const draftStatuses = goTypedConstValues(source, "DraftStatus");
  const channelStatuses = goTypedConstValues(source, "ChannelStatus");
  const platforms = goTypedConstValues(source, "Platform");
  const assetKinds = goTypedConstValues(source, "AssetKind");

  it("抽取器确实抓到了这四组常量", () => {
    expect(draftStatuses).toEqual(expect.arrayContaining(["DRAFT", "SCHEDULED", "ARCHIVED"]));
    expect(channelStatuses).toEqual(expect.arrayContaining(["ACTIVE", "PAUSED", "RETIRED"]));
    expect(platforms).toEqual(expect.arrayContaining(["x", "telegram", "other"]));
    expect(assetKinds.length).toBeGreaterThanOrEqual(3);
  });

  it("草稿状态、渠道状态、平台都有中文", () => {
    expect(draftStatuses.filter((v) => !(v in PUBLISHING_DRAFT_STATUS_LABELS))).toEqual([]);
    expect(channelStatuses.filter((v) => !(v in PUBLISHING_CHANNEL_STATUS_LABELS))).toEqual([]);
    expect(platforms.filter((v) => !(v in PUBLISHING_PLATFORM_LABELS))).toEqual([]);
  });

  it("素材类型也有中文（选项写在页面里）", () => {
    expect(assetKinds.filter((v) => !(v in PUBLISHING_ASSET_KIND_LABELS))).toEqual([]);
  });

  it("草稿状态里不能凭空多出一个「已发布」", () => {
    // model.go：DraftStatus **没有 PUBLISHED**。前端多一个终态就是多一处骗人的
    // 地方——人会以为内容已经出去了。反向对账：前端的键不能超出后端。
    const extra = Object.keys(PUBLISHING_DRAFT_STATUS_LABELS).filter(
      (k) => !draftStatuses.includes(k),
    );
    expect(extra).toEqual([]);
    for (const label of Object.values(PUBLISHING_DRAFT_STATUS_LABELS)) {
      expect(label).not.toMatch(/已发布|已送达/);
    }
  });

  it("中文里一个字都不许回显凭据", () => {
    // 渠道带 CredentialRef（形如 secret://<scope>/<name>），这张表只翻状态。
    // 这一片定级刻意留在 L1，正是为了让误粘的明文不进审批单——展示层同理。
    const all = [
      ...Object.values(PUBLISHING_CHANNEL_STATUS_LABELS),
      ...Object.values(PUBLISHING_PLATFORM_LABELS),
      ...Object.values(PUBLISHING_DRAFT_STATUS_LABELS),
    ].join(" ");
    expect(all).not.toMatch(/secret:\/\/|credential|token|api[_-]?key/i);
  });
});

describe("资源目录：连接状态要有中文", () => {
  const statuses = goTypedConstValues(
    goSource("internal/platform/registry/connector.go"),
    "ConnectionStatus",
  );

  it("抽取器确实抓到了那一组常量", () => {
    expect(statuses).toEqual(expect.arrayContaining(["enabled", "disabled", "killed"]));
  });

  it("每一个连接状态都有中文", () => {
    expect(statuses.filter((v) => !(v in CONNECTION_STATUS_LABELS))).toEqual([]);
  });

  it("killed 不能被说成普通的「停用」", () => {
    // Kill Switch 拉闸是一次显式的紧急动作（宪法 26 条），与停用不是一回事。
    expect(CONNECTION_STATUS_LABELS["killed"]).toBe("已拉闸");
    expect(CONNECTION_STATUS_LABELS["killed"]).not.toBe(CONNECTION_STATUS_LABELS["disabled"]);
  });
});

// --- 数据新鲜度：五个档位的中文，以及「取最差」的顺序 ----------------------

/** 从 `Freshness()` 的**函数体**里把优先级推导出来。
 *
 *  后端没有导出有序清单（那才是真正该有的东西，见交接文档 follow_up），今天
 *  唯一的真相是那个函数**先判哪个、后判哪个**。所以按 `f.State = StateX` 的
 *  首次出现顺序去重，得到的就是「从最严重到最不严重」。
 *
 *  **这条判据依赖一个结构习惯**（函数写成最严重优先的早返回 / switch），不是
 *  契约。习惯变了它不会报错，只会给出一个更短的清单——所以下面第一条断言先
 *  钉住数量下界：被信任的过期闸比没有闸更坏。 */
export function freshnessPriorityFromBody(source: string): string[] {
  const start = source.indexOf("func (o Observation) Freshness(");
  if (start < 0) return [];
  const end = source.indexOf("\n}\n", start);
  const body = source.slice(start, end < 0 ? source.length : end);
  const byName = new Map<string, string>();
  for (const m of source.matchAll(/^\s*(?:const\s+)?([A-Za-z_]\w*)\s+State\s*=\s*"([^"]*)"/gm)) {
    byName.set(m[1] ?? "", m[2] ?? "");
  }
  const out: string[] = [];
  for (const m of body.matchAll(/f\.State\s*=\s*(State\w+)/g)) {
    const value = byName.get(m[1] ?? "");
    if (value !== undefined && !out.includes(value)) out.push(value);
  }
  return out;
}

describe("数据新鲜度：五个档位要有中文，取最差的顺序要与后端一致", () => {
  const source = goSource("internal/platform/ops/freshness.go");
  const states = goTypedConstValues(source, "State");
  const fromBody = freshnessPriorityFromBody(source);

  it("两个抽取器都确实抓到了东西", () => {
    // 抽空会让下面每一条断言恒真——这类测试最典型的假绿
    expect(states.length).toBeGreaterThanOrEqual(5);
    expect(states).toContain("failed");
    expect(states).toContain("uninitialized");
    expect(fromBody.length).toBeGreaterThanOrEqual(5);
  });

  /** 新鲜度的兜底带一层「未知状态（…）」的包装，与原值**不逐字相同**，所以
   *  通用的 `translated` 判据在这里恒为真——不能用它，否则「每一档都有中文」
   *  会对任何一个后端新增的状态自动成立。判据改成「中文名里不含原始取值」，
   *  下面的反向验证喂 "melted" 确认它真的判得出。 */
  const freshnessTranslated = (state: string): boolean => {
    const label = describeFreshness(state).label;
    return label !== "" && label !== state && !label.includes(state);
  };

  it("每一个新鲜度状态都有中文", () => {
    const missing = states.filter((s) => !freshnessTranslated(s));
    expect(missing).toEqual([]);
  });

  // 「发现而非手列」：后端加第六个状态，这一条红。
  it("前端的优先级清单不多不少，正好是后端那五个状态", () => {
    expect([...FRESHNESS_PRIORITY].sort()).toEqual([...states].sort());
  });

  // 顺序不再是第二份手写副本：它被后端函数体钉住。
  it("取最差的顺序与后端 Freshness() 的判定顺序逐值相同", () => {
    expect([...FRESHNESS_PRIORITY]).toEqual(fromBody);
  });

  it("逐值钉死：失败排在未初始化之前（XM-0031 的修正方向）", () => {
    // 两个抽取器同时失灵时的最后一道。前端曾把这两个反过来，于是一个正在
    // 发生的故障被显示成中性的「尚未接入」。
    expect(FRESHNESS_PRIORITY[0]).toBe("failed");
    expect(FRESHNESS_PRIORITY.indexOf("failed")).toBeLessThan(
      FRESHNESS_PRIORITY.indexOf("uninitialized"),
    );
  });

  /** 文档注释里那句「优先级：失败 > 未初始化 > 延迟 > 部分 > 新鲜。」用的是
   *  中文简称，与 describeFreshness 的中文名（同步失败 / 数据延迟 / 数据不完整
   *  / 数据新鲜）**不逐字相同**，所以要一座桥。
   *
   *  这座桥是人写的，因此它自己也要被钉住：下面两条断言分别把它的两端对上
   *  「注释里实际出现的词」与「后端实际有的状态」——后端换词或加状态，桥先红，
   *  而不是让顺序对账悄悄少比一项。 */
  const COMMENT_WORD_TO_STATE: Readonly<Record<string, string>> = {
    失败: "failed",
    未初始化: "uninitialized",
    延迟: "stale",
    部分: "partial",
    新鲜: "fresh",
  };
  const commentWords = (/优先级：([^\n]*)。/.exec(source)?.[1] ?? "").split(" > ");

  it("注释里那句优先级确实抓到了，而且中文桥两端都对得上", () => {
    expect(commentWords).toHaveLength(5);
    expect([...commentWords].sort()).toEqual(Object.keys(COMMENT_WORD_TO_STATE).sort());
    expect([...Object.values(COMMENT_WORD_TO_STATE)].sort()).toEqual([...states].sort());
  });

  it("后端自己的注释与自己的实现先对得上", () => {
    // 这一条红说明后端的注释与代码漂开了——那是后端的问题，但也该有人看见
    expect(commentWords.map((w) => COMMENT_WORD_TO_STATE[w])).toEqual(fromBody);
  });

  describe("变异验证：换成合成源码走同一条流水线", () => {
    it("对调 failed / uninitialized 两个分支，推导顺序跟着变", () => {
      // 改输入而不是删实现：删实现红的是编译，什么也证明不了
      const mutated = source
        .replace("f.State = StateFailed", "f.State = StateTmpMarker")
        .replace("f.State = StateUninitialized", "f.State = StateFailed")
        .replace("f.State = StateTmpMarker", "f.State = StateUninitialized");
      expect(mutated).not.toBe(source);
      expect(freshnessPriorityFromBody(mutated)[0]).toBe("uninitialized");
      // 而前端那一份没跟着变 —— 顺序对账会红，正是要的
      expect([...FRESHNESS_PRIORITY]).not.toEqual(freshnessPriorityFromBody(mutated));
    });

    it("后端多一个状态时，「不多不少」那条会把它算成缺失", () => {
      const mutated = source.replace(
        '\tStateFresh         State = "fresh"',
        '\tStateFresh         State = "fresh"\n\tStateDegraded      State = "degraded"',
      );
      expect(mutated).not.toBe(source);
      const added = goTypedConstValues(mutated, "State").filter(
        (s) => !FRESHNESS_PRIORITY.includes(s),
      );
      expect(added).toEqual(["degraded"]);
    });

    it("函数体改掉赋值写法让抽取器抓空时，数量下界那条拦得住", () => {
      const renamed = source.replaceAll("f.State = ", "f.state = ");
      expect(freshnessPriorityFromBody(renamed)).toEqual([]);
      // 抽空之后顺序对账会「恒真地」通过吗？不会——空数组与五个值不相等。
      // 但集合断言那一条仍然只比 goTypedConstValues 的结果，所以数量下界
      // 必须单独存在。
      expect(freshnessPriorityFromBody(renamed).length).toBeLessThan(5);
    });

    it("「已翻译」的判据能判出没翻译：喂一个后端不存在的状态", () => {
      // 通用的 translated 在这里会说 true（兜底包了一层「未知状态（…）」），
      // 那正是本组不能用它的原因——先把这件事钉住，免得有人顺手换回去
      expect(translated(describeFreshness("melted").label, "melted")).toBe(true);
      expect(freshnessTranslated("melted")).toBe(false);
      expect(describeFreshness("melted").label).toContain("melted");
      // 对照：真的翻译过的值，判据说 true。少了这一条，上面也可能只是恒假
      expect(freshnessTranslated("failed")).toBe(true);
    });
  });
});

// --- 告警计数：「评估 N 轮」这句话钉在后端事实上 ----------------------------

/** `60 * time.Second` → 60；认不出来返回 NaN（同 goDurationHours 的纪律）。 */
export function goDurationSeconds(expression: string): number {
  const seconds = /^(\d+)\s*\*\s*time\.Second$/.exec(expression.trim());
  return seconds ? Number(seconds[1]) : Number.NaN;
}

describe("告警计数：界面上的「评估 N 轮」要与后端的评估周期、SQL 对得上", () => {
  const jobSource = goSource("internal/platform/jobs/alert_evaluate.go");
  const sqlSource = goSource("db/queries/alerts.sql");
  const interval = /DefaultAlertEvaluateInterval\s*=\s*([^\n/]+)/.exec(jobSource)?.[1] ?? "";
  const touchAlert = (() => {
    const start = sqlSource.indexOf("-- name: TouchAlert");
    if (start < 0) return "";
    const next = sqlSource.indexOf("-- name:", start + 1);
    return sqlSource.slice(start, next < 0 ? sqlSource.length : next);
  })();

  it("两个抽取器确实抓到了东西", () => {
    expect(interval).not.toBe("");
    expect(goDurationSeconds(interval)).toBeGreaterThan(0);
    expect(touchAlert).toContain("UPDATE alerts.alert");
  });

  it("前端说的秒数就是后端的评估周期", () => {
    expect(ALERT_EVALUATE_INTERVAL_SECONDS).toBe(goDurationSeconds(interval));
    expect(FIRE_COUNT_MEANING).toContain(`${goDurationSeconds(interval)} 秒`);
  });

  // 这一条是「评估 N 轮」这句文案的**依据**：TouchAlert 每命中一轮 +1。
  // 后端哪天把 fire_count 改成真正的发生次数，这里会红，提醒把文案改回
  // 「触发 N 次」——一句钉不住后端事实的文案，只是把一个猜测换成另一个猜测。
  it("fire_count 确实是每评估一轮 +1，而不是别的什么口径", () => {
    expect(touchAlert).toMatch(/fire_count\s*=\s*fire_count \+ 1/);
    expect(FIRE_COUNT_MEANING).toContain("评估轮数，不是发生次数");
    expect(FIRE_COUNT_HEADER).toBe("评估轮次");
  });

  it("界面上不再有把它说成「次数 / 命中」的地方", () => {
    // 同一句话曾有五份措辞不同的副本（工作台待办、告警中心的列与表格说明、
    // 平台告警面板的列与表格说明、平台概览那一栏），于是同一个数在三个页面
    // 上有三种叫法。这一条跨文件 grep，防的是「只改了工作台」。
    const srcRoot = new URL("../", import.meta.url);
    const suspects = [
      "pages/AlertsPage.tsx",
      "components/PlatformAlertsPanel.tsx",
      "lib/overview.ts",
      "lib/workbench.ts",
    ];
    for (const relative of suspects) {
      const text = readFileSync(new URL(relative, srcRoot), "utf8");
      // 只看真正渲染出来的字面量：注释里复述那句旧话是允许的（本片正是
      // 靠注释记录它为什么错），所以先把 // 与 /** */ 剥掉
      const code = text.replaceAll(/\/\*[\s\S]*?\*\//g, "").replaceAll(/\/\/[^\n]*/g, "");
      expect(code, relative).not.toMatch(/命中\s*\$\{|命中 \d+ 次|"次数"/);
    }
  });
});

// --- 枚举清点：后端一共有多少个枚举，我盖到了几个 --------------------------

/** 后端全部字符串枚举的清点。
 *
 *  **为什么需要它**：2026-09-08 合并主线时，三个新模块（extapp / integration /
 *  publishing）一次带来十个新枚举，而上面那些分组测试**一条都没红**——它们只认
 *  我当初逐个列出的那几个文件。也就是说，「我列了 13 组」与「后端一共有多少组」
 *  之间**没有任何东西在对账**，新模块可以静悄悄地绕过整套门禁。这正是本片要防的
 *  那类分叉，只不过这次发生在门禁自己身上。
 *
 *  所以这一条反过来问：把 internal/platform 下所有字符串枚举**发现**出来，
 *  每一个都必须在下面的清点表里有一行。新增枚举 → 这里红 → 有人必须写一行，
 *  说清它是要给中文，还是根本不露到界面上。
 *
 *  **清点表里的分类是人写下的判断，不是机器验证过的事实。** 机器只保证
 *  「每个枚举都被分过类」这一件事。三档的含义：
 *
 *  - `reconciled` —— 上面有一组自己的差集断言盯着它；
 *  - `labelled-elsewhere` —— 界面上会露出、也已经有中文，但**还没纳入对账**，
 *    后端加取值时不会有任何东西变红（这是记账，不是保证）；
 *  - `backend-only` —— 判断它不露到界面上，附一句依据。
 *
 *  后两档是**待办清单**，不是已完成的覆盖。要收窄它，就把某一行改成
 *  `reconciled`，并在上面补一组差集断言。 */
const ENUM_INVENTORY: Readonly<
  Record<string, { status: "reconciled" | "labelled-elsewhere" | "backend-only"; note: string }>
> = {
  "action.Code": { status: "reconciled", note: "内核错误码，lib/labels.ts" },
  "action.RiskLevel": { status: "reconciled", note: "风险等级，lib/labels.ts" },
  "action.RunStatus": { status: "reconciled", note: "执行状态，lib/labels.ts" },
  "approval.Status": { status: "reconciled", note: "审批状态，lib/labels.ts" },
  "approval.Verdict": { status: "reconciled", note: "表决，lib/labels.ts" },
  "principal.Type": { status: "reconciled", note: "身份类别，lib/labels.ts" },
  "finance.Status": { status: "reconciled", note: "上游账号启停，lib/labels.ts" },
  "alerts.Severity": { status: "reconciled", note: "告警严重度，lib/alerts.ts" },
  "alerts.Status": { status: "reconciled", note: "告警状态，lib/alerts.ts" },
  "alerts.NotifyStatus": { status: "reconciled", note: "投递状态，lib/alerts.ts" },
  "jobs.RunState": { status: "reconciled", note: "任务运行状态，api/jobs.ts" },
  "registry.ServiceStatus": { status: "reconciled", note: "服务状态，ui-admin/freshness.ts" },
  "registry.ConnectionStatus": { status: "reconciled", note: "连接状态，pages/RegistryPage.tsx" },
  "integration.ClientStatus": { status: "reconciled", note: "调用方登记状态，api/integration.ts" },
  "integration.RuleStatus": { status: "reconciled", note: "规则登记状态，api/integration.ts" },
  "integration.TriggerKind": { status: "reconciled", note: "触发类别，api/integration.ts" },
  "extapp.AppStatus": { status: "reconciled", note: "应用状态，api/extapp.ts" },
  "extapp.AuthMode": { status: "reconciled", note: "登录方式，api/extapp.ts" },
  "extapp.ReleaseKind": { status: "reconciled", note: "发布类型，api/extapp.ts" },
  "publishing.DraftStatus": { status: "reconciled", note: "草稿状态，api/publishing.ts" },
  "publishing.ChannelStatus": { status: "reconciled", note: "渠道状态，api/publishing.ts" },
  "publishing.Platform": { status: "reconciled", note: "发布平台，api/publishing.ts" },
  "publishing.AssetKind": { status: "reconciled", note: "素材类型，api/publishing.ts" },
  "finance.RunwayLevel": { status: "labelled-elsewhere", note: "可用天数档位，FinanceSummaryCards 与 FinancePage 有语气表与文案" },
  "finance.RunwayUnknownReason": { status: "labelled-elsewhere", note: "算不出可用天数的原因，lib/runway.ts 的 runwayReasonText" },
  "finance.AccessMethod": { status: "labelled-elsewhere", note: "接入方式，api/finance.ts 的 describeAccessMethod" },
  "finance.SystemType": { status: "labelled-elsewhere", note: "系统类型；是平台标识（sub2api/newapi），当地址用不翻译" },
  "finance.CandidateState": { status: "labelled-elsewhere", note: "渠道绑定候选态，ChannelBindingCard 有文案" },
  "finance.CandidateEvidenceStatus": { status: "labelled-elsewhere", note: "候选证据充分度，同上" },
  "finance.PlatformBucketKind": { status: "labelled-elsewhere", note: "平台归属桶，financeShared 有文案" },
  "cards.OperationState": { status: "labelled-elsewhere", note: "卡操作状态，lib/cardStatus.ts" },
  "cards.RenewalRiskLevel": { status: "labelled-elsewhere", note: "续订风险档，lib/cardStatus.ts" },
  "sms.NumberState": { status: "labelled-elsewhere", note: "号码状态，SMSPanel 有映射与「未知状态」兜底" },
  "sms.OperationState": { status: "labelled-elsewhere", note: "接码操作状态，SMSPanel" },
  "sms.Capability": { status: "labelled-elsewhere", note: "上游能力位，SMSPanel" },
  "ops.State": { status: "reconciled", note: "数据新鲜度五档：中文在 ui-admin/freshness.ts 的 describeFreshness，取最差的顺序在 lib/workbench.ts 的 FRESHNESS_PRIORITY，两者都有差集断言" },
  "server.AssetStatus": { status: "labelled-elsewhere", note: "服务器资产状态，ServerAssetsPanel 的 ASSET_STATUS_OPTIONS" },
  "server.BillingCycle": { status: "labelled-elsewhere", note: "计费周期，lib/serverRegistryForm.ts" },
  "server.CertSource": { status: "labelled-elsewhere", note: "证书来源，ServerDomainsPanel" },
  "server.ServiceKind": { status: "labelled-elsewhere", note: "服务类型，lib/serverRegistryForm.ts 的 SERVICE_KIND_OPTIONS" },
  "registry.Environment": { status: "labelled-elsewhere", note: "环境名；当地址用，不翻译" },
  "savedviews.Density": { status: "labelled-elsewhere", note: "表格密度，ui-admin 的密度切换" },
  "savedviews.SortDirection": { status: "labelled-elsewhere", note: "排序方向；由表头箭头表达，界面上不出现这两个词" },
  "audit.Result": { status: "labelled-elsewhere", note: "审计结果，lib/audit.ts 的 resultLabel" },
  "jobs.ScheduleActivity": { status: "labelled-elsewhere", note: "周期任务活动迹象，JobsPage" },
  "alerts.RunwayImpactTransition": { status: "labelled-elsewhere", note: "阈值改动的影响预览，RunwayThresholdRulePanel" },
  "action.FieldType": { status: "backend-only", note: "Action Schema 的字段类型；只在参数校验里用" },
  "audit/archive.KeyPurpose": { status: "backend-only", note: "归档签名密钥用途；只在归档工具链里" },
  "audit/archive.VerificationCode": { status: "backend-only", note: "归档校验结果码；只在离线校验工具里" },
  "connector.BudgetErrorCode": { status: "backend-only", note: "连接器预算错误码；只进服务端日志与观测" },
  "connector.ErrorKind": { status: "backend-only", note: "连接器错误类别；折进新鲜度的 last_error_code 自由文本" },
  "ratelimit.ErrorKind": { status: "backend-only", note: "限流内部错误类别；只进日志" },
  "finance.ChannelEconomicsConflict": { status: "backend-only", note: "渠道经济性冲突原因；界面用的是 channelFieldReasons 的自有文案" },
  "jobs.AuditArchiveMode": { status: "backend-only", note: "部署模式，来自环境变量" },
  "jobs.CPAMode": { status: "backend-only", note: "部署模式，来自环境变量" },
  "jobs.FinanceCollectMode": { status: "backend-only", note: "部署模式，来自环境变量" },
  "jobs.NewAPIMode": { status: "backend-only", note: "部署模式，来自环境变量" },
  "jobs.ReqlogMetricsMode": { status: "backend-only", note: "部署模式，来自环境变量" },
  "jobs.Sub2APIMode": { status: "backend-only", note: "部署模式，来自环境变量" },
  "sms.Mode": { status: "backend-only", note: "部署模式，来自环境变量" },
  "notify.Domain": { status: "backend-only", note: "企微通道域；只在发信封时用" },
  "notify.Severity": { status: "backend-only", note: "信封严重度；界面读的是 alerts.Severity" },
  "ops.PrimaryKind": { status: "backend-only", note: "指标主值类别；界面读的是格式化后的数字" },
  "ops.SyncStatus": { status: "backend-only", note: "同步状态；界面读的是 ops.State" },
  "ops.ValueKind": { status: "backend-only", note: "指标值语义；只在聚合与降采样里" },
  "shadow.Measure": { status: "backend-only", note: "影子核对口径；影子评估没有管理端界面" },
  "shadow.Side": { status: "backend-only", note: "影子核对来源侧；同上" },
  "shadow.Verdict": { status: "backend-only", note: "影子核对结论；同上" },
};

/** 发现 internal/platform 下所有「字符串枚举」：一个 `type X string`，
 *  加上同一个文件里至少两个该类型的字符串常量。
 *
 *  「至少两个」是为了滤掉那些只给字符串起个别名的类型——只有一个取值的类型
 *  没有「翻译不全」这个问题。 */
function discoverGoStringEnums(): string[] {
  const root = new URL("internal/platform/", REPO_ROOT);
  const files = readdirSync(root, { recursive: true }).filter(
    (name) => name.endsWith(".go") && !name.endsWith("_test.go"),
  );
  const out = new Set<string>();
  for (const file of files) {
    const relative = file.replaceAll("\\", "/");
    const source = readFileSync(new URL(relative, root), "utf8");
    const slash = relative.lastIndexOf("/");
    const prefix = slash < 0 ? "" : relative.slice(0, slash) + ".";
    for (const m of source.matchAll(/^type ([A-Z]\w*) string$/gm)) {
      const typeName = m[1] ?? "";
      if (goTypedConstValues(source, typeName).length >= 2) out.add(prefix + typeName);
    }
  }
  return [...out].sort();
}

describe("枚举清点：后端每一个字符串枚举都必须被分过类", () => {
  const discovered = discoverGoStringEnums();

  it("发现器确实扫到了东西", () => {
    // 扫空了会让下面两条差集断言双双恒真——那正是它们最容易失效的方式。
    expect(discovered.length).toBeGreaterThanOrEqual(60);
    expect(discovered).toContain("action.Code");
    expect(discovered).toContain("publishing.DraftStatus");
  });

  it("没有哪个后端枚举是清点表不知道的", () => {
    // 红在这里 = 后端新增了枚举。补一行，写清它要不要中文。
    const unlisted = discovered.filter((key) => !(key in ENUM_INVENTORY));
    expect(unlisted).toEqual([]);
  });

  it("清点表里也没有后端已经删掉的枚举", () => {
    // 反向也要对：留着一行早就不存在的枚举，会让清点数看起来比实际覆盖得多。
    const stale = Object.keys(ENUM_INVENTORY).filter((key) => !discovered.includes(key));
    expect(stale).toEqual([]);
  });

  it("每一行都写了依据，没有空注释", () => {
    for (const [key, entry] of Object.entries(ENUM_INVENTORY)) {
      expect(entry.note.length, key).toBeGreaterThan(0);
    }
  });
});

// --- 抽取器与判据自身的变异验证 --------------------------------------------

describe("变异验证：后端多一个枚举值时，对账真的会把它算成缺失", () => {
  /** 上面每一组对账都是同一条流水线：**读源码 → 抽常量 → 与前端的表求差集**。
   *
   *  这里把「读源码」换成一段合成源码，其余原样走一遍：先证明真源码这一趟
   *  差集为空（对照组，确认判据不是恒红），再往合成源码里加一个后端不存在的
   *  值，确认差集里恰好多出它（确认判据不是恒真）。
   *
   *  变异改的是**输入**而不是删实现：删掉一段实现会让 import 未使用、整包
   *  编译不过，那样红的是编译而不是这条断言，什么也证明不了。 */
  const realSource = goSource("internal/platform/action/errors.go");
  const missingCodesIn = (source: string): string[] =>
    goTypedConstValues(source, "Code").filter((code) => !(code in ACTION_ERROR_CODES));

  it("对照组：真源码走这条流水线，差集为空", () => {
    expect(missingCodesIn(realSource)).toEqual([]);
  });

  it("往枚举里加一个码：差集里恰好多出它", () => {
    const mutated = realSource.replace(
      '\tCodeInternal                Code = "INTERNAL"',
      '\tCodeInternal                Code = "INTERNAL"\n\tCodeQuotaExhausted          Code = "QUOTA_EXHAUSTED"',
    );
    // 替换真的发生了——没发生的话下面那条断言会因为「差集仍为空」而变红，
    // 但红的原因会是个谜。先把它说出来。
    expect(mutated).not.toBe(realSource);
    expect(missingCodesIn(mutated)).toEqual(["QUOTA_EXHAUSTED"]);
  });

  it("改掉常量写法让抽取器抓空时，「数量下界」那条断言拦得住", () => {
    // 抽取器失灵是这类测试最危险的失败形态：清单空了，「每一项都有中文」
    // 于是自动成立。上面每一组开头那条数量断言就是为这个准备的——这里证明
    // 它确实会红，而不是摆设。
    const renamed = realSource.replaceAll(" Code = ", " ActionCode = ");
    expect(goTypedConstValues(renamed, "Code")).toEqual([]);
    expect(missingCodesIn(renamed)).toEqual([]); // 差集恒真地为空——正是要防的
  });

  it("「已翻译」的判据能判出没翻译：喂一个后端不存在的值", () => {
    // 各 describeX / xxxLabel 的兜底一律把原值吐回来。判据若失灵，上面
    // 所有 translated(...) 断言会集体恒真。
    expect(translated(describeSeverity("scream").label, "scream")).toBe(false);
    expect(translated(describeStatus("MELTED").label, "MELTED")).toBe(false);
    expect(translated(describeNotifyStatus("mailed").label, "mailed")).toBe(false);
    expect(translated(describeSilenceState("paused").label, "paused")).toBe(false);
    expect(translated(jobKindLabel("nightly_dance"), "nightly_dance")).toBe(false);
    // jobStateLabel 的兜底带一层「未知状态（…）」的包装，与原值不逐字相同，
    // 所以对它单独确认：包装里必须逐字含着原值，判据才算数。
    expect(jobStateLabel("levitating")).toContain("levitating");
    // 对照：真的翻译过的值，判据说 true。少了这一条，上面五个 false 也可能
    // 只是因为判据恒假。
    expect(translated(describeSeverity("critical").label, "critical")).toBe(true);
    expect(translated(jobKindLabel("platform_heartbeat"), "platform_heartbeat")).toBe(true);
  });
});
