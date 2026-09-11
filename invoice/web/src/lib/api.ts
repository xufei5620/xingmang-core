import type { ApiMode } from "./api-contract";
import { httpInvoiceApi } from "./http-api";
import { mockInvoiceApi } from "./mock-api";

const configuredMode = (
  import.meta.env.VITE_API_MODE || "http"
).toLowerCase();

if (configuredMode !== "mock" && configuredMode !== "http") {
  throw new Error(
    `Invalid VITE_API_MODE=${configuredMode}; expected "mock" or "http".`,
  );
}

if (import.meta.env.PROD && configuredMode === "mock") {
  throw new Error(
    "VITE_API_MODE=mock is forbidden in production builds; use the development server for the local demo.",
  );
}

// The explicit PROD branch lets Rollup remove the entire mock data module from
// deployable assets. Runtime input can never switch a production bundle back
// to the in-memory client.
export const apiMode: ApiMode = import.meta.env.PROD
  ? "http"
  : (configuredMode as ApiMode);
export const invoiceApi = import.meta.env.PROD
  ? httpInvoiceApi
  : apiMode === "http"
    ? httpInvoiceApi
    : mockInvoiceApi;
export const apiCapabilities = invoiceApi.capabilities;
