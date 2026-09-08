/** 机器标识的中文对照表（XM-I18N-LABELS）。
 *
 *  管理后台里露给人看的机器标识——错误码、审批状态、风险等级、身份类别、
 *  HTTP 状态码——在这里统一给中文名。使用者读不懂英文码，而
 *  ADVANCED_CONTROLS_REQUIRED 这类东西直接摆在界面上等于没说。
 *
 *  三条纪律，改这个文件之前先读：
 *
 *  1. **中文在前，原码保留，不是替换。** 出问题时要拿原码去 grep 服务端
 *     日志、去跟开票线对接口。抹掉原码等于把排查能力也抹掉了。所以本模块
 *     所有对外函数都保证原码仍然逐字出现（见 withCode / errorCodeNote）。
 *  2. **认不出来的码原样显示，绝不猜。** 后端将来会加新码；映射表里没有的
 *     一律原样吐出来，不写「未知错误」——那会把一个有名有姓的问题变成没名字
 *     的问题；也不能靠字面相近给个近似的中文。
 *  3. **这里是唯一的一份。** 同一个事实钉在两处而没有门禁校验，迟早分叉
 *     （ApprovalQueue 曾自带一份审批状态映射，与 lib/approvals.ts 那份的
 *     「已批准」措辞已经不一致）。新增映射一律进本模块，并在
 *     labels.reconcile.test.ts 里补上对后端枚举的对账断言。
 */
import type { ApprovalStatus, ApprovalVerdict } from "../api/approvals";

/** 一条机器标识的中文对照。
 *
 *  `note` 是**限定语**，不是解释：它进括号、跟在中文名后面（如「已批准（待
 *  执行）」）。徽章那种寸土寸金的地方只摆 `label`，把 `note` 放进 title。
 *  `hint` 是完整的一句话，只进 title 或次级文字，不进主文案。 */
export interface Meaning {
  label: string;
  note?: string;
  hint: string;
  /** 「接下来该做什么」，**可见**的一行，不是悬停。
   *
   *  与 hint 分开是有讲究的：hint 解释「为什么会这样」，可以藏进 title；
   *  nextStep 是使用者的下一步动作，**藏起来等于没说**。只有在前端确实知道
   *  下一步、而后端消息又没说的时候才填——编一个「请联系管理员」比空着更糟。 */
  nextStep?: string;
}

/** 「中文（原码）」的统一写法：中文在前，原码逐字保留在括号里。
 *
 *  映射表里没有这个码时**只回原码**——不加「未知」二字，也不加括号：
 *  那一刻界面上唯一可信的事实就是这个码本身。 */
export function withCode(label: string | undefined, code: string): string {
  return label ? `${label}（${code}）` : code;
}

/** 带限定语的完整中文名，如「已批准（待执行）」；没有限定语时就是 label。 */
export function fullLabel(meaning: Meaning): string {
  return meaning.note ? `${meaning.label}（${meaning.note}）` : meaning.label;
}

// --- Action 错误码 ---------------------------------------------------------

/** 内核错误码（internal/platform/action/errors.go 的 Code）。
 *
 *  逐条对着后端的定义与注释写，不是照字面直译：ACTION_NOT_REGISTERED 在
 *  读路径上被当作通用的「资源不存在」用（后端注释里写明了这件事，以及它
 *  为什么没被改名），中文名必须把这个双重身份说出来——否则运营看到「动作
 *  未注册」会去查部署，而实际上只是他给的那个 id 不存在。
 *
 *  这份表由 labels.reconcile.test.ts 对着 errors.go 逐个校验：后端加了新码
 *  而这里没跟，那条测试变红。 */
