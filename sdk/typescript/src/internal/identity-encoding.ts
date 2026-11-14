import { HeimdallError } from "./errors.js";

// Contract encoding only. This does not authenticate an identity.
export function encodeIdentity(issuer: string, subject: string): string {
  for (const [value, maxBytes] of [
    [issuer, 2048],
    [subject, 1024],
  ] as const) {
    if (
      typeof value !== "string" ||
      value.length === 0 ||
      !value.isWellFormed() ||
      Buffer.byteLength(value, "utf8") > maxBytes
    ) {
      throw new HeimdallError("invalid_argument");
    }
  }
  const canonical = JSON.stringify([issuer, subject]).replace(
    /[\u007f-\uffff]/g,
    (character) => "\\u" + character.charCodeAt(0).toString(16).padStart(4, "0"),
  );
  return "user:" + Buffer.from(canonical, "ascii").toString("base64url");
}

export function decodeIdentity(principal: string): { issuer: string; subject: string } {
  try {
    if (!/^user:[A-Za-z0-9_-]+$/.test(principal) || principal.length > 32768) throw new Error();
    const raw = new TextDecoder("utf-8", { fatal: true }).decode(
      Buffer.from(principal.slice(5), "base64url"),
    );
    const pair: unknown = JSON.parse(raw);
    if (!Array.isArray(pair) || pair.length !== 2 || encodeIdentity(pair[0], pair[1]) !== principal)
      throw new Error();
    return { issuer: pair[0], subject: pair[1] };
  } catch {
    throw new HeimdallError("unavailable");
  }
}
