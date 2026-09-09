/** 迷你趋势图的几何计算。
 *
 *  独立成模块而不是塞进组件里：路径字符串是纯函数的产物，能直接断言，
 *  不必先把 SVG 渲染出来再去数属性。零依赖——一个折线图不值得引入图表库
 *  （VERSIONS.lock 锁死依赖，规格 §5.3）。 */

/** 一个时间序列样本。 */
export interface SparkSample {
  /** 横轴位置：**采集时刻**（synced_at）的毫秒时间戳。
   *
   *  刻意不是「观测时刻」：同步失败时后端保留的是上一次成功的 observed_at，
   *  拿它当横轴会把连续几次失败全部堆到那个旧的成功点上，红色失败区间凭空消失。 */
  at: number;
  /** 主数值；null 表示这个样本没有可信数值。 */
  value: number | null;
  /** 这次同步是否失败。失败样本不参与折线，只在原位标一笔。 */
  failed: boolean;
  /** 这次观测是否只拿到了部分数据（后端 is_partial）。
   *
   *  部分数据可能偏小，画成一条正常实线等于宣称一个它没有的完整性
   *  （宪法 12 条），所以要在**线型**上与完整数据分开，而不是只换个颜色。 */
  partial?: boolean;
  /** 数据本身的时刻（observed_at）的毫秒时间戳；null/缺省表示上游没给。
   *
   *  只作为点的附加信息（悬停、摘要），**不参与横轴**——理由见 `at`。 */
  observedAt?: number | null;
}

/** 画布尺寸（用户坐标系，不是 CSS 像素）。 */
export interface SparklineBox {
  width: number;
  height: number;
  /** 内边距。线宽与端点标记要占地方，贴边画会被裁掉一半。 */
  padding: number;
}

export interface SparkPoint {
  x: number;
  y: number;
}

export interface SparklineGeometry {
  /** 完整数据折线段的 points 属性值。失败/缺值处折线断开，所以可能有多段。 */
  segments: string[];
  /** 触及部分数据点的折线段——调用方用虚线画，形状上与 segments 分开。 */
  partialSegments: string[];
  /** 失败样本的位置。 */
  failedPoints: SparkPoint[];
  /** 部分数据样本的位置（用于叠一个独立标记）。 */
  partialPoints: SparkPoint[];
  /** 最后一个成功样本的位置，用来标出「当前值」落在哪。 */
  lastPoint: SparkPoint | null;
}

/** 少于两个成功样本就没有「趋势」可言——一个点连不成线，
 *  画一条水平线只会让人以为「很平稳」，那是编出来的结论。 */
export const MIN_TREND_POINTS = 2;

/** 默认画布。宽高比按「一眼看形状」选，不追求精读数值（精确值在卡片主位）。 */
export const DEFAULT_SPARKLINE_BOX: SparklineBox = { width: 240, height: 40, padding: 4 };

/** 保留两位小数：路径字符串因此稳定可断言，也不会长出一串浮点尾巴。 */
function round2(n: number): number {
  return Math.round(n * 100) / 100;
}

function isPlottable(s: SparkSample): boolean {
  return !s.failed && s.value !== null && Number.isFinite(s.value);
}

function numericValue(s: SparkSample): number | null {
  return s.value !== null && Number.isFinite(s.value) ? s.value : null;
}

/** 能画进折线的样本数（失败样本不算——失败时后端带回的是上一次成功的值，
 *  把它当成一次新观测画上去就是伪造了一个读数）。 */
export function plottableCount(samples: SparkSample[]): number {
  return samples.filter(isPlottable).length;
}

function fmt(p: SparkPoint): string {
  return `${p.x},${p.y}`;
}

/** 计算折线几何；样本不足以构成趋势时返回 null，由调用方显示占位文案。
 *
 *  纵轴定义域取**全部**有数值的样本（含失败样本带回的旧值），这样失败标记
 *  一定落在框内，不需要钳制——钳制会把一个越界的值画在边上，看起来像正常读数。 */