export const ACTION_ERROR_CODES: Readonly<Record<string, Meaning>> = {
  INVALID_PARAMS: {
    label: "参数不合法",
    hint: "请求参数不符合该动作声明的 Schema：少了字段、类型不对，或多给了 Schema 里没有的字段。",
  },
  PERMISSION_DENIED: {
    label: "权限不足",
    hint: "调用者缺少这个动作要求的权限，或者身份根本没解析出来。服务端为最终裁决者。",
  },
  ENVIRONMENT_MISMATCH: {
    label: "环境不匹配",
    hint: "调用者身份属于另一个环境，平台不允许跨环境操作——生产权限不从测试继承。",
  },
  CONFLICT: {
    label: "状态冲突",
    hint: "目标对象此刻的状态不允许这次操作，多半是有人先改了一步。",
  },
  PRECONDITION_FAILED: {
    label: "前置条件不满足",
    hint: "参数指名的那个对象不存在，或者它还缺少这次操作要求的前置状态。",
  },
  ACTION_NOT_REGISTERED: {
    label: "动作未注册或资源不存在",
    hint: "写路径上表示这个 Action 没有注册；读路径上它被当作通用的「资源不存在」（HTTP 404）。两种含义共用一个码，见 action/errors.go 的注释。",
  },
  PRINCIPAL_TYPE_NOT_ALLOWED: {
    label: "身份类别不被允许",
    hint: "这个动作只接受特定的身份类别（多数只认 HUMAN），当前身份不在其列。补权限没有用。",
  },
  ADVANCED_CONTROLS_REQUIRED: {
    label: "需要高级管控",
    hint: "L2 及以上的动作必须走审批，而这套后端还没接上审批中心，于是一律拒绝执行（fail closed）。",
    // 中文名说的是**原因**，而后端那句原话是「需要 Action Advanced
    // Controls（Foundation-B / XM-0030）」——对着这句话，使用者不知道该干什么。
    //
    // 措辞与 api/approvals.ts 的 APPROVALS_NOT_MOUNTED_DESCRIPTION 同一口径：
    // 审批中心**已经**接入了，看到这个码不是「功能还没做」，而是这套后台连的
    // 后端版本旧了。把话说成「等排期」会让人一直等一个不会来的东西。
    nextStep:
      "审批中心自 XM-0030-ENABLE 起已在后端无条件启用，所以这通常不是「功能还没做」，" +
      "而是这套后台连的 platform-api 仍是启用之前的版本——请确认它已滚到含该变更的版本，而不是等排期。",
  },
  APPROVAL_REQUIRED: {
    label: "已受理为审批单",
    // 这一条**不是失败**。中文名必须说成受理而不是「需要审批」——后者听起来
    // 像被拦下了，而实际上单已经落好了，接下来该去「待审批」里等人批。
    hint: "这不是失败：调用被接受但还没有执行，内核落了一张待批审批单。批准之后要由人在「操作与审批 · 待审批」里点执行，动作才真的发生。",
  },
  APPROVAL_NOT_FOUND: {
    label: "审批单不存在",
    hint: "单号形态不对与确实不存在给的是同一个码——区分开等于给出一条探测单号空间的信道。",
  },
  EXECUTION_FAILED: {
    label: "执行失败",
    hint: "动作本身跑了但没跑成。根因不进对外文本，凭 request_id 查服务端日志。",
  },
  RUNWAY_CONFIG_UNAVAILABLE: {
    label: "可用天数阈值配置暂不可用",
    // runway 全站统一叫「可用天数」（api/finance.ts、ChannelsPanel、告警规则
    // 「上游可用天数不足」都是这个词）。不引入第二个说法——哪怕只是在括号里
    // 补一句「续航」，也是在给同一个东西起第二个名字。
    hint: "读不到可用天数的阈值配置，因此拒绝按一个猜出来的阈值作答。",
  },
  REVISION_CONFLICT: {
    label: "版本冲突",
    hint: "你读到这条记录之后已经有人改过它了（expected_version 与当前版本不一致）。刷新后基于最新版本重做。",
  },
  INTERNAL: {
    label: "服务端内部错误",
    hint: "服务端出了没有归类的错。根因绝不进对外文本，凭 request_id 查服务端日志。",
  },

  // --- 以下四个由前端自己造（api/client.ts），后端不会回这些码 ---------
  //
  // 它们同样是摆在人眼前的机器标识，所以同样要有中文；放在同一张表里是因为
  // 它们出现在**同一个位置**（错误条上的「错误码 X」），分两张表只会让调用点
  // 需要先知道「这个码是谁造的」才能取到中文——而那正是调用点不该关心的事。
  NETWORK_UNAVAILABLE: {
    label: "网络不可达",
    hint: "请求压根没到服务端（DNS、连接、CORS 或离线）。这与「服务端拒绝了」是两回事，处置方式也不同。",
  },
  BAD_RESPONSE: {
    label: "响应不是合法 JSON",
    hint: "HTTP 成功了，但响应体解析不出来。多半是中间有反代插了一页 HTML。",
  },
  UNAUTHENTICATED: {
    label: "未登录或登录已过期",
    hint: "本地没有可用的身份令牌，请求没有发出去。重新登录即可。",
  },
  UNKNOWN: {
    label: "响应里没有错误码",
    // **不叫「未知错误」**：那个说法与「映射表不认识这个码」的兜底撞车，而
    // 两者是完全不同的两件事。这个码有确切含义——服务端（或它前面的反代）
    // 没有按平台错误包的格式作答。
    hint: "服务端没有按平台错误包（error.code）的格式作答。路由根本没挂载时 chi 的纯文本 404 就长这样。",
  },
};

