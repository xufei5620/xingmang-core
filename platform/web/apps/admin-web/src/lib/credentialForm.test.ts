import { describe, expect, it } from "vitest";
import {
  EMPTY_CREDENTIAL_FORM,
  buildCredentialParams,
  credentialFieldLabel,
  validateCredentialForm,
  type CredentialFormValues,
} from "./credentialForm";

describe("credential form contract", () => {
  it("requires a valid secret reference and a non-empty pasted value", () => {
    expect(validateCredentialForm(EMPTY_CREDENTIAL_FORM)).toEqual({
      credentialRef: "凭据引用必须是 secret://<scope>/<name>",
      secretValue: "请粘贴凭据值；保存后不会再次显示",
    });
  });

  it("accepts only lowercase scope/name segments and preserves the value exactly", () => {
    const values: CredentialFormValues = {
      credentialRef: " secret://sub2api/read-only ",
      secretValue: " value-with-leading-and-trailing-space ",
    };

    expect(validateCredentialForm(values)).toEqual({});
    expect(buildCredentialParams(values)).toEqual({
      credentialRef: "secret://sub2api/read-only",
      secretValue: " value-with-leading-and-trailing-space ",
    });
  });

  it("rejects inline plaintext refs, path traversal, and whitespace-only values", () => {
    expect(validateCredentialForm({ credentialRef: "api-key", secretValue: "x" }).credentialRef).toMatch(
      /secret:\/\//,
    );
    expect(validateCredentialForm({ credentialRef: "secret://sub2api/../key", secretValue: "x" }).credentialRef).toMatch(
      /格式/,
    );
    expect(validateCredentialForm({ credentialRef: "secret://sub2api/key", secretValue: "   " }).secretValue).toMatch(
      /粘贴凭据值/,
    );
  });

  it("keeps labels aligned with the visible controls", () => {
    expect(credentialFieldLabel("credentialRef")).toBe("CredentialRef");
    expect(credentialFieldLabel("secretValue")).toBe("凭据值");
  });
});