export function buildSparkline(
  samples: SparkSample[],
  box: SparklineBox = DEFAULT_SPARKLINE_BOX,
): SparklineGeometry | null {
  if (plottableCount(samples) < MIN_TREND_POINTS) return null;

  // 契约说升序，这里再排一次：真收到乱序数据，画出来是锯齿状的纯噪音，
  // 而排序的代价只有一次 O(n log n)
  const ordered = [...samples].sort((a, b) => a.at - b.at);

  const innerW = Math.max(0, box.width - box.padding * 2);
  const innerH = Math.max(0, box.height - box.padding * 2);

  const times = ordered.map((s) => s.at);
  const tMin = Math.min(...times);
  const tMax = Math.max(...times);
  // 时间戳全相同（上游没给观测时刻）时退化成按序号等距排布：
  // 除以 0 会得到 NaN，而 NaN 进了路径字符串整条线都不会渲染
  const xOf = (s: SparkSample, index: number): number =>
    tMax === tMin
      ? box.padding + (ordered.length === 1 ? innerW / 2 : (index / (ordered.length - 1)) * innerW)
      : box.padding + ((s.at - tMin) / (tMax - tMin)) * innerW;

  const values = ordered.map(numericValue).filter((v): v is number => v !== null);
  const vMin = Math.min(...values);
  const vMax = Math.max(...values);
  // 值恒定时画在垂直中线：贴着上沿或下沿会被误读成「顶到极值了」
  const yOf = (v: number): number =>
    vMax === vMin ? box.padding + innerH / 2 : box.padding + (1 - (v - vMin) / (vMax - vMin)) * innerH;

  const segments: string[] = [];
  const partialSegments: string[] = [];
  const failedPoints: SparkPoint[] = [];
  const partialPoints: SparkPoint[] = [];
  let lastPoint: SparkPoint | null = null;

  // 当前这一段连续可画的样本（失败/缺值处断开）
  let run: Array<{ point: SparkPoint; partial: boolean }> = [];

  const pushPiece = (from: number, to: number, partial: boolean) => {
    const points = run
      .slice(from, to + 1)
      .map((r) => fmt(r.point))
      .join(" ");
    (partial ? partialSegments : segments).push(points);
  };

  // 孤立的成功样本（前后都是失败）也要留下痕迹：单点 polyline 什么都不画，
  // 所以把坐标写两遍，配合 round linecap 渲染成一个点。丢掉它等于宣称
  // 那个时刻没有采集成功过
  const flush = () => {
    if (run.length === 0) return;
    if (run.length === 1) {
      const only = run[0]!;
      const dot = `${fmt(only.point)} ${fmt(only.point)}`;
      (only.partial ? partialSegments : segments).push(dot);
      run = [];
      return;
    }
    // 逐小段判线型：只要一端是部分数据，这一小段就归到虚线里去。
    // 相邻同型的小段合并成一条 polyline，于是「没有部分数据」时路径字符串
    // 与拆分之前逐字一致——线型是新增的信息，不是把老图重画一遍
    let start = 0;
    let flag = run[0]!.partial || run[1]!.partial;
    for (let i = 1; i < run.length; i++) {
      const piecePartial = run[i - 1]!.partial || run[i]!.partial;
      if (piecePartial !== flag) {
        pushPiece(start, i - 1, flag);
        start = i - 1;
        flag = piecePartial;
      }
    }
    pushPiece(start, run.length - 1, flag);
    run = [];
  };

  ordered.forEach((s, index) => {
    const x = round2(xOf(s, index));
    const v = numericValue(s);

    if (isPlottable(s) && v !== null) {
      const point = { x, y: round2(yOf(v)) };
      const partial = s.partial === true;
      run.push({ point, partial });
      if (partial) partialPoints.push(point);
      lastPoint = point;
      return;
    }

    // 失败或缺值：折线在此断开。断开本身就是信息——
    // 把两端直接连起来等于宣称中间那段数据是连续的
    flush();
    if (v !== null) failedPoints.push({ x, y: round2(yOf(v)) });
  });
  flush();

  return { segments, partialSegments, failedPoints, partialPoints, lastPoint };
}

// --- 文字摘要（无障碍） ---

/** 折线的方向。样本不足以判断时是 unknown，不硬凑一个「平稳」。 */
export type SparkDirection = "up" | "down" | "flat" | "unknown";

export interface SparkSummary {
  /** 样本总数（含失败样本）。 */
  total: number;
  /** 能画进折线的样本数。 */
  plottable: number;
  /** 同步失败的样本数。 */
  failed: number;
  /** 只拿到部分数据的样本数。 */
  partial: number;
  direction: SparkDirection;
}

export function summarizeSparkline(samples: SparkSample[]): SparkSummary {
  const plottable = samples.filter(isPlottable);
  const first = plottable[0]?.value ?? null;
  const last = plottable[plottable.length - 1]?.value ?? null;
  const direction: SparkDirection =
    plottable.length < MIN_TREND_POINTS || first === null || last === null
      ? "unknown"
      : last > first
        ? "up"
        : last < first
          ? "down"
          : "flat";
  return {
    total: samples.length,
    plottable: plottable.length,
    failed: samples.filter((s) => s.failed).length,
    partial: samples.filter((s) => s.partial === true).length,
    direction,
  };
}

const DIRECTION_TEXT: Record<SparkDirection, string> = {
  up: "整体上升",
  down: "整体下降",
  flat: "整体持平",
  unknown: "样本不足以判断方向",
};

/** 折线的文字摘要。
 *
 *  折线本身读不出来，屏幕阅读器只能靠这句话知道图上发生了什么。方向、失败数、
 *  部分数据数三样都要说：它们恰好是「图看起来正常、数据其实不正常」的三种情形。 */
export function describeSparkline(samples: SparkSample[]): string {
  const s = summarizeSparkline(samples);
  const parts = [DIRECTION_TEXT[s.direction], `共 ${s.total} 个采样点`];
  if (s.failed > 0) parts.push(`${s.failed} 次同步失败`);
  if (s.partial > 0) parts.push(`含 ${s.partial} 个部分数据点`);
  return parts.join("，");
}

/** 图上必须用文字说清的那部分：失败与部分数据。没有可说的就返回空串。
 *
 *  与 describeSparkline 分开：这一句要**看得见**。只靠颜色（红色断点、
 *  黄色虚线）区分完整性，色觉障碍者什么也读不到（规格 §7.7 无障碍）。 */
export function sparklineCaveats(samples: SparkSample[]): string {
  const s = summarizeSparkline(samples);
  const parts: string[] = [];
  if (s.failed > 0) parts.push(`${s.failed} 次同步失败（折线在此断开）`);
  if (s.partial > 0) parts.push(`含 ${s.partial} 个部分数据点（虚线，数值可能偏小）`);
  return parts.join("；");
}
