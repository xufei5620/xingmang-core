import type { BlueprintPage } from "./types";

/** 治理段的蓝图规格（UI 第 6 片）。
 *
 *  五页：人员与权限、跨平台财务、运行保障、版本与发布、界面规范。
 *  子页签集合、命名与顺序来自 ui-admin/navigation.ts 的 GOVERNANCE_NAV_ITEMS
 *  （ADMIN-IA 的可执行副本）；本文件只补每一格的**内容结构**。
 *  两处必须对得上，`governance.test.ts` 会逐条断言。
 *
 *  数据来源：原型渲染态 `V["gov/*"]` 的**最后一次赋值**——那几个键被赋值两次，
 *  早期那版是旧 IA 的遗留（比如「资源目录」旧名叫「注册表」），照抄会得到
 *  一份已经被废弃的设计。
 *
 *  **列头与文案逐字抄，样例数字一个不抄**（理由见 types.ts）。
 *
 *  ⚠️ 原型渲染态里有几处**补丁误伤**的文案（build_page() 做全局字符串替换时
 *  连变量名和部分可见文案一起换了），本文件已按人读得通的写法修正：
 *    原型 `活跃 数据连接器（Connector）` → 这里 `活跃连接器`
 *    原型 `最近 检测`                   → 这里 `最近检测`
 *    原型 `Secret供应方`                → 这里 `Secret 供应方`
 *  其余术语（密钥引用（CredentialRef）、稳定性目标（SLO）、故障处理手册（Runbook）、
 *  数据同步位置（Watermark）、二次验证（MFA））是**有意的术语表替换**，逐字保留。 */

/** 治理段各页共用的控制台红线提示（原型的 `controlUiBanner()`）。 */
const CONTROL_BANNER =
  "仅 UI 设计 · 高风险能力统一显示为「暂未开放」，本页不执行任何真实操作。";

export const IDENTITY_BLUEPRINT: BlueprintPage = {
  description:
    "统一管理人员账号、程序身份、AI 身份和服务器采集身份，以及权限规则、密钥引用和登录会话。",
  banner: CONTROL_BANNER,
  tiles: [
    { label: "人员账号", note: "员工域下的自然人账号数" },
    { label: "程序与设备身份", note: "服务、Agent 与 AI 身份合计" },
    { label: "高风险权限", note: "L3 及以上、需人工审批的授权数" },
    { label: "异常密钥引用", note: "临期、需轮换或验证失败的引用数" },
  ],
  tabs: [
    {
      id: "accounts",
      label: "账号与身份",
      source:
        "账号来自 Keycloak（员工域）与平台自己的机器身份登记；随 F-A 的身份工作台上线。",
      filters: ["搜索账号 / 账号来源 / 权限", "类型：全部", "环境：全部", "状态：全部"],
      tables: [
        {
          caption: "账号与身份",
          columns: [
            "主体",
            "类型",
            "账号来源",
            "环境",
            "权限数量",
            "登录验证方式",
            "状态",
            "最后活动",
            "详情",
          ],
          source: "账号目录随 F-A 上线。",
        },
      ],
    },
    {
      id: "rules",
      label: "权限规则",
      source: "权限规则是平台自己的授权策略（ALLOW / DENY + 资源范围 + 风险门槛），随 F-A 上线。",
      tables: [
        {
          caption: "权限规则",
          columns: ["权限规则", "效果", "资源范围", "环境", "风险门槛", "优先级", "状态"],
          source: "授权策略随 F-A 上线。",
        },
      ],
      notes: [
        {
          title: "AI 不作为 L3/L4 第二审批人",
          body:
            "规则集里有一条恒定的 DENY：AI 身份不得成为高风险操作的第二审批人（宪法 28 条）。它不是可配置项，不会出现在可编辑的规则列表里。",
        },
      ],
    },
    {
      id: "scopes",
      label: "权限范围",
      source: "角色集合 × 平台资源的读写矩阵，取自 RoleScopeMap；随 F-A 上线。",
      tables: [
        {
          caption: "权限范围矩阵",
          columns: ["角色集合", "平台 / 资源", "环境", "读取", "受控操作", "审批", "审计"],
          source: "权限矩阵随 F-A 上线。",
        },
      ],
    },
    {
      id: "credentials",
      label: "密钥引用",
      source:
        "只展示引用本身与它的供应方、消费者、轮换状态。**明文永不出现在任何页面响应里**（宪法 7 条）。",
      filters: [
        "搜索 密钥引用（CredentialRef） / 消费者",
        "供应方：全部",
        "环境：全部",
        "状态：全部",
      ],
      tables: [
        {
          caption: "密钥引用",
          columns: [
            "密钥引用（CredentialRef）",
            "供应方",
            "消费者",
            "环境",
            "用途",
            "最近使用",
            "轮换 / 到期",
            "状态",
          ],
          source: "凭据登记随 F-A 上线；状态一律带「不可查看」后缀。",
        },
      ],
    },
    {
      id: "sessions",
      label: "会话",
      source: "登录会话来自 Keycloak 与平台自身的会话表；随 F-A 上线。",
      tables: [
        {
          caption: "登录会话",
          columns: ["主体", "会话 / Client", "登录验证方式", "来源 IP", "环境", "最后活动", "状态"],
          source: "会话列表随 F-A 上线。",
        },
      ],
    },
  ],
};

