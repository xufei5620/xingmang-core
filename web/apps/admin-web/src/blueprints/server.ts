import type { BlueprintLink, BlueprintPage } from "./types";

/** 服务器详情蓝图允许演示的稳定路由段。
 *
 *  这些值来自原型的对象 ID，只作为 UI 深链 fixture 使用；它们不代表当前环境
 *  已登记的资产，也不会触发任何 API 查询。详情页会把所有实时字段显示为「未接入」。 */
export const SERVER_DETAIL_PREVIEW_IDS = [
  "srv_sin_01",
  "srv_lax_02",
  "srv_fsn_01",
  "srv_hkg_db01",
  "srv_sjc_proxy01",
  "srv_nrt_dev01",
] as const;

export type ServerDetailPreviewId = (typeof SERVER_DETAIL_PREVIEW_IDS)[number];

/** 服务器详情的真实应用路由（ADMIN-IA §三、§四）。 */
export function serverDetailPath(serverId: string): string {
  return `/platforms/server/detail/${encodeURIComponent(serverId)}`;
}

/** 只允许蓝图已声明的对象 ID 进入静态详情壳。 */
export function isServerDetailPreviewId(value: string): value is ServerDetailPreviewId {
  return (SERVER_DETAIL_PREVIEW_IDS as readonly string[]).includes(value);
}

const SERVER_DETAIL_LINKS: readonly BlueprintLink[] = SERVER_DETAIL_PREVIEW_IDS.map((serverId) => ({
  href: serverDetailPath(serverId),
  label: `服务器详情 · ${serverId}`,
  description: "只读蓝图",
}));

/** 服务器平台页的蓝图规格（UI 第 6 片，ADMIN-IA §2.2 的 7 页签）。
 *
 *  页签集合、命名与顺序来自 ui-admin/navigation.ts 的 `server` 条目
 *  （那份是 ADMIN-IA 的可执行副本）；本文件只补每一格的**内容结构**。
 *  两处必须对得上，`server.test.ts` 会逐条断言。
 *
 *  ⚠️ **旧路由别名**（ADMIN-IA §三）：`containers`→`services`、`certs`→`domains`、
 *  `logs`→`monitoring`。别名由路由层处理（platforms.ts 的页签解析），
 *  本文件只认新 id——两处都认的话，一个拼错的页签名会被静默当成别名放行。
 *
 *  数据来源：原型渲染态的 `serverPage()` 分发器与 `SERVER_*` 数据常量。
 *  **列头与文案逐字抄，样例数字一个不抄**（理由见 types.ts）。
 *
 *  整页的定位一句话：服务器资产中心，**只读观测优先**——不提供关机、重启、
 *  任意命令执行或密码明文查看。这条在页顶横幅里，不是注释里。 */

/** 资产表的列。overview 与 assets 两格共用同一张表（原型如此）。 */
const ASSET_COLUMNS = [
  "服务器资产",
  "供应商 / 区域",
  "用途 / 服务",
  "配置",
  "成本 / 续费",
  "CPU",
  "内存",
  "磁盘",
  "连接",
  "工作负载",
  "详情",
] as const;

const AGENT_SOURCE = "数据由 Server Agent 只读上报（ADR-015），随 M2 接入。";

