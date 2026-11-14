import assert from "node:assert/strict";
import { createServer } from "node:http";
import { once } from "node:events";
import { randomBytes, randomUUID } from "node:crypto";
import { execFile, spawn } from "node:child_process";
import { promisify } from "node:util";
import { setTimeout as sleep } from "node:timers/promises";

// Destructive only to a newly named, isolated Compose project owned by this run.
const runId = randomUUID();
const project = `heimdall-recovery-${runId.slice(0, 8)}`;
const password = randomBytes(32).toString("hex");
const oldKey = randomBytes(32).toString("hex");
const newKey = randomBytes(32).toString("hex");
const env = {
  ...process.env,
  HEIMDALL_PROBE_POSTGRES_PASSWORD: password,
  HEIMDALL_PROBE_OPENFGA_KEY: oldKey,
};
const compose = [
  "compose",
  "-p",
  project,
  "-f",
  "deploy/verification/compose.yaml",
  "-f",
  "deploy/verification/recovery.compose.yaml",
];
const execute = promisify(execFile);
const observations = {};
const journalDB = process.env.HEIMDALL_RECOVERY_DATABASE_URL;
const sdk = journalDB
  ? await import("../../sdk/typescript/test/fixtures/openfga-sdk-recovery.mjs")
  : null;