export const FINANCE_BLUEPRINT: BlueprintPage = {
  description:
    "按平台保留原始资金事实，全局页面只做聚合、对账、异常协同和开票集成状态。",
  banner: CONTROL_BANNER,
  tiles: [
    { label: "现金到账", note: "真实支付现金，不含赠额" },
    { label: "对账差异", note: "平台金额与支付金额的差额合计" },
    { label: "退款 / 冻结", note: "已退款现金与冻结金额" },
    { label: "开票数据延迟", note: "开票集成的数据新鲜度" },
  ],
  tabs: [
    {
      id: "overview",
      label: "财务总览",
      source:
        "各平台的资金事实由各自的「支付与财务」页保留，这一页只做聚合（ADR-006 统一体验）。随 M3 的支付接入上线。",
      cards: [
        {
          title: "平台资金构成",
          hint: "按平台拆分现金、赠额、退款与差异",
          keys: ["Sub2API", "NewAPI", "退款", "差异"],
        },
        {
          title: "需要处理",
          hint: "对账差异、契约状态与待审批合并",
          keys: ["事项", "来源", "金额 / 影响", "状态"],
        },
      ],
    },
    {
      id: "channels",
      label: "支付通道",
      source: "支付通道的健康与费率随 M3 的支付接入上线。",
      tables: [
        {
          caption: "支付通道",
          columns: [
            "支付通道",
            "所属平台",
            "健康",
            "24h 成功率",
            "结算币种",
            "费率",
            "最近结算",
            "数据状态",
          ],
          source: "支付通道随 M3 上线。",
        },
      ],
    },
    {
      id: "reconciliation",
      label: "财务对账",
      source: "对账批次比对平台金额与支付金额；差异不自动抹平，逐条要求解释。随 M3 上线。",
      filters: ["搜索对账批次 / 订单 / 来源", "平台：全部", "差异：待处理", "币种：全部"],
      tables: [
        {
          caption: "对账批次",
          columns: [
            "对账批次",
            "日期",
            "来源系统",
            "平台金额",
            "支付金额",
            "差异",
            "币种",
            "状态",
            "新鲜度",
            "详情",
          ],
          source: "对账批次随 M3 上线。",
        },
      ],
    },
    {
      id: "exceptions",
      label: "异常与冻结",
      source: "退款冻结、订单状态不一致等异常的协同处理；写操作走 Action，随 F-B 的审批链上线。",
      tables: [
        {
          caption: "财务异常",
          columns: [
            "异常",
            "类型",
            "来源",
            "影响金额",
            "负责人",
            "关联审批",
            "状态",
            "更新时间",
            "详情",
          ],
          source: "异常协同随 M3 上线；退款与补单的执行入口在 Foundation-B 之后才会出现。",
        },
      ],
    },
    {
      id: "invoicing",
      label: "开票集成",
      source:
        "开票系统是 Codex 线的独立服务，平台只保存**引用**不复制 PDF。契约变更走 docs/change-requests/。",
      cards: [
        {
          title: "集成契约状态",
          hint: "只读集成契约状态",
          keys: ["健康", "来源流", "待处理队列", "最近失败", "observed_at"],
        },
        {
          title: "文件引用原则",
          hint: "平台不复制 PDF",
          keys: [
            "source_document_id",
            "document_hash",
            "source_system",
            "备份所有者",
            "平台状态",
          ],
        },
      ],
    },
    {
      id: "settings",
      label: "财务配置",
      source: "金额语义与写入功能的启用条件；写能力随 Foundation-B 解锁。",
      cards: [
        {
          title: "金额语义",
          hint: "四个金额字段各自的定义",
          keys: ["cash_paid", "bonus_granted", "cash_refunded", "invoice_eligible"],
        },
        {
          title: "写入功能启用条件",
          hint: "锁定项在条件满足前不出现执行入口",
          keys: ["退款", "补单", "对账纠正"],
        },
      ],
    },
  ],
};

