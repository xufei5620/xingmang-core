import { describe, expect, it } from "vitest";
import {
  buildServiceCreateParams,
  defaultWatermark,
  EMPTY_SERVICE_FORM,
  hasErrors,
  SERVICE_STATUS_OPTIONS,
  validateObserveForm,
  validateServiceForm,
  type ServiceFormValues,
} from "./serviceForm";

function form(over: Partial<ServiceFormValues> = {}): ServiceFormValues {
  return {
    ...EMPTY_SERVICE_FORM,
    service_type: "sub2api",
    instance_id: "sub2api-dev",
    endpoint: "https://sub2api.example.com",
    owner: "平台组",
    ...over,
  };
}

describe("validateServiceForm：必填项", () => {
  it("空表单四个必填项全部报错", () => {
    const errors = validateServiceForm(EMPTY_SERVICE_FORM);
    expect(Object.keys(errors).sort()).toEqual([
      "endpoint",
      "instance_id",
      "owner",
      "service_type",
    ]);
    expect(hasErrors(errors)).toBe(true);
  });

  it("只有空白字符不算填了——末尾一个空格换来一个 400，人还得自己数空格", () => {
    const errors = validateServiceForm(form({ owner: "   " }));
    expect(errors.owner).toContain("必填");
  });

  it("填全了就没有错误", () => {
    expect(validateServiceForm(form())).toEqual({});
    expect(hasErrors({})).toBe(false);
  });
});

describe("validateServiceForm：标识符（对齐 registry.ValidateIdentifier）", () => {
  it("大写、下划线、点号都不合法", () => {
    for (const bad of ["Sub2API", "sub2_api", "sub2.api", "-sub2api", "子系统"]) {
      expect(validateServiceForm(form({ service_type: bad })).service_type).toBeDefined();
    }
  });

  it("小写字母数字与短横线合法，且允许纯数字开头", () => {
    for (const good of ["sub2api", "s", "9lives", "a-b-c"]) {
      expect(validateServiceForm(form({ instance_id: good })).instance_id).toBeUndefined();
    }
  });

  it("超过 64 位不合法（后端正则上限就是 64）", () => {
    expect(validateServiceForm(form({ instance_id: "a".repeat(64) })).instance_id).toBeUndefined();
    expect(validateServiceForm(form({ instance_id: "a".repeat(65) })).instance_id).toBeDefined();
  });
});

describe("validateServiceForm：地址协议（对齐 registry.Service.Validate）", () => {
  it("对外地址必须 https —— 明文 http 会把数据裸奔出去", () => {
    expect(validateServiceForm(form({ endpoint: "http://x.example.com" })).endpoint).toContain(
      "https://",
    );
    expect(validateServiceForm(form({ endpoint: "x.example.com" })).endpoint).toBeDefined();
  });

  it("内网地址 http/https 都行，但不能是别的协议", () => {
    expect(
      validateServiceForm(form({ internal_endpoint: "http://10.0.0.1" })).internal_endpoint,
    ).toBeUndefined();
    expect(
      validateServiceForm(form({ internal_endpoint: "ftp://10.0.0.1" })).internal_endpoint,
    ).toBeDefined();
  });

  it("内网地址留空不校验——它是可选项", () => {
    expect(validateServiceForm(form({ internal_endpoint: "" })).internal_endpoint).toBeUndefined();
  });
});

describe("buildServiceCreateParams", () => {
  it("environment 来自身份而不是表单，必填五项齐全", () => {
    const params = buildServiceCreateParams(form(), "staging");
    expect(params).toEqual({
      service_type: "sub2api",
      instance_id: "sub2api-dev",
      environment: "staging",
      endpoint: "https://sub2api.example.com",
      owner: "平台组",
    });
  });

  it("空的可选字段直接不传——传空串会让「没填」和「填了个空」在库里一模一样", () => {
    const params = buildServiceCreateParams(form({ health_check_path: "" }), "development");
    expect("health_check_path" in params).toBe(false);
  });

  it("填了的可选字段带上，并且顺手 trim", () => {
    const params = buildServiceCreateParams(
      form({ runbook_path: "  docs/runbook.md  ", native_console_url: "https://c.example.com" }),
      "development",
    );
    expect(params.runbook_path).toBe("docs/runbook.md");
    expect(params.native_console_url).toBe("https://c.example.com");
  });
});

describe("defaultWatermark", () => {
  it("形如 wm-<UTC 紧凑时间戳>，可读且可排序", () => {
    expect(defaultWatermark(new Date("2026-08-27T10:15:30.123Z"))).toBe("wm-20260827T101530Z");
  });

  it("取的是 UTC 而不是本地时间（宪法 14 条：库内一律 UTC）", () => {
    const wm = defaultWatermark(new Date(Date.UTC(2026, 0, 2, 3, 4, 5)));
    expect(wm).toBe("wm-20260102T030405Z");
  });

  it("字符集落在后端标识符友好的范围内，不含冒号与短横线以外的符号", () => {
    expect(defaultWatermark(new Date())).toMatch(/^wm-\d{8}T\d{6}Z$/);
  });
});

describe("validateObserveForm", () => {
  it("水位必填", () => {
    expect(validateObserveForm({ watermark: "  ", status: "active" }).watermark).toBe("水位必填");
  });

  it("状态只接受后端枚举里的三个值", () => {
    for (const option of SERVICE_STATUS_OPTIONS) {
      expect(validateObserveForm({ watermark: "wm-1", status: option.value }).status).toBeUndefined();
    }
    expect(validateObserveForm({ watermark: "wm-1", status: "unknown" }).status).toBeDefined();
    expect(validateObserveForm({ watermark: "wm-1", status: "" }).status).toBeDefined();
  });

  it("枚举与 registry.ServiceStatus 完全一致", () => {
    expect(SERVICE_STATUS_OPTIONS.map((o) => o.value)).toEqual([
      "active",
      "degraded",
      "retired",
    ]);
  });
});
