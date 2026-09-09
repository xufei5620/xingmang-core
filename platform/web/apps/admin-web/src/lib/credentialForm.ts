/** 字段名与页面控件保持一一对应，避免把「引用」和「值」混成一个输入框。 */
export type CredentialFormField = "credentialRef" | "secretValue";

export interface CredentialFormValues {
  credentialRef: string;
  /** 仅在提交前存于内存；成功或取消后由页面清空。 */
  secretValue: string;
}

export type CredentialFormErrors = Partial<Record<CredentialFormField, string>>;

export const EMPTY_CREDENTIAL_FORM: CredentialFormValues = {
  credentialRef: "",
  secretValue: "",
};

const REF_PART = "[a-z0-9][a-z0-9-]{0,63}";
const CREDENTIAL_REF_PATTERN = new RegExp(`^secret://(${REF_PART})/(${REF_PART})$`);

/** 只校验 CredentialRef 的形状，不触碰或记录秘密值。 */
export function parseCredentialRef(value: string): { ref: string; scope: string; name: string } | null {
  const ref = value.trim();
  const match = CREDENTIAL_REF_PATTERN.exec(ref);
  if (!match) return null;
  return { ref, scope: match[1] ?? "", name: match[2] ?? "" };
}

export function validateCredentialForm(values: CredentialFormValues): CredentialFormErrors {
  const errors: CredentialFormErrors = {};
  const trimmedRef = values.credentialRef.trim();
  if (!trimmedRef.startsWith("secret://")) {
    errors.credentialRef = "凭据引用必须是 secret://<scope>/<name>";
  } else if (!parseCredentialRef(trimmedRef)) {
    errors.credentialRef = "凭据引用格式不合法：scope 与 name 只能使用小写字母、数字和连字符";
  }
  if (!values.secretValue.trim()) {
    errors.secretValue = "请粘贴凭据值；保存后不会再次显示";
  }
  return errors;
}

/** 构造 UI 层的 Action 输入；引用去首尾空白，凭据值逐字保留。 */
export function buildCredentialParams(values: CredentialFormValues): {
  credentialRef: string;
  secretValue: string;
} {
  return {
    credentialRef: values.credentialRef.trim(),
    secretValue: values.secretValue,
  };
}

export function credentialFieldLabel(field: CredentialFormField): string {
  return field === "credentialRef" ? "CredentialRef" : "凭据值";
}

export function validateRevokeReason(reason: string): string | undefined {
  return reason.trim() ? undefined : "请填写吊销原因，便于审计回溯";
}
