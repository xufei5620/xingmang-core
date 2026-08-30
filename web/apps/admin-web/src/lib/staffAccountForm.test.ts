import { describe, expect, it } from "vitest";
import {
  EMPTY_STAFF_ACCOUNT_FORM,
  MIN_PASSWORD_LENGTH,
  hasStaffAccountFormErrors,
  validateStaffAccountForm,
  type StaffAccountFormValues,
} from "./staffAccountForm";

const valid: StaffAccountFormValues = {
  username: "bob.chen",
  displayName: "Bob Chen",
  roles: ["staff"],
  initialPassword: "",
};

describe("validateStaffAccountForm", () => {
  it("合法表单（留空初始密码）没有错误", () => {
    expect(hasStaffAccountFormErrors(validateStaffAccountForm(valid))).toBe(false);
  });

  it("空表单：用户名/显示名/角色三处都报错，初始密码留空合法不报错", () => {
    const errors = validateStaffAccountForm(EMPTY_STAFF_ACCOUNT_FORM);
    expect(errors.username).toBeTruthy();
    expect(errors.displayName).toBeTruthy();
    expect(errors.roles).toBeTruthy();
    expect(errors.initialPassword).toBeUndefined();
  });

  it("用户名含非法字符（空格/感叹号）：报错", () => {
    const errors = validateStaffAccountForm({ ...valid, username: "bob chen!" });
    expect(errors.username).toBeTruthy();
  });

  it("用户名短于 3 位：报错", () => {
    const errors = validateStaffAccountForm({ ...valid, username: "ab" });
    expect(errors.username).toBeTruthy();
  });

  it(`初始密码填了但短于 ${MIN_PASSWORD_LENGTH} 位：报错`, () => {
    const errors = validateStaffAccountForm({ ...valid, initialPassword: "short" });
    expect(errors.initialPassword).toBeTruthy();
  });

  it(`初始密码达到 ${MIN_PASSWORD_LENGTH} 位：不报错`, () => {
    const errors = validateStaffAccountForm({
      ...valid,
      initialPassword: "a".repeat(MIN_PASSWORD_LENGTH),
    });
    expect(errors.initialPassword).toBeUndefined();
  });

  it("角色为空数组：报错", () => {
    const errors = validateStaffAccountForm({ ...valid, roles: [] });
    expect(errors.roles).toBeTruthy();
  });
});
