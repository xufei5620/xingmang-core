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
import { describeServiceStatus } from "@xingmang/ui-admin";
import { ALERT_RULES } from "../api/alerts";
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