/** 错误码的中文对照；映射表里没有则返回 undefined（**不要**在这里兜底）。 */
export function actionErrorCode(code: string): Meaning | undefined {
  return ACTION_ERROR_CODES[code];
}

/** 错误条上那句「（……）」里的内容：中文在前，原码逐字保留。
 *
 *  认识的码 → `参数不合法，错误码 INVALID_PARAMS`
 *  不认识的码 → `错误码 SOME_NEW_CODE`（与本片之前的写法完全一致）
 *
 *  不认识时**不加任何中文**是有意的：加一句「未知错误」会让人以为平台不知道
 *  出了什么事，而事实是平台知道得很清楚，只是这套前端还没跟上后端的新码。 */
export function errorCodeNote(code: string): string {
  const meaning = actionErrorCode(code);
  return meaning ? `${meaning.label}，错误码 ${code}` : `错误码 ${code}`;
}

/** 悬停解释；不认识的码返回空串（调用点据此不挂 title）。 */
export function errorCodeHint(code: string): string {
  return actionErrorCode(code)?.hint ?? "";
}

/** 可见的「下一步」；没有就返回空串——调用点据此**不渲染那一行**，
 *  而不是渲染一个空段落或者一句「请联系管理员」。 */
export function errorCodeNextStep(code: string): string {
  return actionErrorCode(code)?.nextStep ?? "";
}

// --- 审批状态与表决 --------------------------------------------------------

/** 审批单状态（internal/platform/approval/approval.go 的 Status）。
 *
 *  **本仓库唯一的一份**。ApprovalQueue 的筛选下拉曾自带一份，与这里的措辞
 *  已经分叉（那边「已批准（待执行）」，这边「已批准」）——限定语现在由
 *  note 承载，两处渲染同一条记录。 */
export const APPROVAL_STATUSES: Readonly<Record<ApprovalStatus, Meaning>> = {
  PENDING: {
    label: "待审批",
    hint: "已经落单，等人投票。票数够了才会转为已批准。",
  },
  APPROVED: {
    label: "已批准",
    note: "待执行",
    // 限定语不能省：批准与执行是两件事，而「已批准」三个字很容易被读成
    // 「这件事做完了」。动作此刻还没有发生。
    hint: "票数已满足，但动作还没有发生——要在「待审批」这一页由人点「执行」才真的跑。",
  },
  REJECTED: {
    label: "已驳回",
    hint: "有人投了反对票。审批是「所有人都同意」，一票反对即驳回，改主意要重新提交。",
  },
  EXECUTED: {
    label: "已执行",
    hint: "动作已经按单上冻结的参数跑过了。一单最多一跑，失败也不回到可重跑状态。",
  },
  EXPIRED: {
    label: "已过期",
    hint: "超过有效期或批准后的执行窗口。过期的单一票也不能投，更不能执行。",
  },
  CANCELLED: {
    label: "已撤回",
    hint: "提交人自己撤回了。只有提交人能撤回自己的单。",
  },
};

/** 一票的内容（approval.go 的 Verdict）。 */
export const APPROVAL_VERDICTS: Readonly<Record<ApprovalVerdict, Meaning>> = {
  APPROVE: { label: "同意", hint: "APPROVE：这一票赞成执行。" },
  REJECT: { label: "驳回", hint: "REJECT：一票驳回即整单驳回。" },
};

/** 一次 Action 执行的最终状态（internal/platform/action/kernel.go 的 RunStatus）。
 *
 *  今天只有两个值，界面上一直是 `status === "succeeded" ? "成功" : "失败"`。
 *  那个三元式在**加第三个值的那一天会把它悄悄显示成「失败」**——不是翻译问题，
 *  是把一个新事实说成一个旧的假事实。改成查表，认不出来的原样显示，
 *  再由 labels.reconcile.test.ts 盯着 kernel.go：加了值而这里没跟就变红。 */
