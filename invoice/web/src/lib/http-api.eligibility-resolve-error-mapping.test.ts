import { afterEach, describe, expect, it, vi } from "vitest";

import { InvoiceApiError } from "./api-contract";
import { httpInvoiceApi } from "./http-api";

// CR-0007 problem three: ResolveEligibilityFreeze's four narrowed
// preconditions must each surface as a specific Chinese sentence (not the
// generic 409/5xx fallback), while keeping the server's `code` on the
// thrown error and leaving every other status/code's fallback unchanged.
//
// resolveEligibilityFreeze is a POST, which this client's requestJSON
// refuses to send without a session CSRF token (csrfTokenForMutation throws
// CSRF_TOKEN_MISSING before ever calling fetch) -- there is no exported test
// seam to set that token directly, so each case first makes one successful
// GET (getEligibilityFreezes, already covered by
// http-api.eligibility-external-user-id.test.ts) whose stubbed response
// carries an X-CSRF-Token header; requestJSON captures that header off any
// response, success or not, which is what unblocks the second, POST call
// this test actually exercises.
const FREEZE_ID = "61000000-0000-4000-8000-000000000001";

function stubFetchSeedCSRFThenRespond(status: number, code: string, message: string) {
  vi.stubGlobal("window", {
    setTimeout: globalThis.setTimeout.bind(globalThis),
    clearTimeout: globalThis.clearTimeout.bind(globalThis),
  });
  let call = 0;
  const fetchMock = vi.fn(async () => {
    call += 1;
    if (call === 1) {
      return new Response(
        JSON.stringify({
          items: [],
          has_more: false,
          next_before_opened_at: "",
          next_before_id: "",
        }),
        {
          status: 200,
          headers: {
            "Content-Type": "application/json",
            "X-CSRF-Token": "test-csrf-token",
          },
        },
      );
    }
    return new Response(JSON.stringify({ error: { code, message } }), {
      status,
      headers: { "Content-Type": "application/json" },
    });
  });
  vi.stubGlobal("fetch", fetchMock);
}

async function seedCSRFToken() {
  await httpInvoiceApi.getEligibilityFreezes({ status: "open" });
}

async function resolveAttempt() {
  return httpInvoiceApi.resolveEligibilityFreeze(FREEZE_ID, {
    version: 1,
    evidenceReference: "ticket-123",
    note: "reconciliation note",
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("resolveEligibilityFreeze error mapping (CR-0007 problem three)", () => {
  it.each([
    [
      503,
      "ELIGIBILITY_SOURCE_STALE",
      "来源数据尚未同步新鲜，暂时无法判定是否可以安全解冻，请稍后重试。",
    ],
    [
      409,
      "ELIGIBILITY_PROJECTION_PENDING",
      "该账号存在尚未完成的资格重算任务，需等待任务结束后才能解冻。",
    ],
    [
      409,
      "ELIGIBILITY_REFUND_EXPOSED",
      "该账号存在未结案的退款或红冲风险，须先在退款与红冲队列处理后才能解冻。",
    ],
    [
      409,
      "ELIGIBILITY_EVALUATION_UNMATCHED",
      "最新余额对账结论尚未匹配，暂不满足安全解冻条件。",
    ],
  ])(
    "%s %s gets its own specific sentence, not the generic fallback",
    async (status, code, expectedMessage) => {
      stubFetchSeedCSRFThenRespond(status, code, "raw server message");
      await seedCSRFToken();
      let caught: unknown;
      try {
        await resolveAttempt();
      } catch (error) {
        caught = error;
      }
      expect(caught).toBeInstanceOf(InvoiceApiError);
      const apiError = caught as InvoiceApiError;
      expect(apiError.message).toBe(expectedMessage);
      // The server's code must still be preserved on the thrown error, even
      // though the displayed message no longer echoes the raw server text.
      expect(apiError.code).toBe(code);
      expect(apiError.status).toBe(status);
    },
  );

  it("leaves an ordinary version conflict on the generic 409 fallback", async () => {
    stubFetchSeedCSRFThenRespond(409, "CONFLICT", "version mismatch");
    await seedCSRFToken();
    await expect(resolveAttempt()).rejects.toMatchObject({
      message: "数据已经发生变化，请刷新后重试。",
      code: "CONFLICT",
    });
  });

  it("leaves an unrelated 503 on the generic service-unavailable fallback", async () => {
    stubFetchSeedCSRFThenRespond(503, "SOURCE_SYNC_UNAVAILABLE", "source stale");
    await seedCSRFToken();
    await expect(resolveAttempt()).rejects.toMatchObject({
      message: "开票服务暂时不可用，请稍后重试。",
      code: "SOURCE_SYNC_UNAVAILABLE",
    });
  });
});
