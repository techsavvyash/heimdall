import { bounded } from "./audited-decision.js";
import { isDeepStrictEqual } from "node:util";
import { compileTenantModel } from "./tenant-model.js";
import { validateEndpoint } from "./endpoints.js";
import { HeimdallError, checkCancellation } from "./errors.js";
type Compiled = ReturnType<typeof compileTenantModel>;
type Config = { url: string; storeId: string; modelId: string; profile: "production" | "loopback" };
type ObjectValue = Record<string, unknown>;
const object = (value: unknown): ObjectValue => {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error();
  return value as ObjectValue;
};
function userset(value: unknown): unknown {
  const node = object(value);
  const result = { ...node };
  if ("computedUserset" in node)
    result.computedUserset = { object: "", ...object(node.computedUserset) };
  for (const operation of ["union", "intersection"]) {
    if (operation in node) {
      const operationNode = object(node[operation]);
      if (!Array.isArray(operationNode.child)) throw new Error();
      result[operation] = { ...operationNode, child: operationNode.child.map(userset) };
    }
  }
  if ("difference" in node) {
    const difference = object(node.difference);
    result.difference = {
      ...difference,
      base: userset(difference.base),
      subtract: userset(difference.subtract),
    };
  }
  return result;
}
// Only the pinned API's explicit protobuf defaults are added; unknown fields survive comparison.
function wireModel(value: unknown): unknown {
  const model = object(value);
  if (!Array.isArray(model.type_definitions)) throw new Error();
  return {
    conditions: {},
    ...model,
    type_definitions: model.type_definitions.map((value) => {
      const type = object(value);
      const relations = Object.fromEntries(
        Object.entries(object(type.relations === undefined ? {} : type.relations)).map(
          ([name, value]) => [name, userset(value)],
        ),
      );
      let metadata: unknown = null;
      if (type.metadata !== undefined && type.metadata !== null) {
        const m = object(type.metadata);
        metadata = {
          module: "",
          source_info: null,
          ...m,
          relations: Object.fromEntries(
            Object.entries(object(m.relations)).map(([name, value]) => {
              const relation = object(value);
              if (!Array.isArray(relation.directly_related_user_types)) throw new Error();
              return [
                name,
                {
                  module: "",
                  source_info: null,
                  ...relation,
                  directly_related_user_types: relation.directly_related_user_types.map(
                    (value) => ({ condition: "", ...object(value) }),
                  ),
                },
              ];
            }),
          ),
        };
      }
      return { ...type, relations, metadata };
    }),
  };
}
function freeze<T>(value: T): T {
  if (value && typeof value === "object") {
    for (const child of Object.values(value)) freeze(child);
    Object.freeze(value);
  }
  return value;
}
// Internal boundary: manifest and binding must have passed structural schema validation.
export class PinnedModel {
  private readonly config: Config;
  private readonly compiled: Compiled;
  constructor(
    config: Config,
    manifest: Parameters<typeof compileTenantModel>[0],
    binding: Parameters<typeof compileTenantModel>[1],
    artifact: Compiled,
    private readonly credential: (signal: AbortSignal) => Promise<string>,
  ) {
    try {
      validateEndpoint(config.url, config.profile);
      if (
        ![config.storeId, config.modelId].every(
          (v) => typeof v === "string" && /^[A-Za-z0-9_-]{1,128}$/.test(v),
        ) ||
        typeof credential !== "function"
      )
        throw new Error();
      const compiled = compileTenantModel(manifest, binding);
      if (
        !isDeepStrictEqual(compiled, artifact) ||
        Buffer.byteLength(JSON.stringify(compiled)) > 1048576
      )
        throw new Error();
      this.config = { ...config };
      this.compiled = freeze(compiled);
    } catch {
      throw new HeimdallError("invalid_argument");
    }
  }
  async verify(signal?: AbortSignal) {
    const bounded = AbortSignal.any([AbortSignal.timeout(5000), ...(signal ? [signal] : [])]);
    checkCancellation(bounded);
    try {
      const parsed = await this.request("/authorization-models/" + this.config.modelId, bounded);
      if (Object.keys(parsed).length !== 1) throw new Error();
      const model = object(parsed.authorization_model);
      if (model.id !== this.config.modelId) throw new Error();
      const { id: _id, ...contents } = model;
      if (!isDeepStrictEqual(wireModel(contents), wireModel(this.compiled.model)))
        throw new Error();
      checkCancellation(bounded);
      return freeze({
        storeId: this.config.storeId,
        modelId: this.config.modelId,
        ...this.compiled,
      });
    } catch {
      checkCancellation(bounded);
      throw new HeimdallError("unavailable");
    }
  }
  async check(user: string, workspace: string, permission: string, signal: AbortSignal) {
    if (!Object.hasOwn(this.compiled.permissions, permission))
      throw new HeimdallError("invalid_argument");
    const result = await this.request("/check", signal, {
      authorization_model_id: this.config.modelId,
      tuple_key: {
        user,
        object: "workspace:" + workspace,
        relation: this.compiled.permissions[permission],
      },
      consistency: "HIGHER_CONSISTENCY",
    });
    if (typeof result.allowed !== "boolean") throw new HeimdallError("unavailable");
    return result.allowed;
  }

  private async request(path: string, signal: AbortSignal, body?: Record<string, unknown>) {
    try {
      const key = await bounded(this.credential, signal);
      if (typeof key !== "string" || !/^[\x21-\x7e]{1,8192}$/.test(key)) throw new Error();
      const headers = new Headers({ authorization: `Bearer ${key}` });
      if (body) headers.set("content-type", "application/json");
      const response = await fetch(
        `${this.config.url.replace(/\/$/, "")}/stores/${this.config.storeId}${path}`,
        {
          headers,
          redirect: "error",
          signal,
          ...(body ? { method: "POST", body: JSON.stringify(body) } : {}),
        },
      );
      if (response.status !== 200 || !response.body) {
        await response.body?.cancel();
        throw new Error();
      }
      const parts: Uint8Array[] = [];
      let size = 0;
      const reader = response.body.getReader();
      try {
        for (;;) {
          const { done, value } = await reader.read();
          if (done) break;
          size += value.length;
          if (size > (body ? 65536 : 1048576)) throw new Error();
          parts.push(value);
        }
      } finally {
        await reader.cancel();
      }
      checkCancellation(signal);
      return object(
        JSON.parse(
          new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(Buffer.concat(parts)),
        ),
      );
    } catch {
      checkCancellation(signal);
      throw new HeimdallError("unavailable");
    }
  }
}
