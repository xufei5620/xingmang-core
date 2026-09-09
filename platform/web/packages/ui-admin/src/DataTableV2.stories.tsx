import type { Meta, StoryObj } from "@storybook/react-vite";
import { Badge } from "@xingmang/ui-primitives";
import { DataTableV2, type DataTableColumn } from "./DataTableV2";
import type { DataTableViewPersistence } from "./DataTableV2";
import { PageState } from "./PageState";

interface Channel {
  id: string;
  name: string;
  type: string;
  state: "启用" | "停用";
  balanceMinorUnits: bigint;
  errorRate: number;
  models: number;
  latency: number;
}

const channels: Channel[] = [
  { id: "ch-openai", name: "OpenAI 中转·主", type: "openai", state: "启用", balanceMinorUnits: 1284500n, errorRate: 1200, models: 12, latency: 480 },
  { id: "ch-gemini", name: "Gemini 备用", type: "gemini", state: "停用", balanceMinorUnits: 250000n, errorRate: 0, models: 5, latency: 0 },
  { id: "ch-ollama", name: "自建 Ollama", type: "ollama", state: "启用", balanceMinorUnits: 0n, errorRate: 187500, models: 3, latency: 2340 },
  { id: "ch-azure", name: "Azure 东亚", type: "azure", state: "启用", balanceMinorUnits: 990000n, errorRate: 300, models: 9, latency: 620 },
  { id: "ch-claude", name: "Claude 直连", type: "anthropic", state: "启用", balanceMinorUnits: 4500000n, errorRate: 90, models: 7, latency: 510 },
  { id: "ch-bedrock", name: "Bedrock 备份", type: "aws", state: "停用", balanceMinorUnits: 120000n, errorRate: 0, models: 4, latency: 0 },
];

const yuan = (minor: bigint): string =>
  `¥${(Number(minor) / 100).toLocaleString("zh-CN", { minimumFractionDigits: 2 })}`;

const columns: DataTableColumn<Channel>[] = [
  {
    id: "name",
    header: "渠道",
    primary: true,
    value: (row) => `${row.name} ${row.type}`,
    cell: (row) => (
      <>
        <span className="font-medium">{row.name}</span>
        <p className="font-mono text-xs text-fg-muted">
          {row.id} · {row.type}
        </p>
      </>
    ),
  },
  {
    id: "state",
    header: "状态",
    value: (row) => row.state,
    cell: (row) => (
      <Badge tone={row.state === "启用" ? "success" : "neutral"}>{row.state}</Badge>
    ),
  },
  {
    id: "balance",
    header: "余额",
    numeric: true,
    // 排序用最小单位的整数（bigint），不用格式化后的字符串
    value: (row) => row.balanceMinorUnits,
    cell: (row) => yuan(row.balanceMinorUnits),
  },
  {
    id: "errorRate",
    header: "错误率",
    numeric: true,
    value: (row) => row.errorRate,
    cell: (row) => `${(row.errorRate / 10000).toFixed(2)}%`,
  },
  { id: "models", header: "模型数", numeric: true, value: (row) => row.models, cell: (row) => row.models },
  {
    id: "latency",
    header: "延迟",
    numeric: true,
    value: (row) => row.latency,
    cell: (row) => (row.latency === 0 ? "—" : `${row.latency} ms`),
  },
];

const emptyState = (
  <PageState kind="empty" title="没有渠道" description="这次观测里 channels 为空数组" />
);

const base = {
  caption: "渠道列表：启停、余额、错误率、模型数与延迟",
  columns,
  rows: channels,
  rowKey: (row: Channel) => row.id,
  emptyState,
};

// 实例化表达式：把泛型钉到 Channel 上。直接写 component: DataTableV2 的话,
// Storybook 的 Meta 会把 T 推成 unknown，于是 args 里的列定义对不上类型
const Table = DataTableV2<Channel>;

const savedViewCapabilities = columns.map((column) => ({
  id: column.id,
  sortable: column.value !== undefined || column.sortAs !== undefined,
  primary: column.primary === true,
  defaultHidden: column.defaultHidden === true,
}));

const persistedView = {
  id: "view-channel-active",
  table_key: "platform.sub2api.channels",
  name: "在用渠道",
  state_version: 1,
  state: {
    schema_version: 1 as const,
    query: "",
    filters: { state: "启用" },
    sort: { column_id: "balance", direction: "desc" as const },
    columns: { known: columns.map((column) => column.id), visible: columns.map((column) => column.id) },
    density: "compact" as const,
  },
  created_at: "2026-08-29T01:00:00Z",
  updated_at: "2026-08-29T01:00:00Z",
};

function persistence(
  over: Partial<DataTableViewPersistence> = {},
): DataTableViewPersistence {
  return {
    tableKey: "platform.sub2api.channels",
    items: [persistedView],
    schemaReady: true,
    columnCapabilities: savedViewCapabilities,
    status: "ready",
    onSave: async () => ({ runId: "run-story-save" }),
    onRemove: async () => ({ runId: "run-story-remove" }),
    ...over,
  };
}

const meta = {
  title: "Admin/DataTableV2",
  component: Table,
  parameters: { layout: "padded" },
} satisfies Meta<typeof Table>;
export default meta;
type Story = StoryObj<typeof meta>;

