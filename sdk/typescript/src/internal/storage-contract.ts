import { HeimdallError } from "./errors.js";
type Migration = { version: number; checksum: string };
type Policy = {
  readerVersion: number;
  writerVersion: number;
  minDatabaseVersion: number;
  maxDatabaseVersion: number;
  migrations: Migration[];
};
type Metadata = {
  databaseVersion: number;
  state: "ready" | "migrating";
  readers: { min: number; max: number };
  writers: { min: number; max: number };
  migrations: Migration[];
};
const version = (v: number): boolean => Number.isInteger(v) && v >= 1 && v <= 1000;

// Read-only startup check after metadata schema validation; never runs migrations.
export function validateStorageCompatibility(metadata: Metadata, policy: Policy): void {
  if (
    ![
      policy.readerVersion,
      policy.writerVersion,
      policy.minDatabaseVersion,
      policy.maxDatabaseVersion,
    ].every(version) ||
    policy.minDatabaseVersion > policy.maxDatabaseVersion ||
    policy.migrations.length !== policy.maxDatabaseVersion ||
    policy.migrations.some(
      (m, i) => m.version !== i + 1 || m.checksum.length !== 64 || /[^a-f0-9]/.test(m.checksum),
    )
  )
    throw new HeimdallError("invalid_argument");
  if (
    metadata.state !== "ready" ||
    metadata.databaseVersion < policy.minDatabaseVersion ||
    metadata.databaseVersion > policy.maxDatabaseVersion ||
    metadata.readers.min > metadata.readers.max ||
    metadata.writers.min > metadata.writers.max ||
    policy.readerVersion < metadata.readers.min ||
    policy.readerVersion > metadata.readers.max ||
    policy.writerVersion < metadata.writers.min ||
    policy.writerVersion > metadata.writers.max ||
    metadata.migrations.length !== metadata.databaseVersion ||
    metadata.migrations.some(
      (m, i) => m.version !== i + 1 || m.checksum !== policy.migrations[i]?.checksum,
    )
  )
    throw new HeimdallError("unavailable");
}