export const OPS_BLUEPRINT: BlueprintPage = {
  description:
    "查看控制平面自身健康、稳定性目标（SLO）、看门狗、备份恢复、故障处理手册（Runbook）、迁移对账和跨渠道保障证据。",
  banner: CONTROL_BANNER,
  tiles: [
    { label: "控制平面健康", note: "健康组件数 / 应有组件数" },
    { label: "稳定性目标（SLO） 风险", note: "接近或已违约的 SLI 数" },
    { label: "外部监控", note: "异故障域探测端点的异常数" },
    { label: "备份恢复风险", note: "演练临期或未演练的备份对象数" },
  ],
  tabs: [
    {
      id: "health",
      label: "控制平面健康",
      source:
        "平台自己的组件健康（platform-api / platform-worker / Secret 供应方 等）；随 M1 的自监控上线。",
      tables: [
        {
          caption: "控制平面组件健康",
          columns: ["组件", "环境", "状态", "数据新鲜度", "最近心跳", "依赖", "最近错误"],
          source: "控制平面自监控随 M1 上线。",
        },
      ],
    },
    {
      id: "stability",
      label: "稳定性与外部监控",
      source: "SLI / SLO 与异故障域的外部探测（Uptime Kuma）；随 M1 上线。",
      tables: [
        {
          caption: "稳定性目标",
          columns: [
            "SLI / 稳定性目标（SLO）",
            "当前值",
            "目标",
            "燃尽 / 违约",
            "探测来源",
            "最近检查",
            "状态",
          ],
          source: "SLO 基线随 M1 上线；基线未定前不显示假的达标率。",
        },
      ],
    },
    {
      id: "backup",
      label: "备份与恢复",
      source: "备份对象、异地副本与恢复演练记录；恢复演练属于 Platform Lifecycle Operation。",
      tables: [
        {
          caption: "备份与恢复",
          columns: [
            "备份对象",
            "所有者",
            "最近备份",
            "异地副本",
            "候选 RPO",
            "最近恢复演练",
            "状态",
            "详情",
          ],
          source: "备份登记随 M1 上线。",
        },
      ],
    },
    {
      id: "runbook",
      label: "故障处理手册（Runbook）",
      source: "手册本体在仓库里，这一页只登记版本、演练记录与关联告警。",
      tables: [
        {
          caption: "故障处理手册",
          columns: [
            "故障处理手册（Runbook）",
            "场景",
            "负责人",
            "版本",
            "最近演练",
            "关联告警",
            "状态",
          ],
          source: "手册登记随 M1 上线。",
        },
      ],
    },
    {
      id: "migration",
      label: "迁移与数据对比",
      source:
        "SoloAI → 星芒的影子对比进度。判据由 cmd/platform-shadow 产出并归档在 docs/shadow-reports/（XM-0037e）。",
      tables: [
        {
          caption: "迁移与数据对比",
          columns: [
            "对象",
            "主源 / 影子源",
            "指标",
            "差异",
            "数据同步位置（Watermark）",
            "连续通过",
            "切换状态",
          ],
          source:
            "影子对比工具已就绪（XM-0037e），这一页读它的归档报告；接线随 M1 上线。",
        },
      ],
    },
    {
      id: "model-quality",
      label: "模型质量保障",
      source: "上游模型的身份一致性与漂移检测证据；随 M1.5 的渠道保障上线。",
      filters: ["搜索 供应方 / 模型 / 证据", "结论：全部", "漂移：全部", "预算：全部"],
      tables: [
        {
          caption: "模型质量保障",
          columns: [
            "供应方 / 模型",
            "声明 / 参考",
            "最近检测",
            "健康",
            "身份一致性",
            "预算",
            "漂移",
            "结论",
            "详情",
          ],
          source: "模型检测随 M1.5 上线；结论一律是概率判断，不写成断言。",
        },
      ],
    },
  ],
};

