import { describe, expect, it } from "vitest";

import { serviceUnitDefinitions } from "./eligibility-wire.generated";
import {
  convertServiceUnits,
  serviceUnitDefinition,
  serviceUnitView,
  unconvertibleAmountNote,
  unconvertibleDisplayText,
  unitContractPendingNote,
  unrecognisedUnitNote,
  type ServiceUnitDefinition,
} from "./service-units";

// XM-INV-UNIT-DISPLAY。这一组盯的是「显示出来的那个数对不对」，所以断言全部写成
// 具体的字符串，而不是「有值 / 不抛错」。

const sub2api = serviceUnitDefinition("SUB2_BALANCE_1E8")!;
const newapi = serviceUnitDefinition("NEWAPI_QUOTA")!;

describe("单位定义来自生成的契约", () => {
  it("两个单位码都在生成表里，且除数是字符串", () => {
    expect(serviceUnitDefinitions.map((unit) => unit.code)).toEqual([
      "SUB2_BALANCE_1E8",
      "NEWAPI_QUOTA",
    ]);
    for (const unit of serviceUnitDefinitions) {
      // 除数写成数字就是 float64，这里正是不许失真的地方。
      expect(typeof unit.divisor).toBe("string");
      expect(unit.divisor).toMatch(/^[1-9][0-9]*$/);
      expect(unit.displayLabel.length).toBeGreaterThan(0);
    }
  });

  it("认不出来的码返回 undefined，而不是随便给一个", () => {
    expect(serviceUnitDefinition("ZZ_MYSTERY_UNIT")).toBeUndefined();
    expect(serviceUnitDefinition(null)).toBeUndefined();
    expect(serviceUnitDefinition("")).toBeUndefined();
  });
});

describe("换算：已核实的两个数据点", () => {
  it("生产核实点：99,948,771,408 单位显示成 999.49", () => {
    expect(convertServiceUnits("99948771408", sub2api)).toBe("999.49");
  });

  it("派工单示例：30,179,629,498 单位显示成 301.80", () => {
    expect(convertServiceUnits("30179629498", sub2api)).toBe("301.80");
  });
});

describe("换算：取舍方向", () => {
  it("恰好半分时进位，不截断", () => {
    // 500000 / 1e8 = 0.005。截断会得 0.00，四舍五入得 0.01。
    expect(convertServiceUnits("500000", sub2api)).toBe("0.01");
  });

  it("差一个单位就不进位", () => {
    expect(convertServiceUnits("499999", sub2api)).toBe("0.00");
  });

  it("进位可以跨过整数位", () => {
    // 0.99999999 -> 1.00，而不是 0.100 之类的拼接事故。
    expect(convertServiceUnits("99999999", sub2api)).toBe("1.00");
  });

  it("零和一都给出两位小数", () => {
    expect(convertServiceUnits("0", sub2api)).toBe("0.00");
    expect(convertServiceUnits("1", sub2api)).toBe("0.00");
  });

  it("整除时不产生多余小数", () => {
    expect(convertServiceUnits("100000000", sub2api)).toBe("1.00");
    expect(convertServiceUnits("250000000", sub2api)).toBe("2.50");
  });
});

describe("换算：New API 的除数是 QuotaPerUnit", () => {
  it("500000 quota = 1.00", () => {
    expect(convertServiceUnits("500000", newapi)).toBe("1.00");
  });

  it("750000 quota = 1.50", () => {
    expect(convertServiceUnits("750000", newapi)).toBe("1.50");
  });

  it("一个 quota 折不出一分钱，显示 0.00 而不是空", () => {
    expect(convertServiceUnits("1", newapi)).toBe("0.00");
  });
});

