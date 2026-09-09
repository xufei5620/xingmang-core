import { describe, expect, it } from "vitest";
import {
  EMPTY_CHANGE_PASSWORD_FORM,
  MIN_PASSWORD_LENGTH,
  hasChangePasswordFormErrors,
  validateChangePasswordForm,
  type ChangePasswordFormValues,
} from "./passwordForm";

const valid: ChangePasswordFormValues = {
  currentPassword: "old-password-1",
  newPassword: "new-password-12",
  confirmPassword: "new-password-12",
};

describe("validateChangePasswordForm", () => {
  it("合法表单没有错误", () => {
    expect(hasChangePasswordFormErrors(validateChangePasswordForm(valid))).toBe(false);
  });

  it("空表单：三个字段都必填", () => {
    const errors = validateChangePasswordForm(EMPTY_CHANGE_PASSWORD_FORM);
    expect(errors.currentPassword).toBeTruthy();
    expect(errors.newPassword).toBeTruthy();
    expect(errors.confirmPassword).toBeTruthy();
  });

  it(`新密码短于 ${MIN_PASSWORD_LENGTH} 位：报错`, () => {
    const errors = validateChangePasswordForm({
      ...valid,
      newPassword: "short",
      confirmPassword: "short",
    });
    expect(errors.newPassword).toBeTruthy();
  });

  it("新密码与当前密码相同：报错", () => {
    const errors = validateChangePasswordForm({
      ...valid,
      newPassword: valid.currentPassword,
      confirmPassword: valid.currentPassword,
    });
    expect(errors.newPassword).toBeTruthy();
  });

  it("确认密码与新密码不一致：报错", () => {
    const errors = validateChangePasswordForm({ ...valid, confirmPassword: "different-password" });
    expect(errors.confirmPassword).toBeTruthy();
  });
});
