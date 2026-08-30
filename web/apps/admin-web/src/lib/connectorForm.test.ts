import { describe, expect, it } from "vitest";
import {
  connectorFieldLabel,
  connectorFormFromConfig,
  hostOnly,
  splitAllowlist,
  validateConnectorForm,
} from "./connectorForm";

describe("connector form contract", () => {
  it("fake mode allows empty endpoint/allowlist/ref, but a given value must be well-formed", () => {
    expect(
      validateConnectorForm({ mode: "fake", endpoint: "", targetAllowlist: "", credentialRef: "" }),
    ).toEqual({});
    expect(
      validateConnectorForm({ mode: "fake", endpoint: "ftp://x", targetAllowlist: "", credentialRef: "" })
        .endpoint,
    ).toMatch(/https/);
    // 后端在 fake 模式下同样拒绝明文 http 与内嵌 user:pw@ 的地址（INVALID_PARAMS）
    expect(
      validateConnectorForm({
        mode: "fake",
        endpoint: "http://api.example.com",
        targetAllowlist: "",
        credentialRef: "",
      }).endpoint,
    ).toMatch(/https/);
    expect(
      validateConnectorForm({
        mode: "fake",
        endpoint: "https://user:pw@api.example.com",
        targetAllowlist: "",
        credentialRef: "",
      }).endpoint,
    ).toMatch(/user:password@/);
    expect(
      validateConnectorForm({
        mode: "fake",
        endpoint: "https://api.example.com",
        targetAllowlist: "",
        credentialRef: "",
      }),
    ).toEqual({});
    expect(
      validateConnectorForm({ mode: "fake", endpoint: "", targetAllowlist: "", credentialRef: "api-key" })
        .credentialRef,
    ).toMatch(/secret:\/\//);
  });

  it("real mode mirrors the backend Validate: https, allowlist containing the endpoint host, a CredentialRef", () => {
    expect(
      validateConnectorForm({ mode: "real", endpoint: "", targetAllowlist: "", credentialRef: "" }),
    ).toEqual({
      endpoint: expect.stringMatching(/上游地址/),
      targetAllowlist: expect.stringMatching(/ADR-004/),
      credentialRef: expect.stringMatching(/CredentialRef/),
    });
    expect(
      validateConnectorForm({
        mode: "real",
        endpoint: "http://api.example.com",
        targetAllowlist: "api.example.com",
        credentialRef: "secret://a/b",
      }).endpoint,
    ).toMatch(/https/);
    expect(
      validateConnectorForm({
        mode: "real",
        endpoint: "https://api.example.com/v1",
        targetAllowlist: "other.example.com",
        credentialRef: "secret://a/b",
      }).targetAllowlist,
    ).toMatch(/api\.example\.com/);
    expect(
      validateConnectorForm({
        mode: "real",
        endpoint: "https://API.example.com:8443/v1",
        targetAllowlist: "api.example.com:8443, db.example.com",
        credentialRef: "secret://newapi/readonly-token",
      }),
    ).toEqual({});
  });

  it("rejects allowlist entries that are URLs rather than hosts", () => {
    expect(
      validateConnectorForm({
        mode: "fake",
        endpoint: "",
        targetAllowlist: "https://api.example.com",
        credentialRef: "",
      }).targetAllowlist,
    ).toMatch(/不是合法主机名/);
  });

  it("splits, trims, drops empties and dedupes case-insensitively", () => {
    expect(splitAllowlist(" a.example.com, ,A.example.com ,b.example.com:5432")).toEqual([
      "a.example.com",
      "b.example.com:5432",
    ]);
    expect(hostOnly(" API.example.com:8443 ")).toBe("api.example.com");
  });

  it("seeds the form from the DB row, falling back to fake plus the platform's expected ref", () => {
    expect(connectorFormFromConfig(undefined, "secret://sub2api-prod/read-token")).toEqual({
      mode: "fake",
      endpoint: "",
      targetAllowlist: "",
      credentialRef: "secret://sub2api-prod/read-token",
    });
    expect(
      connectorFormFromConfig(
        {
          mode: "real",
          endpoint: "https://x.example.com",
          target_allowlist: ["x.example.com", "y.example.com"],
          credential_ref: "",
        },
        "secret://a/b",
      ),
    ).toEqual({
      mode: "real",
      endpoint: "https://x.example.com",
      targetAllowlist: "x.example.com, y.example.com",
      credentialRef: "secret://a/b",
    });
    expect(
      connectorFormFromConfig(
        { mode: "weird", endpoint: "", target_allowlist: [], credential_ref: "secret://c/d" },
        "secret://a/b",
      ),
    ).toMatchObject({ mode: "fake", credentialRef: "secret://c/d" });
  });

  it("keeps labels aligned with the visible controls", () => {
    expect(connectorFieldLabel("mode")).toBe("接入模式");
    expect(connectorFieldLabel("endpoint")).toBe("上游地址");
    expect(connectorFieldLabel("targetAllowlist")).toBe("允许主机");
    expect(connectorFieldLabel("credentialRef")).toBe("CredentialRef");
  });
});