export const ACTION_RUN_STATUSES: Readonly<Record<string, Meaning>> = {
  succeeded: { label: "成功", hint: "Handler 跑完了，且没有返回错误。" },
  failed: { label: "失败", hint: "Handler 跑了但返回了错误；错误码在同一格里。" },
};

export function actionRunStatus(status: string): Meaning | undefined {
  return ACTION_RUN_STATUSES[status];
}

// --- 风险等级 --------------------------------------------------------------

/** 一个风险等级说明了什么。
 *
 *  光一个「L3」说明不了任何事。人要知道的是**这一级会发生什么**：要几票、
 *  能不能自己批、单子多久过期。这些事实的唯一来源是
 *  approval.DefaultPolicy()（internal/platform/approval/approval.go），
 *  由 labels.reconcile.test.ts 逐条对账——**不要在这里手写一个记忆里的数字**。 */
export interface RiskLevelMeaning extends Meaning {
  /** 典型场景，抄自 action/risk.go 每个常量后面的行尾注释。 */
  examples: string;
  /** 需要几票；L0/L1 不走审批，为 0。 */
  votesRequired: number;
  /** 提交人能不能给自己的单投票；不走审批的等级无意义，为 false。 */
  selfApprovalAllowed: boolean;
  /** 是否必须有一张 approval.l4 特权票。 */
  privilegedVoteRequired: boolean;
  /** 审批单有效期（小时）；不走审批的等级为 0。 */
  ttlHours: number;
  /** 该等级是否需要先落审批单（action/risk.go 的 RequiresAdvancedControls）。 */
  requiresApproval: boolean;
}

export const RISK_LEVELS: Readonly<Record<string, RiskLevelMeaning>> = {
  L0: {
    label: "最低风险",
    examples: "保存个人视图、低影响偏好",
    hint: "直接执行，不经过审批。控制手段是权限加基础审计。",
    votesRequired: 0,
    selfApprovalAllowed: false,
    privilegedVoteRequired: false,
    ttlHours: 0,
    requiresApproval: false,
  },
  L1: {
    label: "低风险",
    examples: "修改低风险平台配置、确认普通告警",
    hint: "直接执行，不经过审批。控制手段是权限、幂等与完整审计。",
    votesRequired: 0,
    selfApprovalAllowed: false,
    privilegedVoteRequired: false,
    ttlHours: 0,
    requiresApproval: false,
  },
  L2: {
    label: "中风险",
    examples: "批量配置、启停低风险资源",
    hint: "先落审批单：需 1 票，提交人可以给自己的单投票（单票即自批，等级本意如此），单子 24 小时内有效。",
    votesRequired: 1,
    selfApprovalAllowed: true,
    privilegedVoteRequired: false,
    ttlHours: 24,
    requiresApproval: true,
  },
  L3: {
    label: "高风险",
    examples: "服务切换、账号批量导入、敏感配置",
    hint: "先落审批单：需 2 票，不允许提交人自批，单子 24 小时内有效。",
    votesRequired: 2,
    selfApprovalAllowed: false,
    privilegedVoteRequired: false,
    ttlHours: 24,
    requiresApproval: true,
  },
  L4: {
    label: "最高风险",
    examples: "退款、生产基础设施高影响动作、开票关键动作",
    hint: "先落审批单：需 2 票且其中至少一张须由 approval.l4 持有人投出，不允许提交人自批，单子 4 小时内有效。",
    votesRequired: 2,
    selfApprovalAllowed: false,
    privilegedVoteRequired: true,
    ttlHours: 4,
    requiresApproval: true,
  },
};

/** 风险等级的中文对照；不认识的等级返回 undefined。
 *
 *  等级表以后可能加档（groupByRisk 已经为此留了路）。认不出来时调用点必须
 *  原样显示那个等级串，绝不能兜底成某个已知等级——那会把一个更危险的新等级
 *  显示成一个较轻的。 */
export function riskLevel(level: string): RiskLevelMeaning | undefined {
  return RISK_LEVELS[level];
}

/** 徽章旁边那句话：`L3 高风险`。不认识的等级只回原串。 */
export function riskLevelText(level: string): string {
  const meaning = riskLevel(level);
  return meaning ? `${level} ${meaning.label}` : level;
}

