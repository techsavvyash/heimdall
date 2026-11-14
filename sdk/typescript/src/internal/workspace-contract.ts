import { HeimdallError } from "./errors.js";
type Scope = { productId: string; environment: string; workspaceId: string };
type Workspace = Scope & {
  lifecycle: "pending" | "active" | "suspended" | "tombstoned";
  mutationBlocked: boolean;
  createdAt: number;
  updatedAt: number;
};
// Primary-database, schema-validated read. The caller must propagate read failures.
export function workspaceGate(
  record: Workspace | null,
  scope: Scope,
): "deny" | "check_relationships" {
  if (record === null) return "deny";
  if (record.createdAt > record.updatedAt) throw new HeimdallError("invalid_argument");
  if (
    record.productId !== scope.productId ||
    record.environment !== scope.environment ||
    record.workspaceId !== scope.workspaceId
  )
    throw new HeimdallError("unavailable");
  return record.lifecycle === "active" && !record.mutationBlocked ? "check_relationships" : "deny";
}