const evidenceId = runId;
const committedId = "committed-response-lost";
const interruptedId = "blocked";
let recoveryInput;
async function journal(phase, operationId, generation) {
  const { stdout } = await execute(
    "go",
    ["test", "./provisioning", "-run", "^TestPinnedWorkspaceRecoveryProbe$", "-count=1", "-v"],
    {
      cwd: "sdk/go",
      env: {
        ...process.env,
        HEIMDALL_TEST_DATABASE_URL: journalDB,
        HEIMDALL_PROBE_OPENFGA_KEY: env.HEIMDALL_PROBE_OPENFGA_KEY,
        HEIMDALL_RECOVERY_PROBE_INPUT: JSON.stringify({
          ...recoveryInput,
          phase,
          operationId,
          generation,
          evidenceId,
        }),
      },
      timeout: 60000,
    },
  ).catch(() => {
    throw new Error("SDK journal probe failed in phase " + phase);
  });
  return JSON.parse(stdout.match(/^RECOVERY_PROBE (.+)$/m)[1]);
}
function quarantined(result) {
  assert.deepEqual(result, {
    state: "dispatching",
    generation: 2,
    blocked: true,
    lifecycle: "pending",
    indexed: false,
    audited: 0,
  });
}
function activated(result) {
  assert.deepEqual(result, {
    state: "completed",
    generation: 5,
    blocked: false,
    lifecycle: "active",
    indexed: true,
    audited: 3,
  });
}
let lockProcess;
let responseDropper;
let lockCompletion;
async function docker(args) {
  return (
    await execute("docker", args, { env, timeout: 180000, maxBuffer: 2 * 1024 * 1024 })
  ).stdout.trim();
}
async function sql(query) {
  return docker([
    ...compose,
    "exec",
    "-T",
    "postgres",
    "psql",
    "-U",
    "heimdall_probe",
    "-d",
    "openfga",
    "-v",
    "ON_ERROR_STOP=1",
    "-Atc",
    query,
  ]);
}
async function request(path, body, key = env.HEIMDALL_PROBE_OPENFGA_KEY) {
  const r = await fetch(`http://127.0.0.1:8080${path}`, {
    method: body === undefined ? "GET" : "POST",
    headers: { authorization: `Bearer ${key}`, "content-type": "application/json" },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    signal: AbortSignal.timeout(15000),
    redirect: "error",
  });
  const text = await r.text();
  return { status: r.status, body: text ? JSON.parse(text) : null };
}
async function ok(path, body) {
  const r = await request(path, body);
  assert(r.status >= 200 && r.status < 300, `HTTP ${r.status}`);
  return r.body;
}
async function ready() {
  for (let i = 0; i < 30; i++) {
    try {
      if ((await request("/healthz")).status === 200) return;
    } catch {
      /* bounded readiness polling */
    }
    await sleep(200);
  }
  throw new Error("OpenFGA readiness deadline");
}
async function waitSQL(query, predicate) {
  for (let i = 0; i < 30; i++) {
    const result = await sql(query);
    if (predicate(result)) return result;
    await sleep(50);
  }
  throw new Error("Database observation deadline");
}
try {
  console.error(`Recovery probe ${runId}: starting isolated pinned services`);
  await docker([...compose, "up", "-d", "--wait", "postgres"]);
  await docker([...compose, "run", "--rm", "migrate"]);
  await docker([...compose, "up", "-d", "openfga"]);
  await ready();
  const store = await ok("/stores", { name: `recovery-${runId}` });
  const model = await ok(
    `/stores/${store.id}/authorization-models`,
    sdk?.recoveryModel ?? {
      schema_version: "1.1",
      type_definitions: [
        { type: "user" },
        {
          type: "workspace",
          relations: { member: { this: {} } },
          metadata: { relations: { member: { directly_related_user_types: [{ type: "user" }] } } },
        },
      ],
    },
  );
  if (sdk) {
    recoveryInput = sdk.recoveryProbeConfig(project, store.id, model.authorization_model_id);
    for (const id of [committedId, interruptedId]) quarantined(await journal("dispatch", id));
  }
  const user = sdk
    ? "user:" +
      Buffer.from(JSON.stringify([recoveryInput.config.issuer, "recovery-probe"])).toString(
        "base64url",
      )
    : "user:maya";
  const tuple = { user, relation: "member", object: "workspace:" + interruptedId };
  const committedTuple = { ...tuple, object: "workspace:" + committedId };
  const writePayload = (member) => ({
    authorization_model_id: model.authorization_model_id,
    writes: {
      tuple_keys: sdk ? [member, { ...member, relation: sdk.recoveryOwnerRelation }] : [member],
    },
  });
  const write = writePayload(tuple);
  const committedWrite = writePayload(committedTuple);
  let completeDrop;
  const dropped = new Promise((resolve) => {
    completeDrop = resolve;
  });
  responseDropper = createServer(async (req, res) => {
    try {
      assert.equal(req.method, "POST");
      assert.equal(req.url, "/drop");
      await ok(`/stores/${store.id}/write`, committedWrite);
      res.destroy();
      completeDrop(true);
    } catch {
      res.destroy();
      completeDrop(false);
    }
  });
  responseDropper.listen(0, "127.0.0.1");
  await once(responseDropper, "listening");
  const address = responseDropper.address();
  const clientLost = await fetch(`http://127.0.0.1:${address.port}/drop`, {
    method: "POST",
    signal: AbortSignal.timeout(15000),
    redirect: "error",
  }).then(
    () => false,
    () => true,
  );
  assert.equal(
    await dropped,
    true,
    "Fault proxy must observe upstream commit before dropping response",
  );
  assert(clientLost, "Product-side client must not receive a successful response");
  await new Promise((resolve) => responseDropper.close(resolve));
  responseDropper = undefined;
  assert.equal(
    (
      await ok(`/stores/${store.id}/read`, {
        tuple_key: committedTuple,
        consistency: "HIGHER_CONSISTENCY",
      })
    ).tuples.length,
    1,
  );
  observations.committedWriteResponseLost = true;

  // Keep a write inside PostgreSQL, beyond the product process boundary.
  lockProcess = spawn(
    "docker",
    [
      ...compose,
      "exec",
      "-T",
      "postgres",
      "psql",
      "-U",
      "heimdall_probe",
      "-d",
      "openfga",
      "-v",
      "ON_ERROR_STOP=1",
      "-Atc",
      "BEGIN; LOCK TABLE tuple IN ACCESS EXCLUSIVE MODE; SELECT pg_sleep(120); ROLLBACK;",
    ],
    { env, stdio: "ignore" },
  );
  lockCompletion = new Promise((resolve, reject) => {
    lockProcess.once("exit", resolve);
    lockProcess.once("error", reject);
  });
  const lockPid = await waitSQL(
    "SELECT pid FROM pg_locks WHERE relation='tuple'::regclass AND mode='AccessExclusiveLock' AND granted",
    (value) => /^\d+$/.test(value),
  );
  const pending = request(`/stores/${store.id}/write`, write).then(
    (r) => ({ status: r.status }),
    () => ({ transportFailure: true }),
  );
  await waitSQL(
    `SELECT count(*) FROM pg_stat_activity WHERE datname='openfga' AND pid<>${lockPid} AND wait_event_type='Lock'`,
    (value) => Number(value) >= 1,
  );
  observations.writeObservedWaitingInDatabase = true;
  console.error(
    "Dispatched write observed waiting in PostgreSQL; killing the owned OpenFGA process",
  );

  const oldContainer = await docker([...compose, "ps", "-q", "openfga"]);
  assert(oldContainer && !oldContainer.includes("\n"));
  await docker(["kill", "--signal", "KILL", oldContainer]);
  assert.equal(await docker(["inspect", "--format", "{{.State.Running}}", oldContainer]), "false");
  // Server process death does not substitute for database-side quiescence.
  await sql(
    `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='openfga' AND pid<>pg_backend_pid() AND pid<>${lockPid}`,
  );
  assert.equal(
    await sql(
      `SELECT count(*) FROM pg_stat_activity WHERE datname='openfga' AND pid<>pg_backend_pid() AND pid<>${lockPid}`,
    ),
    "0",
  );
  observations.oldServerStoppedAndDatabaseBackendsDrained = true;
  await sql(`SELECT pg_terminate_backend(${lockPid})`);
  await lockCompletion;
  lockProcess = undefined;
  const outcome = await pending;
  assert(
    outcome.transportFailure || outcome.status >= 400,
    "Blocked request must not report success",
  );
  observations.interruptedClientOutcome = outcome;

  env.HEIMDALL_PROBE_OPENFGA_KEY = newKey;
  await docker([...compose, "up", "-d", "--force-recreate", "openfga"]);
  await ready();
  const newContainer = await docker([...compose, "ps", "-q", "openfga"]);
  assert.notEqual(newContainer, oldContainer);
  // Delayed requests still outside the old server must fail after restart.
  const late = await request(`/stores/${store.id}/write`, write, oldKey);
  assert.equal(late.status, 401);
  const read = () =>
    ok(`/stores/${store.id}/read`, { tuple_key: tuple, consistency: "HIGHER_CONSISTENCY" });
  assert.equal(
    (await read()).tuples.length,
    0,
    "Killed/drained write must not appear after recovery",
  );
  observations.delayedOldCredentialWriteStatus = late.status;
  observations.interruptedTupleAbsentAfterRestart = true;
  assert.equal(
    (
      await ok(`/stores/${store.id}/read`, {
        tuple_key: committedTuple,
        consistency: "HIGHER_CONSISTENCY",
      })
    ).tuples.length,
    1,
  );
  if (sdk) {
    for (const id of [committedId, interruptedId]) quarantined(await journal("observe", id));
    const confirmed = await sdk.recoverTypeScript(
      journalDB,
      {
        ...recoveryInput,
        operationId: committedId,
        generation: 2,
        evidenceId,
      },
      newKey,
    );
    assert.equal(confirmed.status, "active");
    activated(await journal("observe", committedId));
    activated(await journal("recover", interruptedId, 2));
    for (const member of [committedTuple, tuple]) {
      for (const relation of ["member", sdk.recoveryOwnerRelation]) {
        const current = await ok(`/stores/${store.id}/read`, {
          tuple_key: { ...member, relation },
          consistency: "HIGHER_CONSISTENCY",
        });
        assert.equal(current.tuples.length, 1);
      }
    }
    assert.equal((await request(`/stores/${store.id}/write`, write, oldKey)).status, 401);
    observations.sdkJournal = {
      typeScript: "recovered committed grant",
      go: "repaired interrupted grant",
      both: "atomic activation, candidate index and three routed recovery audit events",
    };
  } else {
    await ok(`/stores/${store.id}/write`, {
      authorization_model_id: model.authorization_model_id,
      deletes: { tuple_keys: [committedTuple] },
    });
    assert.equal((await request(`/stores/${store.id}/write`, committedWrite, oldKey)).status, 401);
    assert.equal(
      (
        await ok(`/stores/${store.id}/read`, {
          tuple_key: committedTuple,
          consistency: "HIGHER_CONSISTENCY",
        })
      ).tuples.length,
      0,
    );
    observations.committedWriteReconciledAfterQuiescence = true;

    await ok(`/stores/${store.id}/write`, write);
    assert.equal((await read()).tuples.length, 1);
    await ok(`/stores/${store.id}/write`, {
      authorization_model_id: model.authorization_model_id,
      deletes: { tuple_keys: [tuple] },
    });
    assert.equal((await request(`/stores/${store.id}/write`, write, oldKey)).status, 401);
    assert.equal((await read()).tuples.length, 0);
    observations.newCredentialReconcileAndCleanup =
      "confirmed; delayed old credential cannot regrant";
  }
} catch (error) {
  let message = String(error);
  for (const secret of [password, oldKey, newKey])
    message = message.replaceAll(secret, "[REDACTED]");
  console.error(message);
  process.exitCode = 1;
} finally {
  if (responseDropper) {
    responseDropper.closeAllConnections();
    await new Promise((resolve) => responseDropper.close(resolve));
  }
  try {
    await docker([...compose, "down", "--volumes", "--remove-orphans"]);
    if (lockCompletion) await lockCompletion;
    assert.equal(
      await docker(["ps", "-aq", "--filter", `label=com.docker.compose.project=${project}`]),
      "",
    );
    assert.equal(
      await docker([
        "volume",
        "ls",
        "-q",
        "--filter",
        `label=com.docker.compose.project=${project}`,
      ]),
      "",
    );
    observations.ownedProjectAndVolumeRemoved = true;
  } catch {
    console.error(`Cleanup failed for owned project ${project}; inspect before rerunning`);
    process.exitCode = 1;
  }
}
if (!process.exitCode)
  console.log(
    JSON.stringify(
      {
        suite: "openfga-quiescence-local-v1",
        runId,
        observedAt: new Date().toISOString(),
        observations,
        limitations: [
          "single local OpenFGA replica and isolated database",
          "all API credentials rotated; no other credential paths",
          "not yet verified with Railway deployment controls",
          ...(sdk
            ? [
                "fixture initiates journal and provider dispatch separately; deployment proof is supplied by the test operator",
              ]
            : ["service-only mode"]),
          "deployment-wide maintenance; not per-workspace fencing",
        ],
      },
      null,
      2,
    ),
  );