export const SERVER_BLUEPRINT: BlueprintPage = {
  description:
    "统一查看服务器资产、采购费用、续费风险、凭据完整度和只读运行状态。",
  banner:
    "服务器资产中心：只读观测优先，不提供关机、重启、任意命令执行或密码明文查看。",
  tabs: [
    {
      id: "overview",
      label: "概览",
      source: `概览聚合资产、成本、续费与告警四类事实。${AGENT_SOURCE}`,
      tiles: [
        { label: "服务器资产", note: "在线 / 降级 / 离线分布" },
        { label: "折算月成本", note: "跨周期统一折算，保留汇率快照" },
        { label: "30 天内续费", note: "按续费日排期，含月度折算金额" },
        { label: "活跃告警", note: "严重与注意两级合并计数" },
      ],
      filters: ["搜索服务器 / IP / 服务", "状态：全部", "供应商：全部", "续费：30 天内"],
      tables: [
        {
          caption: "服务器资产总览",
          columns: [...ASSET_COLUMNS],
          source: AGENT_SOURCE,
          links: SERVER_DETAIL_LINKS,
        },
      ],
      cards: [
        {
          title: "近 7 日主机平均负载",
          hint: "所有已接入 Agent 的服务器",
          keys: ["观测窗口", "采样间隔", "数据新鲜度"],
        },
        {
          title: "近期风险",
          hint: "续费、证书和资源告警合并",
          keys: ["对象", "风险", "时间", "状态"],
        },
      ],
    },
    {
      id: "assets",
      label: "服务器资产",
      source: `一行代表一台实际服务器。${AGENT_SOURCE}`,
      tiles: [
        { label: "使用中", note: "状态为在用的资产台数" },
        { label: "生产环境", note: "环境标记为生产的台数" },
        { label: "数据过期", note: "Agent 心跳超出新鲜度阈值的台数" },
        { label: "凭据需轮换", note: "登录凭据超过轮换周期的台数" },
      ],
      filters: ["搜索当前列表", "环境：全部", "连接：全部", "视图：全部 / 生产环境 / 需关注"],
      tables: [
        {
          caption: "服务器资产明细",
          columns: [...ASSET_COLUMNS],
          source: AGENT_SOURCE,
          links: SERVER_DETAIL_LINKS,
        },
      ],
      notes: [
        {
          title: "服务器详情",
          body:
            "点开一行进入服务器详情：资产与采购、费用与续费、配置与网络、实时资源水位、访问与凭据、部署服务与容器、关联域名与证书、告警与审计。详情页同样只读。",
        },
      ],
    },
    {
      id: "suppliers",
      label: "供应商与采购",
      source:
        "供应商、购买账号与采购记录由平台自己登记（不是上游读来的），随 M2 的服务器资产登记簿一并上线。",
      tiles: [
        { label: "供应商", note: "已登记的服务器供应商数" },
        { label: "购买账号", note: "供应商门户账号数，凭据只存引用" },
        { label: "关联服务器", note: "已挂到供应商账号下的台数" },
        { label: "折算月支出", note: "按统一汇率折算的月度支出" },
      ],
      filters: [
        "搜索供应商 / 购买账号",
        "币种：全部",
        "二次验证（MFA）：全部",
        "状态：全部",
      ],
      tables: [
        {
          caption: "供应商与购买账号",
          columns: [
            "供应商",
            "购买账号",
            "密钥引用（CredentialRef）",
            "二次验证（MFA）",
            "服务器",
            "月度折算",
            "最近续费",
            "状态",
            "详情",
          ],
          source: "供应商登记随 M2 上线。",
        },
      ],
      notes: [
        {
          title: "采购权限与服务器权限相互独立",
          body:
            "购买账号密码只保存为密钥引用（CredentialRef），此处不回显。供应商门户的采购权限与服务器 Root 权限是两套东西，不得互相顶替。",
        },
      ],
    },
    {
      id: "services",
      label: "服务与容器",
      source: `从业务服务反查实际运行的服务器、镜像版本、端口与健康状态。${AGENT_SOURCE}`,
      tiles: [
        { label: "运行服务", note: "已纳管的容器 / 进程数" },
        { label: "健康", note: "健康检查通过的服务数" },
        { label: "需关注", note: "健康检查降级的服务数" },
        { label: "异常", note: "健康检查失败的服务数" },
      ],
      filters: ["搜索服务 / 镜像 / 服务器", "平台：全部", "状态：全部", "环境：全部"],
      tables: [
        {
          caption: "服务与容器",
          columns: [
            "服务 / 平台",
            "所在服务器",
            "镜像",
            "端口",
            "版本",
            "状态",
            "最近部署",
            "详情",
          ],
          source: AGENT_SOURCE,
        },
      ],
      notes: [
        {
          title: "不提供重启、删除容器或任意命令执行",
          body:
            "工作负载详情只展示服务 ID、平台、所在服务器、镜像、版本、端口、运行状态与最近部署，以及「查看限定日志 / 进入服务器详情」两个只读入口。",
        },
      ],
    },
    {
      id: "domains",
      label: "域名与证书",
      source:
        "域名、解析目标与证书生命周期放在同一条映射关系里；证书状态由 M2 的探测任务填充。",
      tiles: [
        { label: "域名", note: "已登记的域名数" },
        { label: "证书健康", note: "证书有效且未临期的域名数" },
        { label: "30 天内到期", note: "证书剩余有效期不足 30 天" },
        { label: "未自动续期", note: "续期方式为手动的域名数" },
      ],
      filters: ["搜索域名 / 服务 / 服务器", "证书状态：全部", "续期方式：全部", "到期：全部"],
      tables: [
        {
          caption: "域名与证书",
          columns: [
            "域名 / 服务",
            "服务器",
            "解析目标",
            "签发机构",
            "到期日",
            "剩余",
            "续期",
            "详情",
          ],
          source: "证书探测随 M2 上线。",
        },
      ],
    },
    {
      id: "monitoring",
      label: "监控与告警",
      source: `主机指标、Agent 新鲜度、告警与日志入口统一按服务器检索。${AGENT_SOURCE}`,
      tiles: [
        { label: "已接入 Agent", note: "已接入 / 应接入台数" },
        { label: "在线 / 降级", note: "按最近心跳判定" },
        { label: "严重告警", note: "级别为严重的活跃告警数" },
        { label: "最后观测", note: "全量 Agent 中最新的一次心跳时刻" },
      ],
      filters: ["搜索告警 / 服务器 / 规则", "级别：全部", "状态：全部", "时间：最近24小时"],
      tables: [
        {
          caption: "服务器告警",
          columns: ["告警号", "级别", "规则 / 详情", "服务器", "开始", "状态", "详情"],
          source: "服务器告警随 M2 的 Agent 接入上线；控制平面自身的告警见「告警与故障」。",
        },
        {
          caption: "最近日志与事件",
          columns: ["时间", "服务器", "来源", "级别", "摘要", "详情"],
          source: "日志只提供限定字段查询、保留 30 天，不提供任意 Shell。",
        },
      ],
      cards: [
        {
          title: "近 24 小时资源水位",
          hint: "CPU / 内存综合指数",
          keys: ["观测窗口", "采样间隔", "数据新鲜度"],
        },
        {
          title: "Agent 数据新鲜度",
          hint: "任何监控数字都带观测时间",
          keys: ["服务器", "连接", "最后心跳", "数据"],
        },
      ],
    },
    {
      id: "creds",
      label: "连接与凭据",
      source:
        "供应商门户、SSH、sudo 与只读 Agent 的密钥引用与轮换状态，随 M2 的服务器登记簿上线。",
      tiles: [
        { label: "密钥引用", note: "已登记的 CredentialRef 数" },
        { label: "验证正常", note: "最近一次连通性验证通过" },
        { label: "需轮换", note: "超过轮换周期未更新" },
        { label: "验证失败", note: "最近一次验证未通过" },
      ],
      filters: [
        "搜索目标 / 账号 / 密钥引用（CredentialRef）",
        "类型：全部",
        "状态：全部",
        "权限域：全部",
      ],
      tables: [
        {
          caption: "连接与凭据",
          columns: [
            "目标",
            "类型",
            "账号",
            "密钥引用（CredentialRef）",
            "状态",
            "最近轮换",
            "最近验证",
            "权限域",
            "详情",
          ],
          source: "凭据登记随 M2 上线。",
        },
      ],
      notes: [
        {
          title: "密码、私钥与 Token 不进入页面响应",
          body:
            "本页只展示账号、密钥引用（CredentialRef）、状态与审计信息。状态一律带「不可查看」后缀——它不是提示，是这一列的取值本身（宪法 7 条：凭据只经 CredentialRef）。",
        },
      ],
    },
  ],
};