/** 全套能力：排序、搜索、筛选、视图、列管理、密度、分页。 */
export const Default: Story = {
  args: {
    ...base,
    pageSize: 4,
    searchable: true,
    filters: [{ columnId: "state", label: "状态", options: ["启用", "停用"] }],
    views: [
      {
        name: "全部",
        state: {
          query: "",
          filters: {},
          sort: null,
          visibleColumns: columns.map((c) => c.id),
          density: "compact",
        },
      },
      {
        name: "只看启用",
        state: {
          query: "",
          filters: { state: "启用" },
          sort: null,
          visibleColumns: columns.map((c) => c.id),
          density: "compact",
        },
      },
    ],
  },
};

export const PersonalViewsReady: Story = {
  args: { ...Default.args, persistence: persistence() },
};

export const PersonalViewsLoading: Story = {
  args: { ...Default.args, persistence: persistence({ status: "loading", items: [] }) },
};

export const PersonalViewsDenied: Story = {
  args: {
    ...Default.args,
    persistence: persistence({
      status: "denied",
      items: [],
      message: "缺少 ui.saved_view.manage；个人视图不会保存到浏览器。",
    }),
  },
};

export const PersonalViewsQueryError: Story = {
  args: {
    ...Default.args,
    persistence: persistence({
      status: "error",
      items: [],
      message: "个人视图读取失败；内置视图仍可使用。",
      onRetry: () => {},
    }),
  },
};

export const PersonalViewSaveError: Story = {
  args: {
    ...Default.args,
    persistence: persistence({
      onSave: async () => { throw new globalThis.Error("Action 执行失败，当前条件没有被保存"); },
    }),
  },
};

export const PersonalViewUnsupportedVersion: Story = {
  args: {
    ...Default.args,
    persistence: persistence({
      items: [{
        ...persistedView,
        state_version: 2,
        state: { ...persistedView.state, schema_version: 2 as unknown as 1 },
      }],
    }),
  },
};

export const PersonalViewSaveAndRemoveSuccess: Story = {
  args: {
    ...Default.args,
    persistence: persistence({
      onSave: async () => ({ runId: "run-story-save-success" }),
      onRemove: async () => ({ runId: "run-story-remove-success" }),
    }),
  },
};

/** 加载态由调用方在表格**之外**处理（ApiStateView / PageState）:
 *  表格自己不知道数据是怎么来的，也就不该假装知道它还没来。 */
export const Loading: Story = {
  args: base,
  render: () => <PageState kind="loading" />,
};

/** 数据源本来就空：只显示调用方给的空态，不摆一张有表头、有分页、
 *  一行都没有的表——那会让人以为是自己筛没了。 */
export const Empty: Story = { args: { ...base, rows: [] } };

/** 筛完为空是**另一种**空：它的下一步是清筛选，不是去造数据。
 *  这一格由表格自己给，文案与「清除筛选」按钮都在。 */
export const 筛选后为空: Story = {
  args: {
    ...base,
    searchable: true,
    filters: [{ columnId: "state", label: "状态", options: ["启用", "停用"] }],
    rows: channels.filter((c) => c.state === "启用"),
  },
};

export const Error: Story = {
  args: base,
  render: () => (
    <PageState
      kind="error"
      message="读取渠道指标失败：503 upstream_unavailable（错误码 UNAVAILABLE）"
      footnote="request_id: req-8f21c0"
      onRetry={() => {}}
    />
  ),
};

export const PermissionDenied: Story = {
  args: base,
  render: () => <PageState kind="denied" permission="ops.read" />,
};

/** 行选择与批量条。批量条只是**插槽**——本片不做通用批量操作,
 *  写操作一律走 Action 且 L2 以上要审批。 */
export const 行选择: Story = {
  args: {
    ...base,
    selectable: true,
    bulkActions: (keys) => (
      <span className="text-fg-muted">
        已选中 {keys.length} 条；批量操作随 Foundation-B 上线——这里不执行任何真实操作。
      </span>
    ),
  },
};

/** 行展开：展开区是独立的一行并跨满整表，列一隐藏也不会跟着消失。 */
export const 行展开: Story = {
  args: {
    ...base,
    renderExpanded: (row) => (
      <dl className="grid grid-cols-2 gap-2 text-xs">
        <div>
          <dt className="text-fg-muted">渠道 id</dt>
          <dd className="font-mono text-fg">{row.id}</dd>
        </div>
        <div>
          <dt className="text-fg-muted">上游类型</dt>
          <dd className="font-mono text-fg">{row.type}</dd>
        </div>
      </dl>
    ),
  },
};

/** 超长文案与很多列：横向滚动只发生在表格容器内部，页面 body 永不横滚（§11.2）。 */
export const LongText: Story = {
  args: {
    ...base,
    rows: [
      {
        ...(channels[0] as Channel),
        name: "OpenAI 中转·主（华东一区只读副本 · 由平台组代运维 · 计费口径见 ADR-006）",
        type: "openai-compatible-relay-with-a-very-long-type-name",
      },
      ...channels.slice(1),
    ],
  },
  decorators: [
    (Story) => (
      <div className="w-160 max-w-full overflow-hidden border border-edge">
        <Story />
      </div>
    ),
  ],
};

/** 紧凑密度（默认）：一屏能多看十行，是这类表的主要价值。 */
export const Compact: Story = { args: { ...base, defaultDensity: "compact", pageSize: 6 } };

export const DarkMode: Story = {
  args: { ...base, pageSize: 4, searchable: true },
  globals: { theme: "dark" },
};
