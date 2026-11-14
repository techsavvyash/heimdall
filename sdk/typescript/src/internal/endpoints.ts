import { isIP } from "node:net";
import { HeimdallError } from "./errors.js";

// Validate configured endpoints without normalizing the issuer identity string.
export function validateEndpoint(value: string, profile: "production" | "loopback"): void {
  const invalid = () => {
    throw new HeimdallError("invalid_argument");
  };
  if (
    (profile !== "production" && profile !== "loopback") ||
    value.length > 2048 ||
    /[^\x21-\x7e]/.test(value) ||
    /[\\?#@]/.test(value)
  )
    invalid();
  const parts = /^(https?):\/\/(\[[0-9a-f:]+\]|[a-z0-9.-]+)(?::([0-9]+))?(\/.*)?$/.exec(value);
  if (!parts) return invalid();
  const [, protocol, authority, port, path = ""] = parts;
  const bracketed = authority!.startsWith("[");
  const host = bracketed ? authority!.slice(1, -1) : authority!;
  const ip = isIP(host);
  if (
    bracketed
      ? ip !== 6
      : ip !== 4 &&
        (host.length > 253 ||
          /^(?:[0-9]+|0x[a-f0-9]+)$/.test(host.split(".").at(-1)!) ||
          host.split(".").some((label) => !/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(label)))
  )
    invalid();
  if (port !== undefined && (!/^[1-9][0-9]{0,4}$/.test(port) || Number(port) > 65535)) invalid();
  if (
    protocol === "http" &&
    (profile !== "loopback" || !["localhost", "127.0.0.1", "::1"].includes(host))
  )
    invalid();
  if (/[^A-Za-z0-9._~!$&'()*+,;=:%/-]/.test(path)) invalid();
  for (const segment of path.split("/")) {
    let decoded: string;
    try {
      decoded = decodeURIComponent(segment);
    } catch {
      return invalid();
    }
    if (
      decoded === "." ||
      decoded === ".." ||
      Array.from(decoded).some(
        (c) => c === "/" || c === "\\" || c.codePointAt(0)! <= 32 || c.codePointAt(0) === 127,
      )
    )
      invalid();
  }
  try {
    new URL(value);
  } catch {
    invalid();
  }
}

type Config = {
  issuer: string;
  openfga: { url: string };
  transportProfile?: "production" | "loopback";
};
export function validateConfigEndpoints(config: Config): void {
  const profile = config.transportProfile ?? "production";
  validateEndpoint(config.issuer, profile);
  validateEndpoint(config.openfga.url, profile);
}

// ZITADEL may qualify the OAuth client identifier with its project name.
export function validClientId(value: unknown): value is string {
  return (
    typeof value === "string" &&
    value.length <= 128 &&
    /^[A-Za-z0-9_-]+(?:@[A-Za-z0-9_-]+)?(?![\s\S])/.test(value)
  );
}
