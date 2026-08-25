import type { UserRole } from "../types";

export function shouldShowAdminReturn(
  adminWorkspace: boolean,
  role: UserRole | undefined,
  stepUpRequired: boolean,
) {
  return (
    !adminWorkspace &&
    (role === "admin" || (role === "user" && stepUpRequired))
  );
}
