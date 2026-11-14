import type { Pool } from "pg";
import {
  createDirectoryReader,
  type DirectoryReaderOptions,
} from "../../provisioning/directory-reader.js";
import { IdentityRecords } from "./identity.js";

/** Verify a tracked subject and durably deny it before returning a cleanup candidate. */
export function createDirectoryDeactivation(
  pool: Pool,
  options: DirectoryReaderOptions & { productId: string; environment: string },
) {
  const { issuer, organizationId } = options;
  const reader = createDirectoryReader(options);
  const records = IdentityRecords.retainedRecords(pool, options.productId, options.environment);
  return async function observe(subject: string, signal: AbortSignal) {
    const observed = await reader.read(subject, signal);
    if (observed.state !== "inactive" && observed.state !== "missing")
      return { state: observed.state, sessionsInvalidated: 0 };
    const sessionsInvalidated = await records.recordDirectoryDeactivation(
      issuer,
      subject,
      organizationId,
      observed.state,
      Date.now(),
      signal,
    );
    return { state: observed.state, sessionsInvalidated };
  };
}