describe("换算：大整数不走浮点", () => {
  // 这一组的输入是挑过的。第一版用了 23 位的 12345678901234567890123，
  // 它在「改用 Number」的变异下**照样绿**：丢掉的精度落在显示位数以下，
  // 两条路给出同一个 123,456,789,012,345.68。也就是说那条断言当时是靠
  // 「恰好看不出来」成立的，不是靠实现对。换成下面这些输入后，同一个变异变红。
  it("26 位服务单位逐位精确（这个长度下 Number 会显示出别的数）", () => {
    // 12345678901234567890123456 / 1e8 = 123456789012345678.90123456
    expect(convertServiceUnits("12345678901234567890123456", sub2api)).toBe(
      "123,456,789,012,345,678.90",
    );
  });

  it("同一个输入用 Number 算会算出别的数——这就是不许改用 Number 的理由", () => {
    const viaNumber = (
      Number("12345678901234567890123456") / 1e8
    ).toFixed(2);
    expect(viaNumber).not.toBe("123456789012345678.90");
  });

  it("全 9 的输入进位成整数，浮点路径会差一点点", () => {
    expect(convertServiceUnits("99999999999999999999999", sub2api)).toBe(
      "1,000,000,000,000,000.00",
    );
  });

  it("78 位输入也不丢位", () => {
    const units = "9".repeat(78);
    const converted = convertServiceUnits(units, sub2api)!;
    // 78 个 9 除以 1e8 后进位成 1 后面 70 个 0，两位小数为 .00。
    expect(converted.replace(/,/g, "")).toBe("1" + "0".repeat(70) + ".00");
  });
});

describe("换算：千分位与小数位数", () => {
  it("整数部分加千分位，小数部分不加", () => {
    expect(convertServiceUnits("123456789012345", sub2api)).toBe("1,234,567.89");
  });

  it("decimals 为 0 时不带小数点", () => {
    const whole: ServiceUnitDefinition = {
      code: "SUB2_BALANCE_1E8",
      divisor: "100000000",
      decimals: 0,
      displayLabel: "整数单位",
    };
    expect(convertServiceUnits("250000000", whole)).toBe("3");
    expect(convertServiceUnits("240000000", whole)).toBe("2");
  });
});

describe("换算：换不出来时返回 null，不返回差不多的数", () => {
  it.each(["", "-1", "1.5", "abc", "1e8", " 100", "100 "])(
    "拒绝非整数输入 %p",
    (units) => {
      expect(convertServiceUnits(units, sub2api)).toBeNull();
    },
  );

  it("除数不合法时也拒绝", () => {
    const broken: ServiceUnitDefinition = {
      code: "SUB2_BALANCE_1E8",
      divisor: "0",
      decimals: 2,
      displayLabel: "坏除数",
    };
    expect(convertServiceUnits("100", broken)).toBeNull();
  });

  it("小数位数超出契约范围时也拒绝", () => {
    const broken: ServiceUnitDefinition = {
      code: "SUB2_BALANCE_1E8",
      divisor: "100000000",
      decimals: 9,
      displayLabel: "坏位数",
    };
    expect(convertServiceUnits("100", broken)).toBeNull();
  });
});

