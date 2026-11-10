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
