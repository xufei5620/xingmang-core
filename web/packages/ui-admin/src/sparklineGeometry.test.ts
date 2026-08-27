import { describe, expect, it } from "vitest";
import {
  buildSparkline,
  DEFAULT_SPARKLINE_BOX,
  MIN_TREND_POINTS,
  plottableCount,
  type SparkSample,
} from "./sparklineGeometry";

const BOX = { width: 100, height: 20, padding: 0 };

function ok(at: number, value: number): SparkSample {
  return { at, value, failed: false };
}

function failed(at: number, value: number | null): SparkSample {
  return { at, value, failed: true };
}

describe("plottableCount：只有成功样本能进折线", () => {
  it("失败样本与缺值样本都不算", () => {
    expect(plottableCount([ok(1, 1), failed(2, 5), failed(3, null), { at: 4, value: null, failed: false }])).toBe(1);
  });

  it("非有限数（NaN / Infinity）不算——画上去会让整条路径失效", () => {
    expect(plottableCount([ok(1, Number.NaN), ok(2, Number.POSITIVE_INFINITY), ok(3, 7)])).toBe(1);
  });
});

describe("buildSparkline：数据不足时不画图", () => {
  it("空序列返回 null", () => {
    expect(buildSparkline([], BOX)).toBeNull();
  });

  it("只有一个成功样本返回 null（一个点连不成趋势）", () => {
    expect(buildSparkline([ok(1, 10)], BOX)).toBeNull();
    expect(MIN_TREND_POINTS).toBe(2);
  });

  it("全是失败样本返回 null，哪怕它们带回了旧值", () => {
    expect(buildSparkline([failed(1, 10), failed(2, 20), failed(3, 30)], BOX)).toBeNull();
  });
});

describe("buildSparkline：坐标映射", () => {
  it("首尾贴住左右边界，最大值在上、最小值在下", () => {
    const g = buildSparkline([ok(0, 0), ok(50, 5), ok(100, 10)], BOX);
    expect(g?.segments).toEqual(["0,20 50,10 100,0"]);
  });

  it("padding 会把整条线收进画布内侧", () => {
    const g = buildSparkline([ok(0, 0), ok(100, 10)], { width: 100, height: 20, padding: 5 });
    expect(g?.segments).toEqual(["5,15 95,5"]);
  });

  it("值恒定时画在垂直中线，不贴上下沿（贴边会被读成顶到极值）", () => {
    const g = buildSparkline([ok(0, 7), ok(100, 7)], BOX);
    expect(g?.segments).toEqual(["0,10 100,10"]);
  });

  it("时间戳全相同时按序号等距排布，不产生 NaN", () => {
    const g = buildSparkline([ok(9, 0), ok(9, 5), ok(9, 10)], BOX);
    expect(g?.segments).toEqual(["0,20 50,10 100,0"]);
    expect(g?.segments[0]).not.toContain("NaN");
  });

  it("横轴按时间而不是序号：采集间隔不均时形状要如实反映", () => {
    // 三个样本落在 0 / 10 / 100，中间那点应该靠左而不是在正中
    const g = buildSparkline([ok(0, 0), ok(10, 10), ok(100, 0)], BOX);
    expect(g?.segments).toEqual(["0,20 10,0 100,20"]);
  });

  it("乱序输入按时间重排（把乱序画成锯齿只是噪音）", () => {
    const g = buildSparkline([ok(100, 10), ok(0, 0), ok(50, 5)], BOX);
    expect(g?.segments).toEqual(["0,20 50,10 100,0"]);
  });

  it("坐标保留两位小数，路径字符串稳定", () => {
    const g = buildSparkline([ok(0, 0), ok(1, 1), ok(3, 3)], BOX);
    expect(g?.segments[0]).toBe("0,20 33.33,13.33 100,0");
  });
});

describe("buildSparkline：失败样本断开折线", () => {
  it("失败样本处折线断成两段，失败点单独给出", () => {
    const g = buildSparkline([ok(0, 0), ok(25, 0), failed(50, 5), ok(75, 10), ok(100, 10)], BOX);
    expect(g?.segments).toEqual(["0,20 25,20", "75,0 100,0"]);
    expect(g?.failedPoints).toEqual([{ x: 50, y: 10 }]);
  });

  it("失败样本的旧值参与纵轴定义域，标记一定落在画布内", () => {
    const g = buildSparkline([ok(0, 0), ok(100, 1), failed(50, 10)], BOX);
    for (const p of g?.failedPoints ?? []) {
      expect(p.y).toBeGreaterThanOrEqual(0);
      expect(p.y).toBeLessThanOrEqual(BOX.height);
    }
  });

  it("没有数值的失败样本只断线，不画点（没有值就没有位置）", () => {
    const g = buildSparkline([ok(0, 0), failed(50, null), ok(100, 10)], BOX);
    expect(g?.failedPoints).toEqual([]);
    // 两端各剩一个孤立点，中间不连线——连起来等于宣称那段数据是连续的
    expect(g?.segments).toEqual(["0,20 0,20", "100,0 100,0"]);
  });

  it("被失败样本夹住的孤立成功样本仍留下一个点，不被丢弃", () => {
    const g = buildSparkline([ok(0, 0), failed(25, 0), ok(50, 5), failed(75, 5), ok(100, 10)], BOX);
    expect(g?.segments).toContain("50,10 50,10");
    expect(g?.failedPoints).toEqual([
      { x: 25, y: 20 },
      { x: 75, y: 10 },
    ]);
  });

  it("lastPoint 指向最后一个成功样本，而不是最后一个样本", () => {
    const g = buildSparkline([ok(0, 0), ok(50, 10), failed(100, 10)], BOX);
    expect(g?.lastPoint).toEqual({ x: 50, y: 0 });
  });
});

describe("默认画布", () => {
  it("导出的默认尺寸留了内边距，端点标记不会被裁掉", () => {
    expect(DEFAULT_SPARKLINE_BOX.padding).toBeGreaterThan(0);
    expect(DEFAULT_SPARKLINE_BOX.width).toBeGreaterThan(DEFAULT_SPARKLINE_BOX.height);
  });
});
