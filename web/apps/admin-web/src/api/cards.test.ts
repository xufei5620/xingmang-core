import { describe, expect, it, vi } from "vitest";
import { ApiError, FeatureNotMountedError, type ApiClient } from "./client";
import { listCardTransactions, listCards } from "./cards";

// XM_CARDS_MODE=off 时后端整组不挂载卡片端点（router.go 的 d.Cards != nil），
// 而导航里的「卡片管理」是无条件显示的。不做这层翻译，未启用的环境点进去
// 看到的是一个泛型报错，读的人会以为是坏了而不是没开。
describe("卡片查询在未启用环境下的表现", () => {
  const unmounted = (): ApiClient => ({
    get: vi.fn().mockRejectedValue(new ApiError(404, "UNKNOWN", "请求失败（HTTP 404）")),
    post: vi.fn(),
  });

  it("列表：没有 error.code 的 404 转成 FeatureNotMountedError", async () => {
    const error = await listCards({}, unmounted()).then(
      () => null,
      (e: unknown) => e,
    );
    expect(error).toBeInstanceOf(FeatureNotMountedError);
    expect((error as FeatureNotMountedError).description).toContain("XM_CARDS_MODE=off");
  });

  it("流水：同样翻译，否则详情弹窗里也会露出泛型报错", async () => {
    const error = await listCardTransactions("CHRIS", "card_1", {}, unmounted()).then(
      () => null,
      (e: unknown) => e,
    );
    expect(error).toBeInstanceOf(FeatureNotMountedError);
  });

  // 带 error.code 的 404 是「这一条没找到」，与「整组没挂载」是两件事。
  // 混为一谈会把一次真实的查不到显示成「功能未启用」，让人去改配置。
  it("带 error.code 的 404 原样抛出，不误判成未启用", async () => {
    const notFound: ApiClient = {
      get: vi.fn().mockRejectedValue(new ApiError(404, "CARD_NOT_FOUND", "没有这张卡")),
      post: vi.fn(),
    };
    const error = await listCards({}, notFound).then(
      () => null,
      (e: unknown) => e,
    );
    expect(error).toBeInstanceOf(ApiError);
    expect(error).not.toBeInstanceOf(FeatureNotMountedError);
  });
});
