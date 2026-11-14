import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import { createServer } from "node:http";
import { once } from "node:events";
import { promisify } from "node:util";
import { createRequire } from "node:module";

const execute = promisify(execFile);

export async function verifyRestoredReference({
  config,
  db,
  variables,
  commands,
  request,
  restart,
  stop,
  signIn,
  recipientCookie,
  resource,
  recipient,
  workspace,
  readMemberRelations,
  restoreProvider,
}) {
  const product = config.productId;
  assert(["ledger", "support"].includes(product));
  assert.match(workspace, /^[A-Za-z0-9_-]{1,128}$/);
  const container = process.env.HEIMDALL_RESTORE_POSTGRES_CONTAINER;
  assert.match(container, /^heimdall-records-[a-f0-9]{32}$/);
  const source = new URL(db);
  assert.equal(source.pathname, "/public_runtime");
  const incomplete = process.env.HEIMDALL_PROBE_INCOMPLETE_RESTORE === "1";
  const target = "restore_" + product;
  const login = target + "_login";
  const snapshot = "/tmp/" + target + ".dump";
  const password = randomBytes(24).toString("hex");
  const docker = async (...args) => {
    try {
      return await execute("docker", ["exec", container, ...args], { timeout: 30000 });
    } catch (error) {
      const detail = String(error.stderr ?? "");
      if (detail.includes("permission denied for database"))
        throw new Error("permission denied for database");
      throw new Error("Restore database command failed");
    }
  };
  const sql = async (database, statement) =>
    (
      await docker(
        "psql",
        "-XqAt",
        "-v",
        "ON_ERROR_STOP=1",
        "-U",
        "postgres",
        "-d",
        database,
        "-c",
        statement,
      )
    ).stdout.trim();
  const where = `product_id='${product}' AND environment='verification'`;
  const hash = createHash("sha256")
    .update("user:" + Buffer.from(JSON.stringify([config.issuer, recipient])).toString("base64url"))
    .digest("hex");
  const identityWhere = `${where} AND identity_hash=decode('${hash}','hex')`;
  const candidateWhere = `${identityWhere} AND workspace_id='${workspace}'`;
  const oldRelations = await readMemberRelations(recipient, workspace);
  assert(oldRelations.includes("member"));

  const accepted = new Set();
  let receipts = 0;
  let sinkRequests = 0;
  const token = randomBytes(24).toString("hex");
  const sink = createServer(async (req, res) => {
    sinkRequests++;
    try {
      assert.equal(req.headers.authorization, "Bearer " + token);
      const chunks = [];
      for await (const chunk of req) chunks.push(chunk);
      const { events, eventIds, ...binding } = JSON.parse(Buffer.concat(chunks).toString());
      assert.equal(binding.productId, product);
      assert.equal(binding.environment, "verification");
      assert.equal(binding.sinkId, "siem");
      assert.deepEqual(
        events.map(({ eventId }) => eventId),
        eventIds,
      );
      for (const id of eventIds) {
        assert(!accepted.has(id), "Unexpected duplicate delivery");
        accepted.add(id);
        receipts++;
      }
      res
        .writeHead(200, { "content-type": "application/json" })
        .end(JSON.stringify({ ...binding, eventIds }));
    } catch {
      res.writeHead(400).end();
    }
  }).listen(0, "127.0.0.1");
  await once(sink, "listening");
  const [auditCommand, ...auditArgs] = commands.audit;
  const deliver = async (database) => {
    const reply = await execute(auditCommand, auditArgs, {
      timeout: 45000,
      env: {
        ...variables,
        DATABASE_URL: database,
        HEIMDALL_AUDIT_ENDPOINT: `http://127.0.0.1:${sink.address().port}/events`,
        HEIMDALL_AUDIT_TOKEN: token,
        HEIMDALL_AUDIT_TRANSPORT_PROFILE: "loopback",
      },
    }).catch((error) => {
      const code = /"auditJob":"([a-z_]+)"/.exec(String(error.stderr ?? ""))?.[1];
      throw new Error(
        "Restore audit delivery command failed (" + product + "/" + (code ?? "process") + ")",
      );
    });
    return JSON.parse(reply.stdout);
  };
  try {
    await docker(
      "pg_dump",
      "-U",
      "postgres",
      "-d",
      "public_runtime",
      "-Fc",
      "--no-owner",
      "-f",
      snapshot,
    );
    const delivered = await deliver(db);
    assert(delivered.delivered > 0 && receipts === delivered.delivered);
    const acknowledged = JSON.parse(
      await sql(
        "public_runtime",
        `SELECT json_object_agg(event_id,delivered_at)
      FROM heimdall.audit_deliveries WHERE ${where} AND sink_id='siem' AND state='delivered';`,
      ),
    );
    assert.deepEqual(Object.keys(acknowledged).sort(), [...accepted].sort());

    const revocation = await restoreProvider.revoke();
    assert.match(revocation, /^[A-Za-z0-9_-]{1,128}$/);
    assert.deepEqual(await readMemberRelations(recipient, workspace), []);
    let reactivated = false;
    try {
      await restoreProvider.disable();
      // Reapply the authoritative provider observation through the storage identity gate.
      const { Pool } = createRequire(
        new URL("../../examples/ledger/package.json", import.meta.url),
      )("pg");
      const { createPostgresStorage } = await import("../../sdk/typescript/dist/postgres.js");
      const pool = new Pool({ connectionString: db });
      try {
        const identity = createPostgresStorage(pool, {
          productId: product,
          environment: "verification",
          auditSinks: ["siem"],
        }).identity;
        await identity.invalidate(
          {
            issuer: config.issuer,
            subject: recipient,
            reason: "identity_inactive",
            context: "bearer",
            tokenDigest: "a".repeat(64),
          },
          AbortSignal.timeout(5000),
        );
      } finally {
        await pool.end();
      }
      const denial = await sql(
        "public_runtime",
        `SELECT observed_at FROM heimdall.identity_denials WHERE ${identityWhere};`,
      );
      assert.match(denial, /^[0-9]+$/);
      await stop();
      const unfinished = `SELECT count(*) FROM heimdall_managed.operations
        WHERE ${where} AND state NOT IN ('completed','cancelled');`;
      assert.equal(await sql("public_runtime", unfinished), "0", "Source work must be resolved");

      await sql(
        "postgres",
        `CREATE ROLE ${login} LOGIN PASSWORD '${password}' IN ROLE heimdall_public_runtime;`,
      );
      await sql("postgres", `CREATE DATABASE ${target} ALLOW_CONNECTIONS false;`);
      await sql(
        "postgres",
        `REVOKE CONNECT ON DATABASE ${target} FROM PUBLIC; ALTER DATABASE ${target} ALLOW_CONNECTIONS true;`,
      );
      await docker(
        "pg_restore",
        "-U",
        "postgres",
        "-d",
        target,
        "--single-transaction",
        "--exit-on-error",
        "--no-owner",
        snapshot,
      );
      await assert.rejects(
        docker(
          "env",
          "PGPASSWORD=" + password,
          "psql",
          "-XqAt",
          "-h",
          "127.0.0.1",
          "-U",
          login,
          "-d",
          target,
          "-c",
          "SELECT 1",
        ),
        /permission denied for database/,
      );
      assert.equal(await sql(target, unfinished), "0", "Restored work must be resolved");
      assert.equal(
        await sql(
          target,
          `SELECT count(*) FROM heimdall_managed.membership_candidates WHERE ${candidateWhere};`,
        ),
        "1",
      );
      assert.equal(
        await sql(target, `SELECT count(*) FROM heimdall.identity_denials WHERE ${identityWhere};`),
        "0",
      );
      assert.notEqual(
        await sql(
          target,
          `SELECT count(*) FROM heimdall.records WHERE ${where} AND kind='session';`,
        ),
        "0",
      );
      const receivedIds = [...accepted].sort();
      assert(receivedIds.every((id) => /^[A-Za-z0-9_-]{1,128}$/.test(id)));
      const ids = receivedIds.map((id) => "'" + id + "'").join(",");
      const acknowledgements = receivedIds
        .map((id) => {
          assert(Number.isSafeInteger(acknowledged[id]) && acknowledged[id] > 0);
          return `('${id}',${acknowledged[id]}::bigint)`;
        })
        .join(",");
      assert.equal(
        await sql(
          target,
          `SELECT count(*) FROM heimdall.audit_deliveries WHERE ${where}
        AND sink_id='siem' AND state='pending' AND event_id IN (${ids});`,
        ),
        String(receivedIds.length),
      );

      const url = new URL(source);
      url.pathname = "/" + target;
      url.username = login;
      url.password = password;
      if (incomplete) {
        // An operation known to have run is absent from the recovered journal.
        const removedOperation = `SELECT count(*) FROM heimdall_managed.operations
          WHERE ${where} AND operation_id='${revocation}' AND state='completed';`;
        assert.equal(await sql("public_runtime", removedOperation), "1");
        assert.equal(await sql(target, removedOperation), "0");
        // Withhold one receipt from the recovery inventory. The source ledger proves the gap.
        const availableReceipts = { ...acknowledged };
        delete availableReceipts[receivedIds[0]];
        const unknownReceipts = receivedIds.filter((id) => !Object.hasOwn(availableReceipts, id));
        assert.equal(unknownReceipts.length, 1);
        const before = sinkRequests;
        // The operator leaves CONNECT revoked. Try the actual product and its delivery
        // executable with the fenced runtime credential before verifying preserved state.
        await assert.rejects(
          restart({
            DATABASE_URL: url.href,
            HEIMDALL_KEY_DATABASE_URL: url.href,
            HEIMDALL_EXPORT_DATABASE_URL: "",
            [product === "ledger" ? "LEDGER_DATABASE_URL" : "SUPPORT_DATABASE_URL"]: url.href,
          }),
          /Reference backend failed readiness/,
        );
        await stop();
        await assert.rejects(deliver(url.href), /Restore audit delivery command failed/);
        assert.equal(sinkRequests, before);
        assert.equal(receipts, receivedIds.length);
        assert.equal(
          await sql("postgres", `SELECT has_database_privilege('${login}','${target}','CONNECT');`),
          "f",
        );
        assert.equal(
          await sql(
            target,
            `SELECT count(*) FROM heimdall.audit_deliveries WHERE ${where}
            AND sink_id='siem' AND state='pending' AND event_id IN (${ids});`,
          ),
          String(receivedIds.length),
        );
        return {
          product,
          scopedRead: 200,
          incompleteRestore: {
            revocationMissingFromRestoredJournal: true,
            identityDenialMissingFromRestore: true,
            withheldSinkReceipts: unknownReceipts.length,
            backendReady: false,
            deliveryWorkerBlocked: true,
            confirmedReceiptsNotRedelivered: receivedIds.length,
            runtimeFenced: true,
          },
        };
      }

      // The fixture knows the post-backup changes. Apply only those facts to the isolated target.
      const recoveryKey = randomBytes(32);
      const fingerprint = createHash("sha256")
        .update("heimdall.record-key-usage.v1\0")
        .update(recoveryKey)
        .digest("hex");
      await sql(
        target,
        `BEGIN;
        DELETE FROM heimdall.records WHERE ${where};
        UPDATE heimdall_keys.record_key_usage SET state='retired' WHERE state='active';
        INSERT INTO heimdall_keys.record_key_usage(fingerprint,encryption_limit)
          VALUES(decode('${fingerprint}','hex'),100000);
        INSERT INTO heimdall.identity_denials(product_id,environment,identity_hash,observed_at,reason)
          VALUES('${product}','verification',decode('${hash}','hex'),${denial},'inactive');
        DELETE FROM heimdall_managed.membership_candidates WHERE ${candidateWhere};
        UPDATE heimdall.audit_deliveries d SET state='delivered',delivered_at=receipt.ack_at
          FROM (VALUES ${acknowledgements}) AS receipt(id,ack_at)
          WHERE ${where} AND sink_id='siem' AND state='pending' AND d.event_id=receipt.id;
        COMMIT;`,
      );
      assert.equal(
        await sql(
          target,
          `SELECT count(*) FROM heimdall.audit_deliveries WHERE ${where}
        AND sink_id='siem' AND state='delivered' AND event_id IN (${ids});`,
        ),
        String(receivedIds.length),
      );

      await sql("postgres", `GRANT CONNECT ON DATABASE ${target} TO ${login};`);

      // Export to the same receipt endpoint after reconciling known receipts.
      const before = receipts;
      const pending = Number(
        await sql(
          target,
          `SELECT count(*) FROM heimdall.audit_deliveries WHERE ${where} AND sink_id='siem' AND state='pending';`,
        ),
      );
      assert(pending > 0);
      while (receipts - before < pending) {
        const previous = receipts;
        const replay = await deliver(url.href);
        assert(replay.delivered > 0);
        assert.equal(replay.quarantined, 0);
        assert.equal(receipts - previous, replay.delivered);
      }
      assert.equal(receipts - before, pending);

      await restart({
        DATABASE_URL: url.href,
        HEIMDALL_KEY_DATABASE_URL: url.href,
        HEIMDALL_EXPORT_DATABASE_URL: "",
        [product === "ledger" ? "LEDGER_DATABASE_URL" : "SUPPORT_DATABASE_URL"]: url.href,
        HEIMDALL_RECORD_KEY_ID: "recovery",
        HEIMDALL_RECORD_KEYS: JSON.stringify({ recovery: recoveryKey.toString("base64") }),
      });
      const stale = await request(resource, { headers: { cookie: recipientCookie } });
      assert.equal(stale.status, 401);
      await stale.body?.cancel();
      const owner = await signIn();
      const current = await request(resource, { headers: { cookie: owner } });
      assert.equal(current.status, 200);
      await current.body?.cancel();
      await restoreProvider.activate();
      reactivated = true;
      await signIn(true, 401);
      assert.deepEqual(await readMemberRelations(recipient, workspace), []);
      return {
        product,
        scopedRead: 200,
        coordinatedRestore: {
          runtimeFenced: true,
          revokedMembershipStayedAbsent: true,
          identityDenialReconciled: true,
          staleCookie: 401,
          reactivatedIdentity: 401,
          freshOwner: 200,
          confirmedReceiptsNotRedelivered: receivedIds.length,
          remainingDeliveries: pending,
        },
      };
    } finally {
      if (!reactivated) await restoreProvider.activate();
    }
  } finally {
    await new Promise((resolve) => sink.close(resolve));
    await docker("rm", "-f", snapshot);
  }
}