export const CHANGES_BLUEPRINT: BlueprintPage = {
  description:
    "管理变更单、发布、CI、发布包供应链和数据库生命周期操作；审批执行仍在受控操作中心统一查看。",
  banner: CONTROL_BANNER,
  tiles: [
    { label: "待评审变更", note: "已提交、等待评审的变更单数" },
    { label: "等待生产批准", note: "已通过评审、等待人工批准的发布数" },
    { label: "自动测试失败", note: "最近一轮 CI 失败的流水线数" },
    { label: "供应链异常", note: "签名、SBOM 或漏洞检查未通过的发布包数" },
  ],
  tabs: [
    {
      id: "requests",
      label: "变更单",
      source: "变更单与审批链随 Foundation-B（XM-0030）上线。",
      filters: ["搜索变更单 / 资源 / 负责人", "类型：全部", "环境：全部", "状态：全部"],
      tables: [
        {
          caption: "变更单",
          columns: [
            "变更单",
            "标题",
            "影响资源",
            "环境",
            "阶段",
            "审批",
            "验证证据",
            "状态",
            "详情",
          ],
          source: "变更单随 Foundation-B 上线。",
        },
      ],
    },
    {
      id: "releases",
      label: "发布与回滚",
      source: "发布与回滚记录随 Foundation-B 上线；执行入口在审批链就绪之前不会出现。",
      filters: ["搜索发布 / Task / PR / Commit", "环境：全部", "状态：全部"],
      tables: [
        {
          caption: "发布与回滚",
          columns: [
            "发布",
            "版本 / 发布包",
            "环境",
            "Task / PR",
            "自动测试",
            "审批",
            "计划时间",
            "状态",
            "详情",
          ],
          source: "发布记录随 Foundation-B 上线。",
        },
      ],
    },
    {
      id: "tests",
      label: "自动测试与质量",
      source: "CI 流水线结果来自 GitHub Actions；接线随 Foundation-B 上线。",
      tables: [
        {
          caption: "自动测试与质量",
          columns: ["流水线", "Commit", "检查", "通过 / 总数", "失败阶段", "耗时", "状态"],
          source: "CI 接线随 Foundation-B 上线。",
        },
      ],
    },
    {
      id: "packages",
      label: "发布包与安全检查",
      source: "镜像摘要、SBOM、签名与漏洞检查；接线随 Foundation-B 上线。",
      tables: [
        {
          caption: "发布包与安全检查",
          columns: [
            "发布包",
            "Digest",
            "SBOM",
            "签名",
            "漏洞",
            "License",
            "来源",
            "状态",
          ],
          source: "供应链检查随 Foundation-B 上线。",
        },
      ],
    },
    {
      id: "database",
      label: "数据库变更",
      source:
        "迁移属于 Platform Lifecycle Operation（宪法 2、3 条）：走版本化脚本 + 变更单 + 人工批准，不经 Action 通道。",
      tables: [
        {
          caption: "数据库变更",
          columns: [
            "变更",
            "Schema / 版本",
            "环境",
            "迁移前备份",
            "锁评估",
            "验证",
            "回退路径",
            "状态",
          ],
          source: "迁移登记随 Foundation-B 上线。",
        },
      ],
    },
  ],
};

