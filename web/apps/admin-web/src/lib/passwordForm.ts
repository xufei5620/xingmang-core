/** 改密页（/account/password）的表单值与校验（XM-LOGIN local 模式）。 */

export interface ChangePasswordFormValues {
  currentPassword: string;
  newPassword: string;
  confirmPassword: string;
}

export type ChangePasswordFormField = keyof ChangePasswordFormValues;

export type ChangePasswordFormErrors = Partial<Record<ChangePasswordFormField, string>>;

export const EMPTY_CHANGE_PASSWORD_FORM: ChangePasswordFormValues = {
  currentPassword: "",
  newPassword: "",
  confirmPassword: "",
};

/** 与后端契约一致：新密码至少 10 位。真正的强度裁决仍在服务端。 */
export const MIN_PASSWORD_LENGTH = 10;

const FIELD_LABELS: Record<ChangePasswordFormField, string> = {
  currentPassword: "当前密码",
  newPassword: "新密码",
  confirmPassword: "确认新密码",
};

export function changePasswordFieldLabel(field: ChangePasswordFormField): string {
  return FIELD_LABELS[field];
}

export function validateChangePasswordForm(
  values: ChangePasswordFormValues,
): ChangePasswordFormErrors {
  const errors: ChangePasswordFormErrors = {};

  if (!values.currentPassword) {
    errors.currentPassword = "必填";
  }

  if (!values.newPassword) {
    errors.newPassword = "必填";
  } else if (values.newPassword.length < MIN_PASSWORD_LENGTH) {
    errors.newPassword = `至少 ${MIN_PASSWORD_LENGTH} 位`;
  } else if (values.currentPassword && values.newPassword === values.currentPassword) {
    errors.newPassword = "新密码不能与当前密码相同";
  }

  if (!values.confirmPassword) {
    errors.confirmPassword = "必填";
  } else if (values.confirmPassword !== values.newPassword) {
    errors.confirmPassword = "两次输入的新密码不一致";
  }

  return errors;
}

export function hasChangePasswordFormErrors(errors: ChangePasswordFormErrors): boolean {
  return Object.keys(errors).length > 0;
}
