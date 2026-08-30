import { describe, expect, it, vi } from "vitest";
import type { ApiClient } from "./client";
import {
  CREDENTIAL_MANAGE_PERMISSION,
  fingerprintPrefix,
  listCredentials,
  revokeCredential,
  rotateCredential,
  upsertCredential,
} from "./credentials";

const config = {
  baseUrl: "",
  principalId: "operator",
  principalType: "HUMAN" as const,
  scopes: ["credential.manage"],
  environment: "development",
};

function client(getBody: unknown = { items: [] }): ApiClient {
  return {
    get: vi.fn().mockResolvedValue(getBody),
    post: vi.fn().mockResolvedValue({ action_run_id: "run-cred-1", result: {} }),
  };
}

describe("credentials API contract", () => {
  it("projects only safe metadata from the list response", async () => {
    const c = client({
      items: [
        {
          credential_ref: "secret://sub2api/readonly",
          scope: "sub2api",
          updated_at: "2026-08-30T09:00:00Z",
          fingerprint: "sha256:abcdef0123456789",
          secret_value: ["should", "never", "escape"].join("-"),
        },
      ],
    });

    const items = await listCredentials({}, c, config);

    expect(items).toEqual([
      {
        credential_ref: "secret://sub2api/readonly",
        scope: "sub2api",
        updated_at: "2026-08-30T09:00:00Z",
        fingerprint: "sha256:abcdef0123456789",
      },
    ]);
    expect(JSON.stringify(items)).not.toContain("should-never-escape");
    expect(c.get).toHaveBeenCalledWith("/api/v1/credentials", expect.objectContaining({
      searchParams: { environment: "development" },
    }));
  });

  it("uses the approved Action endpoint and sends a transient value only for upsert", async () => {
    const c = client();
    const transientValue = ["paste", "only", "in", "request"].join("-");

    const run = await upsertCredential(
      { credentialRef: "secret://sub2api/readonly", secretValue: transientValue },
      {},
      c,
    );

    expect(run.runId).toBe("run-cred-1");
    expect(c.post).toHaveBeenCalledWith(
      "/api/v1/actions/credential.secret.upsert/versions/1/execute",
      { params: { credential_ref: "secret://sub2api/readonly", secret_value: transientValue } },
      expect.objectContaining({ requestId: expect.any(String) }),
    );
  });

  it("uses rotate for an existing reference and revoke requires a reason", async () => {
    const c = client();
    await rotateCredential(
      { credentialRef: "secret://sub2api/readonly", secretValue: "next-value" },
      {},
      c,
    );
    await revokeCredential(
      { credentialRef: "secret://sub2api/readonly", reason: "planned-retirement" },
      {},
      c,
    );

    expect(c.post).toHaveBeenNthCalledWith(
      1,
      "/api/v1/actions/credential.secret.rotate/versions/1/execute",
      { params: { credential_ref: "secret://sub2api/readonly", secret_value: "next-value" } },
      expect.objectContaining({ requestId: expect.any(String) }),
    );
    expect(c.post).toHaveBeenNthCalledWith(
      2,
      "/api/v1/actions/credential.secret.revoke/versions/1/execute",
      { params: { credential_ref: "secret://sub2api/readonly", reason: "planned-retirement" } },
      expect.objectContaining({ requestId: expect.any(String) }),
    );
  });

  it("exposes the narrow permission separately from server authorization", () => {
    expect(CREDENTIAL_MANAGE_PERMISSION).toBe("credential.manage");
  });

  it("shortens fingerprints to an eight-character evidence prefix", () => {
    expect(fingerprintPrefix("sha256:abcdef0123456789")).toBe("sha256:abcdef01…");
    expect(fingerprintPrefix("abcdef01")).toBe("abcdef01");
    expect(fingerprintPrefix("")).toBe("—");
  });
});
