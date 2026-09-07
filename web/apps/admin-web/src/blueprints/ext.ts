import type { BlueprintPage } from "./types";

/** 扩展能力四页的蓝图规格（UI 第 6 片，实施计划 §2.5）。
 *
 *  这四页是**只读蓝图**：仅预览、不保存、不发布、不执行。它们存在的意义是
 *  把「将来可能做什么」画出来，好让人现在就能判断该不该做——**不得因为
 *  页面存在就提前建后端**。
 *
 *  所以这里比治理段更克制：没有筛选条（筛不出东西的筛选器在一个连数据源都
 *  没规划的页面上纯属误导），编辑器类的区块只列字段名不给可输入的控件。
 *
 *  子页签集合与命名来自 ui-admin/navigation.ts 的 EXT_NAV_ITEMS；
 *  内容结构来自原型渲染态的 `V["ext/*"]`。**文案逐字抄，样例数字一个不抄**。 */

// 这四页**不设 banner**：PlaceholderPage 的 PlaceholderGate 已经给
// `/ext/*` 挂了「只读蓝图：仅预览、不保存、不发布、不执行」，再加一条几乎同义的
// 横幅只是噪音——两条并排的红线提示会让人两条都不读。
// 原型的 futureBanner 还提了「禁止填写真实密码、密钥或 Token」，这里不抄：
// 本片的蓝图一个可输入控件都没有，那句话会指向不存在的东西。

const NO_BACKEND = "后置能力：尚未排期，不因为这一页存在就提前建后端。";

export const EXT_APP_BLUEPRINT: BlueprintPage = {
  description: "具体展示未来如何管理多个前端应用、页面配置、组件和发布版本。",
  tiles: [
    { label: "应用", note: "纳管的前端应用数" },
    { label: "配置草稿", note: "未发布的配置版本数" },
    { label: "待审核", note: "等待人工审核的配置数" },
    { label: "正式版本", note: "已发布到生产的配置版本数" },
  ],
  tabs: [
    {
      id: "catalog",
      label: "应用目录",
      source: NO_BACKEND,
      tables: [
        {
          caption: "应用目录",
          columns: [
            "应用",
            "域名",
            "环境",
            "登录方式",
            "配置版本",
            "发布版本",
            "负责人",
            "状态",
            "详情",
          ],
          source: NO_BACKEND,
        },
      ],
    },
    {
      id: "pages",
      label: "页面配置",
      source: NO_BACKEND,
      cards: [
        {
          title: "配置分类",
          hint: "左侧菜单",
          keys: ["品牌与主题", "顶部导航", "公告与联系方式", "FAQ与协议", "功能开关"],
        },
        {
          title: "字段设置",
          hint: "草稿态，不保存",
          keys: ["应用名称", "主色", "Logo文字", "联系电话", "首页公告"],
        },
        { title: "实时预览", hint: "桌面 / 手机", keys: ["桌面预览", "手机预览"] },
      ],
    },
    {
      id: "components",
      label: "页面组件",
      source: NO_BACKEND,
      tables: [
        {
          caption: "页面组件",
          columns: ["页面组件", "用途", "支持应用", "配置字段", "当前版本", "状态", "详情"],
          source: NO_BACKEND,
        },
      ],
    },
    {
      id: "releases",
      label: "版本与发布",
      source: NO_BACKEND,
      tables: [
        {
          caption: "配置版本",
          columns: [
            "配置版本",
            "应用",
            "创建人",
            "阶段",
            "测试结果",
            "正式时间",
            "回滚来源",
            "状态",
          ],
          source: NO_BACKEND,
        },
      ],
      notes: [
        {
          title: "版本流程",
          body:
            "草稿 → 预览 → 待审核 → 测试环境 → 正式版本。这一页只展示流程，不保存、不发布、不回滚。",
        },
      ],
    },
  ],
};

export const EXT_INTEGRATION_BLUEPRINT: BlueprintPage = {
  description: "具体展示 API 调用方、Webhook、自动化流程和每次运行记录。",
  tiles: [
    { label: "API调用方", note: "已登记的外部调用方数" },
    { label: "Webhook异常", note: "投递失败或已暂停的 Webhook 数" },
    { label: "流程草稿", note: "未启用的自动化流程数" },
    { label: "多次失败任务", note: "连续失败、需人工介入的运行数" },
  ],
  tabs: [
    {
      id: "clients",
      label: "API调用方",
      source: `${NO_BACKEND}对外开放 API 需要先有配额、限流与审计，那几样都还没有。`,
      tables: [
        {
          caption: "API调用方",
          columns: [
            "API调用方",
            "用途",
            "身份类型",
            "权限范围",
            "IP限制",
            "调用配额",
            "最近请求",
            "状态",
            "详情",
          ],
          source: NO_BACKEND,
        },
      ],
    },
    {
      id: "webhooks",
      label: "Webhook",
      source: NO_BACKEND,
      tables: [
        {
          caption: "Webhook",
          columns: [
            "Webhook",
            "方向",
            "事件类型",
            "目标地址",
            "签名",
            "成功率",
            "最近投递",
            "状态",
            "详情",
          ],
          source: NO_BACKEND,
        },
      ],
    },
    {
      id: "flows",
      label: "自动化流程",
      source: `${NO_BACKEND}流程里的「受控操作」节点必须走 Action 与审批链，不会有绕过通道。`,
      cards: [
        {
          title: "流程步骤库",
          hint: "可用节点类型",
          keys: [
            "手动或定时触发",
            "Webhook触发",
            "读取数据",
            "受控操作",
            "条件判断",
            "人工审批",
            "发送通知",
          ],
        },
        { title: "流程画布", hint: "草稿态，不运行", keys: ["节点序号", "节点标题", "节点说明"] },
        {
          title: "步骤设置",
          hint: "以「人工审批」节点为例",
          keys: ["当前步骤", "超时", "批准人数", "超时处理", "失败重试", "审计"],
        },
      ],
    },
    {
      id: "runs",
      label: "运行记录",
      source: NO_BACKEND,
      tables: [
        {
          caption: "流程运行记录",
          columns: ["运行", "流程", "触发方式", "开始", "耗时", "结果", "审计", "详情"],
          source: NO_BACKEND,
        },
      ],
      cards: [{ title: "运行时间线", hint: "单次运行的逐步记录", keys: ["时刻", "事件"] }],
    },
  ],
};

