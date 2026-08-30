/** 运行保障 · 控制平面健康子页的展示口径（XM-OPS0）。
 *
 *  新鲜度状态本身（uninitialized/failed/stale/partial/fresh）的中文名与语气已经
 *  由 `@xingmang/ui-admin` 的 `describeFreshness` 定义过一次——`OpsFreshness` 与
 *  它认识的 `FreshnessContract` 是同一份后端契约，这里不重新抄一遍映射表，
 *  只补 ops 这一页特有的两样东西：
 *    - 采集链路「有效模式」（fake/real/模块未挂载）怎么说；
 *    - 六个异构来源（worker 心跳 / 两条采集链路 / 两个连接器健康 / 保留期清理）
 *      怎么拼成同一张表的行。
 *
 *  抽成纯函数是为了能直接断言这些规则本身：一张把「模拟数据」显示成和
 *  「真实对接」一样颜色的表，在截图上和正确实现长得一模一样，只有测试看得出来。 */
import type { BadgeTone } from "@xingmang/ui-primitives";
import type { OpsFreshness, OpsMetricSnapshot, OpsOverview, OpsSyncPipeline } from "../api/ops";

export interface OpsModeDisplay {
  label: string;
  tone: BadgeTone;
}

/** 采集链路「有效模式」的展示口径。
 *
 *  `config_available=false`（这个部署压根没挂载凭据模块）与「挂载了但还没
 *  配置」是两件不同的事，必须分开说——都显示成「未配置」会让人以为配一下
 *  就能用，实际上这个部署里根本没有这个模块可配。
 *
 *  fake 不能显示成和 real 一样的语气：fake 模式下渠道数据是模拟出来的，
 *  运营拿它去核对真实告警会得出错误结论。 */
export function describeSyncMode(
  pipeline: Pick<OpsSyncPipeline, "config_available" | "effective_mode">,
): OpsModeDisplay {
  if (!pipeline.config_available) {
    return { label: "凭据模块未挂载", tone: "neutral" };
  }
  if (pipeline.effective_mode === "real") {
    return { label: "真实对接", tone: "success" };
  }
  if (pipeline.effective_mode === "fake") {
    return { label: "模拟数据", tone: "warning" };
  }
  // 契约里 config_available=true 时 effective_mode 只会是 fake/real 之一；
  // 真出现第三个值就原样显示，不要假装认识它（未知值不静默吞掉）
  return { label: pipeline.effective_mode || "未知模式", tone: "warning" };
}

export interface ConnectorHealthValue {
  healthy: boolean | null;
  version: string;
  kind: string;
  latencyMs: number | null;
}

/** 从 `OpsMetricSnapshot.value`（未类型化的 JSON）里取连接器健康检查关心的
 *  四个字段。防御式读取——这个 value 随 metric_key 变形状，字段名或类型
 *  对不上时给安全默认值，不让一次意外的后端改动直接炸掉这一格。 */
export function readConnectorHealthValue(value: Record<string, unknown>): ConnectorHealthValue {
  return {
    healthy: typeof value.healthy === "boolean" ? value.healthy : null,
    version: typeof value.version === "string" ? value.version : "",
    kind: typeof value.kind === "string" ? value.kind : "",
    latencyMs: typeof value.latency_ms === "number" ? value.latency_ms : null,
  };
}

/** 控制平面组件健康表的一行。字段是纯数据，不含 JSX——展示（Badge、颜色）
 *  留给页面的列定义去做，这里只回答「这一行该显示什么内容」。 */
export interface OpsHealthRow {
  id: string;
  component: string;
  environment: string;
  freshness: OpsFreshness;
  /** 「依赖」列。没有可报告的依赖时为 "-"，不是空串——空串在表格里看着像加载中。 */
  dependency: string;
  /** 状态徽章旁的次要说明（采集模式 / 连接器健康摘要）；没有则为空串。 */
  note: string;
  noteTone: BadgeTone;
}

const NO_DEPENDENCY = "-";

/** 按位置取契约保证存在的数组元素；取不到说明后端违反了「始终 2 个元素」的
 *  契约。这时候选择抛错而不是静默错位（比如把 newapi 的数据画到 sub2api 那
 *  一行），参考 navigation.ts 的 navLabel：宁可让问题在这里显形，也不要把一个
 *  已经漂开的契约悄悄渲染成看起来正常的样子。 */
function requireItem<T>(items: readonly T[], index: number, what: string): T {
  const item = items[index];
  if (item === undefined) {
    throw new Error(
      `ops overview 响应违反契约：期望 ${what} 存在，实际只有 ${items.length} 条`,
    );
  }
  return item;
}

function pipelineRow(id: string, component: string, environment: string, pipeline: OpsSyncPipeline): OpsHealthRow {
  const mode = describeSyncMode(pipeline);
  return {
    id,
    component,
    environment,
    freshness: pipeline.freshness,
    dependency: pipeline.sample_metric_key || NO_DEPENDENCY,
    note: mode.label,
    noteTone: mode.tone,
  };
}

function connectorHealthRow(
  id: string,
  component: string,
  environment: string,
  snapshot: OpsMetricSnapshot,
): OpsHealthRow {
  const value = readConnectorHealthValue(snapshot.value);
  const note = [
    value.healthy === null ? null : value.healthy ? "健康" : "不健康",
    value.version ? `v${value.version}` : null,
  ]
    .filter((part): part is string => part !== null)
    .join(" · ");
  return {
    id,
    component,
    environment,
    freshness: snapshot.freshness,
    dependency: value.kind || NO_DEPENDENCY,
    note,
    // healthy 明确为 false 时用 danger；null（这个部署的健康检查没给这个字段）
    // 与 true 都不该染成红色——前者是「不知道」，不是「不健康」
    noteTone: value.healthy === false ? "danger" : "neutral",
  };
}

/** `OpsOverview` → 控制平面组件健康表的 6 行，顺序固定：
 *  worker 心跳 / sub2api 采集链路 / newapi 采集链路 / sub2api 连接器健康 /
 *  newapi 连接器健康 / 保留期清理。
 *
 *  环境统一取 `build.environment`：整份响应本来就是按单一环境查询出来的
 *  （environment 是请求的查询参数），六行没有各自不同的环境可言。 */
export function buildOpsHealthRows(data: OpsOverview): OpsHealthRow[] {
  const environment = data.build.environment;

  const sub2apiPipeline = requireItem(data.sync_pipelines, 0, "sync_pipelines[0]（sub2api）");
  const newapiPipeline = requireItem(data.sync_pipelines, 1, "sync_pipelines[1]（newapi）");
  const sub2apiHealth = requireItem(data.connector_health, 0, "connector_health[0]（sub2api）");
  const newapiHealth = requireItem(data.connector_health, 1, "connector_health[1]（newapi）");

  return [
    {
      id: "worker-heartbeat",
      component: "worker 心跳",
      environment,
      freshness: data.worker_heartbeat.freshness,
      dependency: NO_DEPENDENCY,
      note: "",
      noteTone: "neutral",
    },
    pipelineRow("sync-sub2api", "sub2api 采集链路", environment, sub2apiPipeline),
    pipelineRow("sync-newapi", "newapi 采集链路", environment, newapiPipeline),
    connectorHealthRow("connector-sub2api", "sub2api 连接器健康", environment, sub2apiHealth),
    connectorHealthRow("connector-newapi", "newapi 连接器健康", environment, newapiHealth),
    {
      id: "retention",
      component: "保留期清理",
      environment,
      freshness: data.retention.freshness,
      dependency: NO_DEPENDENCY,
      note: "",
      noteTone: "neutral",
    },
  ];
}