/** 上游账号登记簿的启停状态（internal/platform/finance/account.go 的 Status）。
 *
 *  只有两个值，而「disabled」在这里**不是**「坏了」——它是采集侧的 Kill
 *  Switch（宪法 26 条）：停掉之后不再打上游，历史台账保持不动。中文名要说
 *  出这个区别，否则运营会把一次有意的停采当成故障去排查。 */
export const UPSTREAM_ACCOUNT_STATUSES: Readonly<Record<string, Meaning>> = {
  active: { label: "在采", hint: "采集任务会继续读这个账号的余额与消耗。" },
  disabled: {
    label: "已停采",
    hint: "采集侧的 Kill Switch：不再打这个上游，历史台账保持不动。这是有意停的，不是故障。",
  },
};

/** 登记状态的中文（原码）；不认识的状态原样返回。 */
export function upstreamAccountStatusText(status: string): string {
  return withCode(UPSTREAM_ACCOUNT_STATUSES[status]?.label, status);
}

export function upstreamAccountStatusHint(status: string): string {
  return UPSTREAM_ACCOUNT_STATUSES[status]?.hint ?? "";
}

// --- 身份类别 --------------------------------------------------------------

/** 身份类别（internal/platform/principal/principal.go 的 Type）。 */
export const PRINCIPAL_TYPES: Readonly<Record<string, Meaning>> = {
  HUMAN: {
    label: "人",
    hint: "HUMAN：真人账号。审批只认这一类（宪法 10 条：AI 不作为审批人）。",
  },
  SERVICE: {
    label: "服务",
    hint: "SERVICE：平台内部或对接系统的机器身份，不复用人类账号。",
  },
  AI: { label: "AI", hint: "AI：AI 工具的机器身份。不能投审批票。" },
  SERVER_AGENT: { label: "服务器代理", hint: "SERVER_AGENT：装在服务器上的代理进程身份。" },
};

/** 身份类别的中文（原码）；不认识的类别原样返回。 */
export function principalTypeText(type: string): string {
  return withCode(PRINCIPAL_TYPES[type]?.label, type);
}

export function principalTypeHint(type: string): string {
  return PRINCIPAL_TYPES[type]?.hint ?? "";
}

// --- HTTP 状态码 -----------------------------------------------------------

/** 界面上真的会露出来的 HTTP 状态码。
 *
 *  只收平台自己会产生的那些（httpapi/response.go 的 StatusForCode，加上
 *  鉴权层的 401 与前端的伪状态 0），不抄一份完整的 RFC 状态码表：抄来的
 *  那些永远不会出现，只会让人以为界面见过它们。 */
export const HTTP_STATUSES: Readonly<Record<number, Meaning>> = {
  0: { label: "网络不可达", hint: "前端造的伪状态码：请求压根没到服务端。" },
  202: { label: "已受理，尚未执行", hint: "调用被接受并落成了审批单，动作还没有发生。" },
  400: { label: "请求不合法", hint: "参数没通过校验。" },
  401: { label: "未登录", hint: "没有身份令牌，或令牌已经过期。" },
  403: { label: "无权限", hint: "身份认出来了，但缺少这次操作要求的权限。" },
  404: { label: "未找到", hint: "地址或资源不存在；也可能是这组端点在本环境根本没挂载。" },
  409: { label: "状态冲突", hint: "跨环境操作，或目标对象此刻的状态不允许这次操作。" },
  412: { label: "前置条件不满足", hint: "参数指名的对象不存在或缺少前置状态。" },
  500: { label: "服务端内部错误", hint: "服务端出了没有归类的错。" },
  501: { label: "功能尚未上线", hint: "平台还没有实现这一级风险要求的控制手段。" },
  502: { label: "上游执行失败", hint: "动作跑了但没跑成，多半卡在被操作的那个系统上。" },
  503: { label: "暂不可用", hint: "依赖的配置或服务此刻取不到，服务端拒绝按猜测作答。" },
};

/** `HTTP 404 未找到`：状态码在前（它本身就是人尽皆知的数字），中文补在后面。
 *
 *  这一处**不套 withCode**：状态码是数字不是英文词，读不读得懂英文与它无关，
 *  真正需要补的是「404 在这一页意味着什么」。 */
export function httpStatusText(status: number): string {
  const meaning = HTTP_STATUSES[status];
  return meaning ? `HTTP ${status} ${meaning.label}` : `HTTP ${status}`;
}
