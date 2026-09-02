/** TOTP 相关表单的纯校验逻辑（XM-AUTH-TOTP0）。真正的口令强度/验证码裁决
 *  仍在服务端；这里只做"填得像不像"的前端提示，减少一次注定失败的往返。 */

/** 6 位纯数字动态码。 */
export function validateTotpCode(code: string): string | undefined {
  const trimmed = code.trim();
  if (!trimmed) return "必填";
  if (!/^\d{6}$/.test(trimmed)) return "应为 6 位数字";
  return undefined;
}

/** 恢复码：去掉展示用的空格/短横线后应为 10 个字符（与后端
 *  localauth.NormalizeRecoveryCode/recoveryCodeLen 同一形态）；具体是否
 *  真的匹配某一张未用过的码由服务端裁决，这里只挡明显填不对的情况。 */
export function validateRecoveryCode(code: string): string | undefined {
  const normalized = code.replace(/[\s-]/g, "");
  if (!normalized) return "必填";
  if (!/^[A-Za-z0-9]{10}$/.test(normalized)) return "恢复码格式不对，请检查是否抄写完整";
  return undefined;
}

/** 把恢复码格式化成 5-5 分组展示（与生成时的展示约定一致，纯前端排版，
 *  不影响提交值——提交前会被 validateRecoveryCode 同款逻辑去掉分隔符）。 */
export function formatRecoveryCodeForDisplay(code: string): string {
  const normalized = code.replace(/[\s-]/g, "");
  if (normalized.length !== 10) return code;
  return `${normalized.slice(0, 5)}-${normalized.slice(5)}`;
}
