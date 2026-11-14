import { execFileSync } from "node:child_process";
import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { basename, join, resolve } from "node:path";
const root = resolve(import.meta.dirname, "..");
const work = mkdtempSync(join(tmpdir(), "heimdall-consumer-"));
const run = (command, args, cwd = work) => execFileSync(command, args, { cwd, stdio: "pipe" });
try {
  const archivePath = process.argv[2];
  let tarPath;
  if (archivePath) {
    tarPath = resolve(archivePath);
  } else {
    const archive = join(work, "archive");
    mkdirSync(archive);
    run(
      "pnpm",
      ["--filter", "@techsavvyash/heimdall-sdk", "pack", "--pack-destination", archive],
      root,
    );
    const tarball = readdirSync(archive).find((name) => name.endsWith(".tgz"));
    if (!tarball) throw new Error("Package tarball missing");
    tarPath = join(archive, tarball);
  }
  const metadata = JSON.parse(readFileSync(join(root, "sdk/typescript/package.json"), "utf8"));
  if (basename(tarPath) !== `techsavvyash-heimdall-sdk-${metadata.version}.tgz`)
    throw new Error("Package version mismatch");
  const packedMetadata = JSON.parse(run("tar", ["-xOzf", tarPath, "package/package.json"]));
  for (const field of ["name", "version", "private", "engines", "exports", "bin", "dependencies"]) {
    assert.deepEqual(packedMetadata[field], metadata[field], `Unexpected packed package ${field}`);
  }
  for (const entry of run("tar", ["-tzf", tarPath]).toString().trim().split("\n")) {
    if (entry === "package/package.json") continue;
    if (!/^package\/dist\/(?:[\w.-]+\/)*[\w.-]+\.(?:js|ts|js\.map|ts\.map)$/.test(entry))
      throw new Error("Unexpected package path");
  }
  writeFileSync(
    join(work, "package.json"),
    JSON.stringify({
      type: "module",
      dependencies: { "@techsavvyash/heimdall-sdk": "file:" + tarPath },
    }),
  );
  writeFileSync(
    join(work, "pnpm-workspace.yaml"),
    "minimumReleaseAge: 43200\nminimumReleaseAgeStrict: true\n",
  );
  run("pnpm", ["install", "--prefer-offline", "--ignore-scripts"]);
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
    `import { createHeimdall, auditEventDigest, type Principal, type RuntimeOptions } from '@techsavvyash/heimdall-sdk';
import { createPostgresStorage } from '@techsavvyash/heimdall-sdk/postgres';
declare const pool: Parameters<typeof createPostgresStorage>[0];
const storage = createPostgresStorage(pool,{productId:'ledger',environment:'production',auditSinks:['siem']});
void storage;
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
    `import {createHeimdall,HeimdallError,auditEventDigest} from '@techsavvyash/heimdall-sdk';
import {createPostgresStorage} from '@techsavvyash/heimdall-sdk/postgres';
import {verifySessionStore} from '@techsavvyash/heimdall-sdk/sessionstoretest';
import {setupProduct,inspectModelRollout} from '@techsavvyash/heimdall-sdk/provisioning';
if (typeof setupProduct !== 'function') throw new Error('missing provisioning export');
if (typeof inspectModelRollout !== 'function') throw new Error('missing rollout inspection export');
if (typeof verifySessionStore !== 'function') throw new Error('missing session store conformance export');
const storage = createPostgresStorage({connect(){}},{productId:'ledger',environment:'production',auditSinks:[]});
if(!Object.isFrozen(storage) || typeof storage.appendAudit !== 'function') throw new Error('Missing PostgreSQL factory');
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
  console.log(
    "Packed runtime: fresh dependency install, core/PostgreSQL imports, declarations and principal type boundary passed",
  );
} finally {
  rmSync(work, { recursive: true, force: true });
}
