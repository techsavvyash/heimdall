import { createHash } from "node:crypto";
import type { AuditedAuthentication } from "./audited-authentication.js";
import type { Principal } from "../runtime.js";
import { validateCursorScope } from "./cursor-contract.js";
import { CursorSigner } from "./envelopes.js";
import { checkCancellation, HeimdallError } from "./errors.js";
import { encodeIdentity } from "./identity-encoding.js";
import type { PinnedModel } from "./model-binding.js";

export type Workspace = { id: string; name: string };
export type WorkspaceSource = {
  scan(
    input: { after: string | null; limit: number },
    options: { signal: AbortSignal },
  ): Promise<{
    items: { workspace: Workspace; position: string }[];
    exhausted: boolean;
  }>;
};
export type DiscoveryOptions = {
  source: WorkspaceSource;
  activeKeyId: string;
  keys: Readonly<Record<string, Uint8Array>>;
  scanBudget?: number;
};
export type WorkspaceDiscovery = {
  list(
    actor: Principal,
    options: { cursor?: string; limit?: number; signal: AbortSignal },
  ): Promise<{ items: Workspace[]; nextCursor: string | null }>;
};

const boundedText = (value: unknown, max: number): value is string =>
  typeof value === "string" && value.length > 0 && value.isWellFormed() && [...value].length <= max;

export function workspaceDiscovery(
  options: DiscoveryOptions,
  scope: { productId: string; environment: string; modelId: string; manifestVersion: number },
  auth: AuditedAuthentication,
  model: PinnedModel,
): WorkspaceDiscovery {
  const budget = options?.scanBudget ?? 500;
  if (
    typeof options?.source?.scan !== "function" ||
    !Number.isSafeInteger(budget) ||
    budget < 1 ||
    budget > 500
  )
    throw new HeimdallError("invalid_argument");
  const scan = options.source.scan.bind(options.source);
  const signer = new CursorSigner(options.activeKeyId, options.keys);
  const { manifestVersion, ...deployment } = scope;
  return Object.freeze({
    async list(actor: Principal, options: Parameters<WorkspaceDiscovery["list"]>[1]) {
      const { limit = 20, cursor, signal } = options ?? {};
      if (
        !(signal instanceof AbortSignal) ||
        !Number.isSafeInteger(limit) ||
        limit < 1 ||
        limit > 100 ||
        (cursor !== undefined && (typeof cursor !== "string" || !cursor))
      )
        throw new HeimdallError("invalid_argument");
      if (!auth.owns(actor)) throw new HeimdallError("unauthenticated");
      const user = encodeIdentity(actor.issuer, actor.subject);
      const binding = { ...deployment, principal: createHash("sha256").update(user).digest("hex") };
      let after: string | null = null;
      if (cursor !== undefined) {
        try {
          const payload = JSON.parse(signer.verify(cursor).toString("utf8"));
          if (
            payload.schemaVersion !== 1 ||
            payload.kind !== "workspace_discovery" ||
            !boundedText(payload.position, 2048) ||
            !Number.isSafeInteger(payload.issuedAt) ||
            !Number.isSafeInteger(payload.expiresAt) ||
            payload.issuedAt < 0
          )
            throw new Error();
          validateCursorScope(payload, binding, Date.now());
          after = payload.position;
        } catch {
          throw new HeimdallError("invalid_argument");
        }
      }
      const items: Workspace[] = [];
      let exhausted = false;
      await auth.checkPermission(
        actor,
        null,
        "workspaces.list",
        scope.modelId,
        manifestVersion,
        async (operation, parent) => {
          const positions = new Set<string>();
          if (after !== null) positions.add(after);
          const ids = new Set<string>();
          for (let consumed = 0; consumed < budget && items.length < limit && !exhausted;) {
            const size = Math.min(20, budget - consumed);
            const page = await scan({ after, limit: size }, { signal: operation });
            checkCancellation(operation);
            if (
              !Array.isArray(page?.items) ||
              page.items.length > size ||
              typeof page.exhausted !== "boolean" ||
              (page.items.length === 0 && !page.exhausted)
            )
              throw new HeimdallError("unavailable");
            exhausted = page.exhausted;
            for (const [index, candidate] of page.items.entries()) {
              const { workspace, position } = candidate ?? {};
              if (
                !boundedText(position, 2048) ||
                positions.has(position) ||
                !workspace ||
                typeof workspace.id !== "string" ||
                !/^[A-Za-z0-9_-]{1,128}(?![\s\S])/.test(workspace.id) ||
                ids.has(workspace.id) ||
                !boundedText(workspace.name, 256)
              )
                throw new HeimdallError("unavailable");
              const item = { id: workspace.id, name: workspace.name };
              positions.add(position);
              ids.add(item.id);
              const allowed = await auth.checkPermission(
                actor,
                item.id,
                "workspace.member",
                scope.modelId,
                manifestVersion,
                (signal) => model.checkMembership(user, item.id, signal),
                operation,
                parent,
              );
              after = position;
              consumed++;
              if (allowed) items.push(item);
              if (items.length === limit) {
                exhausted = page.exhausted && index === page.items.length - 1;
                break;
              }
            }
          }
          return items.length > 0;
        },
        signal,
      );
      const issuedAt = Date.now();
      return {
        items,
        nextCursor:
          exhausted || after === null
            ? null
            : signer.sign(
                Buffer.from(
                  JSON.stringify({
                    schemaVersion: 1,
                    kind: "workspace_discovery",
                    ...binding,
                    position: after,
                    issuedAt,
                    expiresAt: issuedAt + 900_000,
                  }),
                ),
              ),
      };
    },
  });
}
