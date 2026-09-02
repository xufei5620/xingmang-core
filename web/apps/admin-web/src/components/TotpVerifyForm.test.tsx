import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { TotpVerifyForm } from "./TotpVerifyForm";

const mapError = () => ({ message: "验证失败，请重试。", expired: false });

describe("TotpVerifyForm（XM-AUTH-TOTP0 抽出，供 XM-INVCON1 断言步进复用）", () => {
  it("默认展示动态码输入，提交前只允许 6 位数字", () => {
    const onVerify = vi.fn().mockResolvedValue(undefined);
    render(<TotpVerifyForm description="请输入 6 位动态码。" onVerify={onVerify} mapError={mapError} />);
    expect(screen.getByLabelText(/动态码/)).not.toBeNull();
    expect(screen.queryByLabelText(/恢复码/)).toBeNull();
    expect((screen.getByRole("button", { name: "验证" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("输满 6 位数字后提交，onVerify 收到 {code}", async () => {
    const onVerify = vi.fn().mockResolvedValue(undefined);
    render(<TotpVerifyForm description="desc" onVerify={onVerify} mapError={mapError} />);
    fireEvent.change(screen.getByLabelText(/动态码/), { target: { value: "123456" } });
    fireEvent.click(screen.getByRole("button", { name: "验证" }));
    await vi.waitFor(() => expect(onVerify).toHaveBeenCalledWith({ code: "123456" }));
  });

  it("非数字字符被过滤，超过 6 位被截断", () => {
    render(<TotpVerifyForm description="desc" onVerify={vi.fn()} mapError={mapError} />);
    fireEvent.change(screen.getByLabelText(/动态码/), { target: { value: "12a3456789" } });
    expect((screen.getByLabelText(/动态码/) as HTMLInputElement).value).toBe("123456");
  });

  it("「改用恢复码」切换到恢复码输入，提交时 onVerify 收到 {recoveryCode}", async () => {
    const onVerify = vi.fn().mockResolvedValue(undefined);
    render(<TotpVerifyForm description="desc" onVerify={onVerify} mapError={mapError} />);
    fireEvent.click(screen.getByRole("button", { name: "改用恢复码" }));
    expect(screen.queryByLabelText(/^动态码/)).toBeNull();
    fireEvent.change(screen.getByLabelText(/恢复码/), { target: { value: "abcde-fghij" } });
    fireEvent.click(screen.getByRole("button", { name: "验证" }));
    await vi.waitFor(() => expect(onVerify).toHaveBeenCalledWith({ recoveryCode: "abcde-fghij" }));
  });

  it("onVerify 抛错：就地显示 mapError 给出的文案", async () => {
    const onVerify = vi.fn().mockRejectedValue(new Error("boom"));
    render(
      <TotpVerifyForm
        description="desc"
        onVerify={onVerify}
        mapError={() => ({ message: "验证码不正确。", expired: false })}
      />,
    );
    fireEvent.change(screen.getByLabelText(/动态码/), { target: { value: "123456" } });
    fireEvent.click(screen.getByRole("button", { name: "验证" }));
    expect(await screen.findByRole("alert")).toHaveProperty("textContent", "验证码不正确。");
  });

  it("mapError 判定为已过期且提供了 onExpired：调用 onExpired 而不是就地显示错误", async () => {
    const onExpired = vi.fn();
    const onVerify = vi.fn().mockRejectedValue(new Error("expired"));
    render(
      <TotpVerifyForm
        description="desc"
        onVerify={onVerify}
        mapError={() => ({ message: "已过期", expired: true })}
        onExpired={onExpired}
      />,
    );
    fireEvent.change(screen.getByLabelText(/动态码/), { target: { value: "123456" } });
    fireEvent.click(screen.getByRole("button", { name: "验证" }));
    await vi.waitFor(() => expect(onExpired).toHaveBeenCalledTimes(1));
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("自定义 title/submitLabel 生效", () => {
    render(
      <TotpVerifyForm
        title="需要二次验证"
        description="desc"
        submitLabel="验证并继续"
        onVerify={vi.fn()}
        mapError={mapError}
      />,
    );
    expect(screen.getByText("需要二次验证")).not.toBeNull();
    expect(screen.getByRole("button", { name: "验证并继续" })).not.toBeNull();
  });
});
