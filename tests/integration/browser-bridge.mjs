import assert from "node:assert/strict";
import { randomBytes, randomUUID } from "node:crypto";
import { createServer } from "node:http";

// Loopback-only capability probe. Production cookies, storage and HTTP adapters belong in SDKs.
export async function createBrowserBridge() {
  const state = randomUUID();
  const cookie = randomBytes(32).toString("hex");
  const startKey = randomBytes(32).toString("hex");
  const deadline = Date.now() + 300000;
  let origin,
    authorizeUrl,
    expectedIntent,
    resolveCallback,
    claimed = false;
  let result = "Waiting for protocol verification";
  let finished = false;
  const server = createServer((req, res) => {
    res.setHeader("Cache-Control", "no-store");
    res.setHeader("Referrer-Policy", "no-referrer");
    res.setHeader(
      "Content-Security-Policy",
      "default-src 'none'; base-uri 'none'; frame-ancestors 'none'",
    );
    const reply = (status, text) => {
      res.writeHead(status, { "content-type": "text/plain" });
      res.end(text);
    };
    if (req.headers.host !== new URL(origin).host || req.method !== "GET")
      return reply(400, "Invalid request");
    const url = new URL(req.url, origin);
    if (url.pathname === `/start/${startKey}`) {
      if (claimed || !authorizeUrl || Date.now() >= deadline)
        return reply(409, "Unavailable transaction");
      res.writeHead(302, {
        location: authorizeUrl,
        "set-cookie": `heimdall_probe=${cookie}; HttpOnly; SameSite=Lax; Path=/; Max-Age=300`,
      });
      return res.end();
    }
    if (url.pathname === "/result") {
      res.writeHead(200, { "content-type": "text/html; charset=utf-8" });
      return res.end(
        `<!doctype html><title>Heimdall federation probe</title>${finished ? "" : '<meta http-equiv="refresh" content="1">'}<h1>${result}</h1><p>Disposable verification fixture. No product session has been created.</p>`,
      );
    }
    if (url.pathname !== "/complete") return reply(404, "Not found");
    if (claimed) return reply(409, "Callback already claimed");
    if (Date.now() >= deadline) return reply(410, "Transaction expired");
    const cookies = (req.headers.cookie ?? "").split(";").map((entry) => entry.trim());
    if (
      cookies.filter((entry) => entry.startsWith("heimdall_probe=")).length !== 1 ||
      !cookies.includes(`heimdall_probe=${cookie}`)
    )
      return reply(401, "Browser binding required");
    if (url.searchParams.getAll("flow").length !== 1 || url.searchParams.get("flow") !== state)
      return reply(400, "Transaction mismatch");
    if (
      !expectedIntent ||
      url.searchParams.getAll("id").length !== 1 ||
      url.searchParams.get("id") !== expectedIntent
    )
      return reply(400, "Intent mismatch");
    if (url.searchParams.getAll("token").length !== 1 || !url.searchParams.get("token"))
      return reply(400, "Intent credential required");
    claimed = true; // Claim synchronously before any provider verification awaits.
    res.writeHead(303, {
      location: "/result",
      "set-cookie": "heimdall_probe=; HttpOnly; SameSite=Lax; Path=/; Max-Age=0",
    });
    res.end();
    resolveCallback(url);
  });
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  origin = `http://127.0.0.1:${server.address().port}`;
  const timeout = setTimeout(() => {
    server.closeAllConnections();
    server.close();
  }, 300000);
  return {
    origin,
    callbackUrl: `${origin}/complete?flow=${state}`,
    async authenticate(url, intentId) {
      assert(!authorizeUrl);
      authorizeUrl = url;
      expectedIntent = intentId;
      const callback = new URL(
        `${origin}/complete?flow=${state}&id=${intentId}&token=synthetic-invalid`,
      );
      const request = (target, value) =>
        fetch(target, {
          redirect: "manual",
          headers: value ? { cookie: value } : {},
          signal: AbortSignal.timeout(5000),
        });
      assert.equal((await request(callback)).status, 401);
      assert.equal((await request(callback, "heimdall_probe=wrong")).status, 401);
      const wrongState = new URL(callback);
      wrongState.searchParams.set("flow", randomUUID());
      assert.equal((await request(wrongState, `heimdall_probe=${cookie}`)).status, 400);
      const wrongIntent = new URL(callback);
      wrongIntent.searchParams.set("id", "wrong");
      assert.equal((await request(wrongIntent, `heimdall_probe=${cookie}`)).status, 400);
      const pending = new Promise((resolve, reject) => {
        resolveCallback = resolve;
        server.once("close", () => reject(new Error("Browser probe timed out before callback")));
      });
      console.error(`Browser probe start: ${origin}/start/${startKey}`);
      const target = await pending;
      assert.equal((await request(target, `heimdall_probe=${cookie}`)).status, 409);
      return target;
    },
    finish(success) {
      finished = true;
      result = success ? "Federation verification passed" : "Federation verification failed";
      clearTimeout(timeout);
      setTimeout(() => {
        server.closeAllConnections();
        server.close();
      }, 60000);
    },
  };
}