export const DESIGN_BLUEPRINT: BlueprintPage = {
  description:
    "统一控制台的颜色、排版、组件状态和复杂业务模块展示；所有示例只用于规范说明。",
  // 这一页刻意**不带**控制台红线横幅：它不展示任何业务数据，也不涉及任何操作，
  // 挂一条「不执行真实操作」的提示反而是噪音（原型同样没给它挂）。
  tabs: [
    {
      id: "color",
      label: "颜色与排版",
      source:
        "颜色与字号的**唯一权威**是 @xingmang/design-tokens，不是这一页的文字描述。这一页解释它们各自用在哪儿。",
      cards: [
        {
          title: "核心颜色",
          hint: "基础色 → 语义色 → 组件色",
          keys: ["交互主色", "页面底色", "内容表面", "正常", "提醒", "危险"],
        },
        {
          title: "字体层级",
          hint: "IBM Plex Sans / Mono",
          keys: ["页面标题", "卡片标题", "正文内容", "辅助文字", "数字与时间"],
        },
        {
          title: "间距与圆角",
          hint: "4px 基准网格",
          keys: ["间距刻度", "按钮圆角", "卡片圆角", "行内详情圆角", "状态标签"],
        },
      ],
      notes: [
        {
          title: "前端红线：禁止硬编码颜色、圆角与阴影",
          body:
            "这一页是规范的说明面，Storybook 是它的可交互面，design-tokens 是它的实现。三者同源——在这里改一个色值不会生效，要改 tokens。",
        },
      ],
    },
    {
      id: "controls",
      label: "按钮与表单",
      source: "按钮与表单的状态样例见 Storybook 的 ui-primitives 故事；这一页只说明各状态的用途。",
      cards: [
        {
          title: "按钮",
          hint: "默认 / 悬停 / 聚焦 / 禁用",
          keys: ["主要按钮", "次要按钮", "边框按钮", "危险按钮", "暂未开放", "加载中"],
        },
        {
          title: "表单",
          hint: "标签、说明和校验不可缺失",
          keys: ["默认输入", "选择状态", "错误状态", "不可编辑"],
        },
      ],
    },
    {
      id: "cards",
      label: "卡片与状态",
      source: "卡片、数据新鲜度、风险等级与运行环境标签的用法；组件在 ui-admin 与 ui-primitives。",
      cards: [
        { title: "标准卡片", hint: "无点击行为时不显示箭头", keys: ["用途", "悬停行为"] },
        { title: "可点击卡片", hint: "核心对象进入完整详情页面", keys: ["用途", "跳转目标"] },
        {
          title: "数据新鲜度标签",
          hint: "任何数字都要能回答「什么时候的」",
          keys: ["刚刚更新", "延迟", "数据已过期"],
        },
        { title: "风险等级标签", hint: "与 Action 风险分级同源", keys: ["低风险", "中风险", "高风险"] },
        { title: "运行环境标签", hint: "开发 / 测试 / 生产", keys: ["开发", "测试", "生产"] },
        { title: "空状态与骨架", hint: "空不等于坏", keys: ["暂时没有数据", "骨架屏"] },
      ],
    },
    {
      id: "tables",
      label: "表格与详情",
      source:
        "表格规范：固定表头、数值右对齐、轻量信息行内展开、重对象进完整详情页。实现是 DataTableV2。",
      cards: [
        { title: "数据表格", hint: "固定表头 · 数值右对齐", keys: ["筛选条", "列显隐", "密度", "分页"] },
        { title: "普通行内详情", hint: "对象属性、关联关系和常用入口", keys: ["适用对象", "展开方式"] },
        {
          title: "证据行内详情",
          hint: "观测时间、来源、哈希、审计编号",
          keys: ["适用对象", "必含字段"],
        },
      ],
    },
    {
      id: "states",
      label: "页面状态",
      source:
        "八种页面状态的定义与用法。实现是 ui-admin 的 PageState，Storybook 里有可交互样例。",
      cards: [
        {
          title: "八种状态",
          hint: "同一页面位置，不同的诚实说法",
          keys: [
            "Loading 正在载入",
            "Empty 还没有记录",
            "No Results 没有匹配的数据",
            "Error 数据加载失败",
            "PermissionDenied 没有查看权限",
            "数据已过期",
            "部分数据",
            "来源暂不可用",
          ],
        },
        {
          title: "每种状态必须携带的证据",
          hint: "「出错了」不是状态，是感想",
          keys: ["来源", "范围 / 查询条件", "观测时间", "阈值", "缺失的是哪一部分"],
        },
      ],
      notes: [
        {
          title: "「部分数据」与「空」不是一回事",
          body:
            "可用来源已展示、缺失来源不会被静默忽略——这是宪法 12 条在页面状态上的落点。把部分数据显示成完整数据，比显示成空更糟。",
        },
      ],
    },
    {
      id: "components",
      label: "复杂组件",
      source: "流程节点、内容日历单元格、桌面 / 手机预览框与告警时间线的规范；随各自功能片实现。",
      cards: [
        { title: "流程节点", hint: "自动化流程与审批链共用", keys: ["节点序号", "标题", "说明"] },
        { title: "内容日历单元格", hint: "内容发布用", keys: ["日期", "时间与标题", "渠道与状态"] },
        { title: "桌面与手机预览框", hint: "应用配置用", keys: ["桌面预览", "手机预览"] },
        { title: "告警时间线", hint: "告警与故障详情用", keys: ["时刻", "事件"] },
      ],
    },
  ],
};
