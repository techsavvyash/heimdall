import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { exportJWK, generateKeyPair, SignJWT } from "jose";
import { expect, test, vi } from "vitest";
import { createBrowserOIDC } from "../src/internal/browser-oidc.js";
import { HeimdallError } from "../src/internal/errors.js";

const fixture = (name: string) =>
  JSON.parse(
    readFileSync(new URL(`../../../contracts/fixtures/${name}.json`, import.meta.url), "utf8"),
  );
const { clientId, clientSecret, audience, subject, code, nonce, verifier, cases } =
  fixture("browser-oidc");
const { issuer, metadata } = fixture("oidc-discovery");
const redirectUri = "https://ledger.example.test/auth/callback";

test("browser OIDC code exchange follows the shared response and claim matrix", async () => {
  const { privateKey, publicKey } = await generateKeyPair("RS256", { extractable: true });
  const jwk = { ...(await exportJWK(publicKey)), kid: "test" };
  let mode = "";
  let posts = 0;
  const requests = async (input: string, init?: RequestInit) => {
    if (input.endsWith("/.well-known/openid-configuration")) return Response.json(metadata);
    if (input === metadata.jwks_uri) return Response.json({ keys: [jwk] });
    expect(input).toBe(metadata.token_endpoint);
    expect(init?.redirect).toBe("error");
    expect(init?.method).toBe("POST");
    const encoded = Buffer.from(
      new Headers(init?.headers).get("authorization")!.slice(6),
      "base64",
    ).toString();
    expect(encoded).toBe("ledger-web:fixture%3Asecret%2B*%7E");
    expect(Object.fromEntries(init?.body as URLSearchParams)).toEqual({
      grant_type: "authorization_code",
      redirect_uri: redirectUri,
      code,
      code_verifier: verifier,
    });
    posts++;
    if (mode === "redirect")
      return new Response(null, {
        status: 302,
        headers: { location: "https://other.example.test" },
      });
    if (mode === "oversize") return new Response(" ".repeat(65537));
    if (mode === "invalid grant") return Response.json({ error: "invalid_grant" }, { status: 400 });
    const sign = (claims: Record<string, unknown>) =>
      new SignJWT(claims)
        .setProtectedHeader({ alg: "RS256", kid: "test" })
        .setIssuer(issuer)
        .setIssuedAt()
        .setExpirationTime("5m")
        .sign(privateKey);
    const accessToken = await sign({
      sub: mode === "wrong access subject" ? "other" : subject,
      aud: mode === "wrong access audience" ? clientId : audience,
    });
    const idAudiences = [clientId];
    if (
      ["project and client audiences", "missing authorized party with two audiences"].includes(mode)
    )
      idAudiences.push(audience);
    if (mode === "extra ID audience") idAudiences.push("other");
    const idToken = await sign({
      sub: subject,
      aud: mode === "wrong ID audience" ? audience : idAudiences,
      nonce: mode === "wrong nonce" ? "other" : nonce,
      ...(mode === "missing authorized party with two audiences"
        ? {}
        : {
            azp: mode === "wrong authorized party" ? "other" : clientId,
          }),
      at_hash:
        mode === "wrong access hash"
          ? "other"
          : createHash("sha256").update(accessToken).digest().subarray(0, 16).toString("base64url"),
    });
    return Response.json({
      access_token: accessToken,
      id_token: idToken,
      token_type: "Bearer",
      refresh_token: "refresh-fixture",
    });
  };
  vi.stubGlobal("fetch", requests);
  try {
    const browser = await createBrowserOIDC({
      issuer,
      profile: "production",
      clientId,
      audience,
      clientSecret,
      redirectUri,
    });
    for (const scenario of cases as { name: string; error: string }[]) {
      mode = scenario.name;
      const before = posts;
      const exchange = browser.exchange(code, async () => {
        if (mode === "claim uncertain") throw new HeimdallError("outcome_unknown");
        return { nonce, pkceVerifier: verifier };
      });
      const result = await exchange.then(
        (value) => ({ subject: value.identity.subject, refreshToken: value.refreshToken }),
        (error: Error) => ({ error: error.message }),
      );
      expect({ mode, ...result }).toEqual(
        scenario.error
          ? { mode, error: scenario.error }
          : { mode, subject, refreshToken: "refresh-fixture" },
      );
      expect({ mode, posts: posts - before }).toEqual({
        mode,
        posts: mode === "claim uncertain" ? 0 : 1,
      });
    }
  } finally {
    vi.unstubAllGlobals();
  }
});