export const EXT_PUBLISHING_BLUEPRINT: BlueprintPage = {
  description: "具体展示内容日历、草稿素材、审核、渠道账号和发布结果。",
  tiles: [
    { label: "本月计划", note: "本月已排期的内容条数" },
    { label: "待审核", note: "等待人工审核的内容数" },
    { label: "待发布", note: "已审核、等待到点发布的内容数" },
    { label: "发布失败", note: "投递失败、需重试的内容数" },
  ],
  tabs: [
    {
      id: "calendar",
      label: "内容日历",
      source: NO_BACKEND,
      cards: [
        {
          title: "内容日历",
          hint: "完整月视图 / 周视图",
          keys: ["日期", "计划时间与标题", "渠道与状态"],
        },
      ],
    },
    {
      id: "drafts",
      label: "草稿与素材",
      source: NO_BACKEND,
      cards: [
        { title: "草稿与素材", hint: "左侧列表", keys: ["草稿标题", "素材库"] },
        { title: "草稿编辑", hint: "草稿态，不发布", keys: ["标题", "正文", "计划时间", "素材"] },
        { title: "渠道预览", hint: "各渠道的呈现差异", keys: ["Telegram预览", "X预览"] },
      ],
    },
    {
      id: "approvals",
      label: "审批队列",
      source: `${NO_BACKEND}对外发布属于有外部影响的操作，必须人工审核，不会有自动放行。`,
      tables: [
        {
          caption: "内容审批队列",
          columns: ["内容", "渠道", "提交人", "内容类型", "风险", "计划时间", "状态", "详情"],
          source: NO_BACKEND,
        },
      ],
    },
    {
      id: "channels",
      label: "渠道与账号",
      source: `${NO_BACKEND}渠道授权凭据同样只经 CredentialRef，不在页面上回显。`,
      tables: [
        {
          caption: "渠道与账号",
          columns: [
            "渠道账号",
            "平台",
            "用途",
            "授权状态",
            "到期提醒",
            "速率限制",
            "最近发布",
            "状态",
          ],
          source: NO_BACKEND,
        },
      ],
    },
    {
      id: "records",
      label: "发布记录",
      source: NO_BACKEND,
      tables: [
        {
          caption: "发布记录",
          columns: [
            "发布记录",
            "内容",
            "渠道",
            "计划时间",
            "结果",
            "平台返回编号",
            "互动数据",
            "详情",
          ],
          source: NO_BACKEND,
        },
      ],
    },
  ],
};

export const EXT_AI_BLUEPRINT: BlueprintPage = {
  description: "具体展示模型线路、AI 角色、可用工具、运行证据和预算。",
  tiles: [
    { label: "模型线路", note: "已配置的主备线路组数" },
    { label: "AI角色", note: "已定义的 AI 角色数" },
    { label: "可用工具", note: "已登记的 AI 工具数" },
    { label: "月度预算", note: "已用 / 上限" },
  ],
  tabs: [
    {
      id: "routes",
      label: "模型线路",
      source: NO_BACKEND,
      tables: [
        {
          caption: "模型线路",
          columns: [
            "模型线路",
            "主线路",
            "备用线路",
            "可用模型",
            "今日调用",
            "今日成本",
            "预算使用",
            "状态",
            "详情",
          ],
          source: NO_BACKEND,
        },
      ],
    },
    {
      id: "roles",
      label: "AI角色",
      source: `${NO_BACKEND}AI 身份不得作为 L3/L4 的第二审批人（宪法 10 条），这条不是可配置项。`,
      tables: [
        {
          caption: "AI角色",
          columns: [
            "AI角色",
            "主要用途",
            "可访问数据",
            "可用工具",
            "月度预算",
            "人工审批",
            "状态",
            "详情",
          ],
          source: NO_BACKEND,
        },
      ],
    },
    {
      id: "tools",
      label: "AI工具",
      source: `${NO_BACKEND}高风险工具（如重启服务）恒为「暂未开放」，解锁要过 Action 与审批链。`,
      tables: [
        {
          caption: "AI工具",
          columns: [
            "AI工具",
            "用途",
            "可用角色",
            "权限要求",
            "风险",
            "审批",
            "环境",
            "状态",
            "详情",
          ],
          source: NO_BACKEND,
        },
      ],
    },
    {
      id: "budget",
      label: "运行与预算",
      source: NO_BACKEND,
      tables: [
        {
          caption: "AI 运行记录",
          columns: [
            "运行",
            "AI角色",
            "模型",
            "输入数据等级",
            "工具调用",
            "Token",
            "费用",
            "结果",
            "详情",
          ],
          source: NO_BACKEND,
        },
      ],
      cards: [
        {
          title: "运行证据与预算使用",
          hint: "单次运行的证据",
          keys: ["输入数据等级", "脱敏状态", "工具调用", "预算使用", "审计编号"],
        },
      ],
    },
  ],
};