describe("serviceUnitView：一格要显示什么", () => {
  it("认识的单位码：主显示是换算值，副标签是来源口径", () => {
    const view = serviceUnitView({
      serviceUnits: "30179629498",
      unitCode: "SUB2_BALANCE_1E8",
    });
    expect(view.amount).toBe("301.80");
    expect(view.note).toBe("SoloV API 余额");
    expect(view.converted).toBe(true);
  });

  // XM-INV-UNIT-DISPLAY-USERONLY。缺席型断言，所以先说清楚它靠什么成立：
  //
  //   * 扫的是**返回对象的每一个字符串字段**，不是手列 amount / note 两个名字。
  //     上一版的 title 就是这么长出来的；将来再加一个带原始值的字段，这条会自己
  //     变红，而不是安静地放过新字段。
  //   * 比数字时先把非数字字符去掉再比，所以不依赖千分位怎么分组——它不是
  //     groupThousands 的副本，那份逻辑改了也不会让这道断言悄悄失效。
  //   * 单位码比的是形状（全大写 + 下划线）而不是那两个已知码，多一个码照样拦得住。
  it("返回的任何字段里都没有原始刻度，也没有单位码", () => {
    const unitCodeShape = /[A-Z][A-Z0-9]*_[A-Z0-9_]+/;
    for (const value of [
      { serviceUnits: "30179629498", unitCode: "SUB2_BALANCE_1E8" },
      { serviceUnits: "99948771408", unitCode: "SUB2_BALANCE_1E8" },
      { serviceUnits: "500000", unitCode: "NEWAPI_QUOTA" },
      { serviceUnits: "30179629498", unitCode: "ZZ_MYSTERY_UNIT" },
      { serviceUnits: "30179629498", unitCode: null },
      { serviceUnits: "-5", unitCode: "SUB2_BALANCE_1E8" },
    ]) {
      const view = serviceUnitView(value);
      const fields = Object.values(view).filter(
        (field): field is string => typeof field === "string",
      );
      // 没有这一句，上面的 for 在字段全被删光时会一次都不跑，整条断言恒真。
      expect(fields.length).toBeGreaterThan(0);
      const rawDigits = value.serviceUnits.replace(/[^0-9]/g, "");
      for (const field of fields) {
        if (value.unitCode) expect(field).not.toContain(value.unitCode);
        expect(field).not.toMatch(unitCodeShape);
        // 短到几位的原始值没法跟换算结果区分（"5" 会撞上任何含 5 的数），所以只对
        // 真正长成「后台刻度」的值查包含关系；那也正是负责人指的那种数。
        if (rawDigits.length >= 6) {
          expect(field.replace(/[^0-9]/g, "")).not.toContain(rawDigits);
        }
      }
    }
  });

  it("换算结果不带货币符号——这两格不是人民币", () => {
    const view = serviceUnitView({
      serviceUnits: "30179629498",
      unitCode: "SUB2_BALANCE_1E8",
    });
    expect(view.amount).not.toMatch(/[¥$]/);
    expect(view.note).not.toMatch(/[¥$]/);
  });

  it("单位码为空：主显示「暂无法换算」，原因仍是「单位合同待建立」", () => {
    // 换算数没有，就不给数——旧版这里回落到原始数字（哪怕是 0），那条路已经关掉。
    const view = serviceUnitView({ serviceUnits: "30179629498", unitCode: null });
    expect(view.amount).toBe(unconvertibleDisplayText);
    expect(view.note).toBe(unitContractPendingNote);
    expect(view.converted).toBe(false);
  });

  it("未识别的单位码：主显示「暂无法换算」+（未识别单位），不抛错也不摊开原始值", () => {
    const view = serviceUnitView({
      serviceUnits: "30179629498",
      unitCode: "ZZ_MYSTERY_UNIT",
    });
    expect(view.amount).toBe(unconvertibleDisplayText);
    expect(view.note).toBe(unrecognisedUnitNote);
    expect(view.converted).toBe(false);
  });

  it("单位码认得但数字不合法：说的是数字的问题，不是单位的问题", () => {
    const view = serviceUnitView({
      serviceUnits: "-5",
      unitCode: "SUB2_BALANCE_1E8",
    });
    expect(view.amount).toBe(unconvertibleDisplayText);
    expect(view.note).toBe(unconvertibleAmountNote);
    expect(view.note).not.toBe(unrecognisedUnitNote);
    expect(view.converted).toBe(false);
  });

  it("三条降级分支的主显示是同一句，区别只在原因", () => {
    // 上面三条各自钉住一条分支的原因；这一条钉住「主显示不因分支而漏出别的东西」，
    // 否则将来只改其中一条分支回落到原始值，另外两条的断言并不会红。
    const degraded = [
      { serviceUnits: "30179629498", unitCode: null },
      { serviceUnits: "30179629498", unitCode: "ZZ_MYSTERY_UNIT" },
      { serviceUnits: "-5", unitCode: "SUB2_BALANCE_1E8" },
    ].map((value) => serviceUnitView(value));
    expect(degraded.map((view) => view.amount)).toEqual([
      unconvertibleDisplayText,
      unconvertibleDisplayText,
      unconvertibleDisplayText,
    ]);
    expect(new Set(degraded.map((view) => view.note)).size).toBe(3);
  });

  it("New API 的格子用 New API 的标签，不会串到 SoloV API 上", () => {
    const view = serviceUnitView({
      serviceUnits: "500000",
      unitCode: "NEWAPI_QUOTA",
    });
    expect(view.amount).toBe("1.00");
    expect(view.note).toBe("New API 额度");
  });
});
