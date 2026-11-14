#!/usr/bin/env node
import { open, lstat, readFile, link, unlink } from "node:fs/promises";
import { randomUUID } from "node:crypto";
import { join } from "node:path";
import { Pool } from "pg";
import { HeimdallError } from "./internal/errors.js";
import { readSetupStatus, setupProduct } from "./provisioning/index.js";
import type { ProductSetupPlan } from "./provisioning/index.js";

async function run() {
  if (process.argv[2] === "--status") {
    const [, , , productId, environment, ...extra] = process.argv;
    const database = process.env.HEIMDALL_SETUP_DATABASE_URL;
    if (!database || !productId || !environment || extra.length)
      throw new HeimdallError("invalid_argument");
    const pool = new Pool({ connectionString: database, connectionTimeoutMillis: 5000 });
    try {
      const status = await readSetupStatus(pool, productId, environment, AbortSignal.timeout(5000));
      process.stdout.write(JSON.stringify(status, null, 2) + "\n");
    } finally {
      await pool.end();
    }
    return;
  }
  const [planPath, manifestPath, ...extra] = process.argv.slice(2);
  const {
    HEIMDALL_SETUP_DATABASE_URL: database,
    HEIMDALL_SETUP_ZITADEL_TOKEN: zitadelToken,
    HEIMDALL_SETUP_OPENFGA_TOKEN: openfgaToken,
    HEIMDALL_SETUP_SECRET_DIR: secretDir,
    HEIMDALL_SETUP_RECOVER_STORE_ID: recoverStoreId,
    HEIMDALL_SETUP_RECOVER_MODEL_ID: recoverModelId,
  } = process.env;
  if (
    !planPath ||
    !manifestPath ||
    extra.length ||
    !database ||
    !zitadelToken ||
    !openfgaToken ||
    !secretDir
  )
    throw new HeimdallError("invalid_argument");
  const directory = await lstat(secretDir);
  if (!directory.isDirectory() || directory.mode & 0o077 || directory.uid !== process.getuid?.())
    throw new HeimdallError("invalid_argument");
  const plan = JSON.parse(await readFile(planPath, "utf8")) as ProductSetupPlan;
  const manifest = await readFile(manifestPath, "utf8");
  const pool = new Pool({ connectionString: database, connectionTimeoutMillis: 5000 });
  const signal = AbortSignal.timeout(300_000);
  const secretPath = (id: string, clientId: string) => join(secretDir, `${id}.${clientId}.secret`);
  try {
    const result = await setupProduct(
      pool,
      plan,
      manifest,
      {
        zitadelToken,
        openfgaToken,
        ...(recoverStoreId ? { recoverStoreId } : {}),
        ...(recoverModelId ? { recoverModelId } : {}),
        async saveSecret(id, clientId, secret, operation) {
          operation.throwIfAborted();
          const temporary = join(secretDir, "." + randomUUID());
          const file = await open(temporary, "wx", 0o600);
          try {
            try {
              await file.writeFile(secret);
              await file.sync();
            } finally {
              await file.close();
            }
            operation.throwIfAborted();
            await link(temporary, secretPath(id, clientId));
            const parent = await open(secretDir, "r");
            try {
              await parent.sync();
            } finally {
              await parent.close();
            }
          } finally {
            await unlink(temporary);
          }
        },
        async hasSecret(id, clientId, operation) {
          operation.throwIfAborted();
          const file = await lstat(secretPath(id, clientId)).catch(() => undefined);
          return (
            !!file?.isFile() &&
            file.uid === process.getuid?.() &&
            !(file.mode & 0o077) &&
            file.size > 0 &&
            file.size <= 4096
          );
        },
      },
      signal,
    );
    process.stdout.write(JSON.stringify(result, null, 2) + "\n");
  } finally {
    await pool.end();
  }
}

try {
  await run();
} catch (error) {
  if (error instanceof HeimdallError) console.error(error.code, error.operationId ?? "");
  else console.error("unavailable");
  process.exitCode = 1;
}
