import { useQuery } from "@tanstack/react-query";
import {
  DataTableV2,
  FreshnessBadge,
  FreshnessNote,
  PageState,
  type DataTableColumn,
} from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import { listMetrics, type MetricItem } from "../api/platform";
import { ApiStateView } from "./ApiStateView";
import {
  metricLabel,
  NEWAPI_CHANNELS_METRIC_KEY,
  readNewApiChannelRows,
  type NewApiChannelRow,
} from "../lib/metrics";
import {
  formatCount,
  formatErrorRatePPM,
  formatMinorUnits,
  toIntegerValue,
} from "../lib/money";


/** 未配置余额时的显示文案。
 *
 *  与「¥0.00」是相反的两件事：这个渠道本来就不按余额计费（正常），
 *  而 0 是配了余额并且已经花光（要立刻处理）。契约层为此把余额做成可空、
 *  nil 时不写那个键；这里是那条纪律在屏幕上的最后一米。 */
const UNCONFIGURED_BALANCE_TEXT = "未配置";

/** NewAPI 渠道表：把总览卡片上那句「N 个渠道」摊开成逐渠道的状态。
 *
 *  数据来源是 `newapi.channels.status` 这一条指标的 value，不是另一个接口——
 *  因此它的新鲜度就是那条指标的新鲜度，必须原样带上（规格 §9.1）：
 *  一张看着很具体的明细表最容易让人忘记问「这是什么时候的数」。
 *
 *  这是 UI 原型对 NewAPI 页面的核心诉求。原型里同屏还要显示联系人、
 *  充值成本率、接入平台标签、上游账号凭据到期日——那四类**不是 NewAPI 的
 *  API 数据**，是平台自己的经营登记（supplier ledger），归 XM-0037 单独设计
 *  （理由见 connectors/newapi/doc.go 与 contracts/connectors/newapi.read.v1.md）。
 *  这里只画读得到的那部分，不给读不到的东西留空列——空列会让人以为
 *  「上游没返回」，而事实是我们压根没问过它。 */
export function NewApiChannelsPanel() {
  const query = useQuery({
    queryKey: ["metrics"],
    queryFn: ({ signal }) => listMetrics({ signal }),
  });

  const metric = (query.data ?? []).find((m) => m.metric_key === NEWAPI_CHANNELS_METRIC_KEY);

  return (
    <section className="flex flex-col gap-3">
      <header className="flex items-start justify-between gap-3">
        <p className="text-xs text-fg-muted">
          逐渠道的启停、余额、错误率与延迟，取自渠道状态指标的最近一次观测。
        </p>
        {metric ? <FreshnessBadge freshness={metric.freshness} /> : null}
      </header>
      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        <NewApiChannelsView metric={metric} />
      </ApiStateView>
    </section>
  );
}

function NewApiChannelsView({ metric }: { metric: MetricItem | undefined }) {
  if (!metric) {
    return (
      <PageState
        kind="empty"
        title="暂无渠道状态指标"
        description={`该环境下还没有 ${NEWAPI_CHANNELS_METRIC_KEY} 的观测；NewAPI 采集任务跑起来后会出现在这里`}
      />
    );
  }

  // 从未成功采集时不渲染表格：后端此时 value 里可能还留着上一版结构或空数组，
  // 画成一张「0 个渠道」的表就是编数据（宪法 12 条）
  if (metric.freshness.state === "uninitialized") {
    return (
      <PageState
        kind="empty"
        title="未初始化"
        description={`${metricLabel(metric.metric_key)} 从未成功采集过，没有可信的渠道明细可显示`}
      />
    );
  }

  const rows = readNewApiChannelRows(metric.value);
  const value = metric.value ?? {};
  const enabled = rows.filter((r) => r.enabled === true).length;

  return (
    <div className="flex flex-col gap-3">
      <div className="rounded-lg border border-edge bg-surface p-3">
        <FreshnessNote freshness={metric.freshness} />
        <p className="text-xs text-fg-muted">
          来源 {metric.source || "—"}
          {metric.watermark ? ` · 水位 ${metric.watermark}` : null}
          {` · ${rows.length} 个渠道 · 启用 ${enabled}`}
          {/* 判据一起显示出来：看板说「2 个异常」时，人要能当场看出异常是按
              什么算的，而不是去翻源码（契约层把它一并写进了 value） */}
          {toIntegerValue(value["unhealthy_threshold_ppm"]) === null
            ? null
            : ` · 异常判据 错误率 ≥ ${formatErrorRatePPM(value["unhealthy_threshold_ppm"])}`}
        </p>
      </div>

      <NewApiChannelTable rows={rows} thresholdPPM={value["unhealthy_threshold_ppm"]} />
    </div>
  );
}

/** 启停状态的文本形态。排序、搜索与筛选都用它。 */
function enabledText(enabled: boolean | null): string {
  if (enabled === null) return "未知";
  return enabled ? "启用" : "停用";
}

