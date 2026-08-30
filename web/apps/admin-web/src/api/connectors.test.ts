import { describe, expect, it, vi } from "vitest";
import type { ApiClient } from "./client";
import {
  CONNECTOR_MANAGE_PERMISSION,
  isConnectorMode,
  isConnectorPlatform,
  listConnectorConfigs,
  setConnectorConfig,
} from "./connectors";

function client(getBody: unknown = { items: [] }): ApiClient {
  return {
    get: vi.fn().mockResolvedValue(getBody),
    post: vi.fn().mockResolvedValue({ action_run_id: "run-conn-1", result: {} }),
  };
}

describe("connectors API contract", () => {
  it("lists the current per-platform config from GET /api/v1/connectors/config", async () => {
    const row = {
      platform: "sub2api",
      mode: "real",
      endpoint: "https://sub2api.example.com",
      target_allowlist: ["sub2api.example.com"],
      credential_ref: "secret://sub2api-prod/read-token",
      version: 4,
      updated_at: "2026-08-30T09:00:00Z",
      updated_by: "HUMAN:operator",
    };
    const c = client({ items: [{ ...row, secret_value: "must-not-be-projected" }] });

    const items = await listConnectorConfigs({}, c);

    expect(c.get).toHaveBeenCalledWith("/api/v1/connectors/config", {});
    expect(items).toEqual([row]);
  });

  it("keeps unknown platforms and modes verbatim and defaults missing fields honestly", async () => {
    const c = client({ items: [{ platform: "cpa", mode: "weird", target_allowlist: ["a.example.com", 7] }] });

    expect(await listConnectorConfigs({}, c)).toEqual([
      {
        platform: "cpa",
        mode: "weird",
        endpoint: "",
        target_allowlist: ["a.example.com"],
        credential_ref: "",
        version: 0,
        updated_at: "",
        updated_by: "",
      },
    ]);
  });

  it("rejects rows without a platform and tolerates a missing items array", async () => {
    await expect(listConnectorConfigs({}, client({ items: [{ mode: "fake" }] }))).rejects.toThrow(/platform/);
    expect(await listConnectorConfigs({}, client({}))).toEqual([]);
  });

  it("sends connector.config.set@1 with a normalized comma-separated allowlist string", async () => {
    const c = client();

    const run = await setConnectorConfig(
      {
        platform: "newapi",
        mode: "real",
        endpoint: " https://newapi.example.com/api ",
        targetAllowlist: " newapi.example.com, ,NEWAPI.example.com ,db.example.com:5432",
        credentialRef: " secret://newapi/readonly-token ",
      },
      {},
      c,
    );

    expect(run.runId).toBe("run-conn-1");
    expect(c.post).toHaveBeenCalledWith(
      "/api/v1/actions/connector.config.set/versions/1/execute",
      {
        params: {
          platform: "newapi",
          mode: "real",
          endpoint: "https://newapi.example.com/api",
          target_allowlist: "newapi.example.com,db.example.com:5432",
          credential_ref: "secret://newapi/readonly-token",
        },
      },
      expect.objectContaining({ requestId: expect.any(String) }),
    );
  });

  it("exposes the connector permission and the closed platform/mode sets", () => {
    expect(CONNECTOR_MANAGE_PERMISSION).toBe("connector.manage");
    expect(isConnectorPlatform("sub2api")).toBe(true);
    expect(isConnectorPlatform("cpa")).toBe(false);
    expect(isConnectorMode("real")).toBe(true);
    expect(isConnectorMode("mock")).toBe(false);
  });
});
