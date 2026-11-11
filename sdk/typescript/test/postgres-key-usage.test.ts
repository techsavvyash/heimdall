import { Pool } from "pg";
import { expect, it } from "vitest";
import {
  reserveRecordKeyUsage,
  registerRecordKeyUsage,
  retireRecordKeyUsage,
} from "../src/internal/postgres/key-usage.js";
import { checkKeyUsageReadiness } from "../src/internal/postgres/readiness.js";
it("key accounting rejects invalid input and cancellation without a connection", async () => {
  const pool = new Pool();
  try {
    await expect(reserveRecordKeyUsage(pool, "bad")).rejects.toThrow("invalid_argument");
    await expect(reserveRecordKeyUsage(pool, "0".repeat(64), AbortSignal.abort())).rejects.toThrow(
      "cancelled",
    );
    await expect(checkKeyUsageReadiness(pool, AbortSignal.abort())).rejects.toThrow("cancelled");
    expect(pool.totalCount).toBe(0);
  } finally {
    await pool.end();
  }
});

it.skipIf(!process.env.HEIMDALL_TEST_KEYS_ADMIN)(
  "live key registration, concurrent reservations and retirement",
  async () => {
    const admin = new Pool({ connectionString: process.env.HEIMDALL_TEST_KEYS_ADMIN });
    const runtime = new Pool({ connectionString: process.env.HEIMDALL_TEST_KEYS_RUNTIME });
    const fingerprint = "1".repeat(64);
    try {
      await reserveRecordKeyUsage(runtime, "6".repeat(64));
      await expect(reserveRecordKeyUsage(runtime, "6".repeat(64))).rejects.toThrow("unavailable");
      await expect(registerRecordKeyUsage(admin, fingerprint, 7, false)).rejects.toThrow(
        "invalid_argument",
      );
      const readonly = new Pool({
        connectionString: process.env.HEIMDALL_TEST_KEYS_ADMIN,
        options: "-c default_transaction_read_only=on",
      });
      try {
        await expect(registerRecordKeyUsage(readonly, "9".repeat(64), 1, true)).rejects.toThrow(
          "unavailable",
        );
      } finally {
        await readonly.end();
      }
      await registerRecordKeyUsage(admin, fingerprint, 7, true);
      const outcomes = await Promise.all(
        Array.from({ length: 20 }, () =>
          reserveRecordKeyUsage(runtime, fingerprint).then(
            () => true,
            () => false,
          ),
        ),
      );
      expect(outcomes.filter(Boolean)).toHaveLength(7);
      await registerRecordKeyUsage(admin, fingerprint, 7, true);
      expect(
        (
          await admin.query(
            "SELECT used::int FROM heimdall_keys.record_key_usage WHERE fingerprint=decode($1,'hex')",
            [fingerprint],
          )
        ).rows[0].used,
      ).toBe(7);
      await expect(registerRecordKeyUsage(admin, fingerprint, 8, true)).rejects.toThrow("conflict");
      await retireRecordKeyUsage(admin, fingerprint);
      await retireRecordKeyUsage(admin, fingerprint);
      await expect(registerRecordKeyUsage(admin, fingerprint, 7, true)).rejects.toThrow("conflict");
      await expect(reserveRecordKeyUsage(runtime, fingerprint)).rejects.toThrow("unavailable");
      await expect(registerRecordKeyUsage(runtime, "2".repeat(64), 1, true)).rejects.toThrow(
        "unavailable",
      );
      await expect(retireRecordKeyUsage(runtime, fingerprint)).rejects.toThrow("unavailable");
      await expect(reserveRecordKeyUsage(runtime, "f".repeat(64))).rejects.toThrow("unavailable");
    } finally {
      await Promise.all([admin.end(), runtime.end()]);
    }
  },
);
it.skipIf(!process.env.HEIMDALL_TEST_KEYS_LOSS)(
  "key reservation COMMIT loss never grants allowance",
  async () => {
    const pool = new Pool({ connectionString: process.env.HEIMDALL_TEST_KEYS_LOSS });
    try {
      await expect(reserveRecordKeyUsage(pool, "3".repeat(64))).rejects.toThrow("outcome_unknown");
    } finally {
      await pool.end();
    }
  },
);