function newApiChannelColumns(thresholdPPM: unknown): DataTableColumn<NewApiChannelRow>[] {
  return [
    {
      id: "channel",
      header: "渠道",
      primary: true,
      value: (row) => [row.name, row.channelId, row.type].filter(Boolean).join(" "),
      cell: (row) => (
        <>
          <span className="font-medium">{row.name || row.channelId || "—"}</span>
          <p className="font-mono text-xs text-fg-muted">
            {row.channelId}
            {row.type ? ` · ${row.type}` : null}
          </p>
        </>
      ),
    },
    {
      id: "enabled",
      header: "状态",
      value: (row) => enabledText(row.enabled),
      cell: (row) => <EnabledBadge enabled={row.enabled} />,
    },
    {
      id: "balance",
      header: "余额",
      numeric: true,
      // 未配置余额排在最后而不是当成 0：两者是相反的两件事
      value: (row) => row.balanceMinorUnits ?? null,
      cell: (row) => <BalanceCell row={row} />,
    },
    {
      id: "errorRate",
      header: "错误率",
      numeric: true,
      value: (row) => row.errorRatePPM,
      cell: (row) => <ErrorRateCell row={row} thresholdPPM={thresholdPPM} />,
    },
    {
      id: "modelCount",
      header: "模型数",
      numeric: true,
      value: (row) => row.modelCount,
      cell: (row) => formatCount(row.modelCount),
    },
    {
      id: "latency",
      header: "延迟",
      numeric: true,
      value: (row) => row.latencyMS,
      cell: (row) => (row.latencyMS === null ? "—" : `${formatCount(row.latencyMS)} ms`),
    },
  ];
}

function NewApiChannelTable({
  rows,
  thresholdPPM,
}: {
  rows: NewApiChannelRow[];
  thresholdPPM: unknown;
}) {
  return (
    <DataTableV2
      caption="NewAPI 逐渠道启停、余额、错误率、模型数与延迟"
      columns={newApiChannelColumns(thresholdPPM)}
      rows={rows}
      // channel_id 可能缺失（上游没给），退到下标兜底，避免 key 冲突
      rowKey={(row) => row.channelId || `#${rows.indexOf(row)}`}
      pageSize={10}
      searchable
      filters={[{ columnId: "enabled", label: "状态", options: ["启用", "停用", "未知"] }]}
      emptyState={
        <PageState kind="empty" title="没有渠道" description="这次观测里 channels 为空数组" />
      }
    />
  );
}

/** 启停徽章。
 *
 *  字段缺失显示「未知」而不是默认「启用」：把一个已停用的渠道画成绿色，
 *  比不显示更糟——人会据此排除掉真正的故障原因。 */
function EnabledBadge({ enabled }: { enabled: boolean | null }) {
  if (enabled === null) {
    return (
      <Badge tone="warning" title="上游没有给出 enabled 字段">
        未知
      </Badge>
    );
  }
  return enabled ? (
    <Badge tone="success" title="渠道已启用">
      启用
    </Badge>
  ) : (
    <Badge tone="neutral" title="渠道已停用，不参与调用；停用不计入异常数">
      停用
    </Badge>
  );
}

/** 余额单元格：三态。
 *
 *  「未配置」用弱化文字而不是徽章，因为它是**正常状态**，不该在表里抢眼；
 *  真正要抢眼的是余额见底。 */
function BalanceCell({ row }: { row: NewApiChannelRow }) {
  if (row.balanceMinorUnits === undefined) {
    return (
      <span className="text-fg-muted" title="NewAPI 上这个渠道没有配置余额，与「余额为 0」不是一回事">
        {UNCONFIGURED_BALANCE_TEXT}
      </span>
    );
  }
  return <span>{formatMinorUnits(row.balanceMinorUnits, row.currency)}</span>;
}

/** 错误率单元格：ppm 整数 → 百分比。
 *
 *  越过异常判据的渠道标红，但**停用的渠道不标**——它没在服务，谈不上出错
 *  （与契约层 ChannelStatus.Unhealthy() 同一条判断，两边必须一致，
 *  否则表里标红的条数和上面那句「异常 N」对不上）。 */
function ErrorRateCell({ row, thresholdPPM }: { row: NewApiChannelRow; thresholdPPM: unknown }) {
  const text = formatErrorRatePPM(row.errorRatePPM);
  // 走 toIntegerValue 而不是 `typeof === "number"` + BigInt（）：后者对
  // 超出安全整数范围的 JSON 数字会抛异常，把一整格页面炸掉。判据取不到时
  // 不标红——**宁可漏标也不错标**：一个错标成红色的健康渠道会让人去查
  // 一个并不存在的故障。
  const threshold = toIntegerValue(thresholdPPM);
  const unhealthy =
    row.enabled === true &&
    row.errorRatePPM !== null &&
    threshold !== null &&
    row.errorRatePPM >= threshold;

  return unhealthy ? (
    <span className="font-medium text-danger" title="错误率已越过异常判据">
      {text}
    </span>
  ) : (
    <span>{text}</span>
  );
}
