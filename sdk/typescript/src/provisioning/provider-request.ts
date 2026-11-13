import { validateEndpoint } from "../internal/endpoints.js";
import { checkCancellation, HeimdallError } from "../internal/errors.js";

export const providerId = (value: unknown): value is string =>
  typeof value === "string" && /^[A-Za-z0-9_-]{1,128}(?![\s\S])/.test(value);

export function providerRequest(
  base: string,
  token: string,
  profile: "production" | "loopback",
  signal: AbortSignal,
  headers: Record<string, string> = {},
) {
  validateEndpoint(base, profile);
  if (
    new URL(base).pathname !== "/" ||
    typeof token !== "string" ||
    !/^[\x21-\x7e]{1,4096}(?![\s\S])/.test(token)
  )
    throw new HeimdallError("invalid_argument");
  return async function call<T>(path: string, body?: object): Promise<T | null> {
    const operation = AbortSignal.any([signal, AbortSignal.timeout(5000)]);
    checkCancellation(operation);
    try {
      const response = await fetch(base.replace(/\/$/, "") + path, {
        method: body === undefined ? "GET" : "POST",
        redirect: "error",
        signal: operation,
        headers: {
          "content-type": "application/json",
          ...headers,
          authorization: "Bearer " + token,
        },
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
      });
      if (!response.ok) {
        await response.body?.cancel();
        if (response.status === 404) return null;
        throw new Error();
      }
      if (!response.body) throw new Error();
      const reader = response.body.getReader();
      const chunks: Uint8Array[] = [];
      let bytes = 0;
      try {
        for (;;) {
          const { done, value } = await reader.read();
          if (done) break;
          bytes += value.length;
          if (bytes > 1048576) throw new Error();
          chunks.push(value);
        }
      } finally {
        await reader.cancel();
      }
      return JSON.parse(Buffer.concat(chunks, bytes).toString("utf8")) as T;
    } catch {
      checkCancellation(operation);
      throw new HeimdallError("unavailable");
    }
  };
}
