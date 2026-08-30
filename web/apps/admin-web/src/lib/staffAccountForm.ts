/** 「新建账号」对话框的表单值、校验与字段标签（XM-LOGIN 人员与权限）。
 *
 *  纯函数，不碰网络：Action 参数组装留在 api/staff.ts（createStaffAccount），
 *  这里只负责「用户能不能提交」。 */

export interface StaffAccountFormValues {
  username: string;
  displayName: string;
  roles: string[];
  /** 留空＝让服务端生成初始密码；填了就用这个值。 */
  initialPassword: string;
}

export type StaffAccountFormField = keyof StaffAccountFormValues;

export type StaffAccountFormErrors = Partial<Record<StaffAccountFormField, string>>;

export const EMPTY_STAFF_ACCOUNT_FORM: StaffAccountFormValues = {
  username: "",
  displayName: "",
  roles: [],
  initialPassword: "",
};

const FIELD_LABELS: Record<StaffAccountFormField, string> = {
  username: "用户名",
  displayName: "显示名",
  roles: "角色",
  initialPassword: "初始密码",
};

export function staffAccountFieldLabel(field: StaffAccountFormField): string {
  return FIELD_LABELS[field];
}

/** 字母、数字、点、下划线、短横线；3-64 位。与后端账号名的通常约束对齐——
 *  真正的裁决仍在服务端，这里只是提前把明显不合法的输入挡在提交之前。 */
const USERNAME_PATTERN = /^[A-Za-z0-9._-]{3,64}$/;

/** 与改密页一致的最短长度（XM-LOGIN 后端契约：初始密码同样至少 10 位）。 */
export const MIN_PASSWORD_LENGTH = 10;

export function validateStaffAccountForm(values: StaffAccountFormValues): StaffAccountFormErrors {
  const errors: StaffAccountFormErrors = {};

  const username = values.username.trim();
  if (!username) {
    errors.username = "必填";
  } else if (!USERNAME_PATTERN.test(username)) {
    errors.username = "只能包含字母、数字、点、下划线、短横线，长度 3-64 位";
  }

  if (!values.displayName.trim()) {
    errors.displayName = "必填";
  }

  if (values.roles.length === 0) {
    errors.roles = "至少选择一个角色";
  }

  if (values.initialPassword && values.initialPassword.length < MIN_PASSWORD_LENGTH) {
    errors.initialPassword = `至少 ${MIN_PASSWORD_LENGTH} 位；留空则由系统生成`;
  }

  return errors;
}

export function hasStaffAccountFormErrors(errors: StaffAccountFormErrors): boolean {
  return Object.keys(errors).length > 0;
}
