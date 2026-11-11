import { execFileSync } from "node:child_process";
import { mkdtempSync, mkdirSync, readdirSync, renameSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
const root = resolve(import.meta.dirname, "..");
const work = mkdtempSync(join(tmpdir(), "heimdall-consumer-"));
const run = (command, args, cwd = work) => execFileSync(command, args, { cwd, stdio: "pipe" });
try {
  const archive = join(work, "archive");
  mkdirSync(archive);
  run("pnpm", ["--filter", "@heimdall/sdk", "pack", "--pack-destination", archive], root);
  const tarball = readdirSync(archive).find((name) => name.endsWith(".tgz"));
  if (!tarball) throw new Error("Package tarball missing");
  run("tar", ["-xzf", join(archive, tarball), "-C", archive]);
  const scope = join(work, "node_modules", "@heimdall");
  mkdirSync(scope, { recursive: true });
  renameSync(join(archive, "package"), join(scope, "sdk"));
  writeFileSync(join(work, "package.json"), '{"type":"module"}');
  writeFileSync(
    join(work, "tsconfig.json"),
    JSON.stringify({
      compilerOptions: {
        target: "ES2023",
        module: "NodeNext",
        moduleResolution: "NodeNext",
        strict: true,
        noEmit: true,
        types: [],
        lib: ["ES2024", "DOM"],
      },
      files: ["consumer.ts"],
    }),
  );
  writeFileSync(
    join(work, "consumer.ts"),
    `import { createHeimdall, auditEventDigest, type Principal, type RuntimeOptions } from '@heimdall/sdk';
declare const config: string, manifest: string, options: RuntimeOptions;
const signal = new AbortController().signal;
const runtime = await createHeimdall(config, manifest, options, {signal});
const principal: Principal = await runtime.tokens.verify('access-token', {signal});
const digest: string = auditEventDigest({});
void principal; void digest;
// @ts-expect-error Caller-created objects lack verified principal provenance.
const forged: Principal = {issuer:'x',subject:'x',kind:'human',productId:'x',environment:'x',policyVersion:'x',identityValidatedAt:0,expiresAt:1};
void forged;
`,
  );
  writeFileSync(
    join(work, "probe.mjs"),
    `import {createHeimdall,HeimdallError,auditEventDigest} from '@heimdall/sdk';
if(typeof createHeimdall !== 'function' || typeof auditEventDigest({}) !== 'string') throw new Error('Missing runtime exports');
try {
 await createHeimdall('{}','{}',{}, {signal:new AbortController().signal});
 throw new Error('Accepted invalid configuration');
} catch(error) {
 if(!(error instanceof HeimdallError) || error.code !== 'invalid_argument') throw error;
}
`,
  );
  run(join(root, "node_modules", ".bin", "tsc"), ["-p", join(work, "tsconfig.json")]);
  run(process.execPath, [join(work, "probe.mjs")]);
  console.log("Packed runtime: isolated import, declarations and principal type boundary passed");
} finally {
  rmSync(work, { recursive: true, force: true });
}
