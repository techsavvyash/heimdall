import { OperationRecords } from "../src/internal/postgres/operations.js";
export const operationRecords = (...args: ConstructorParameters<typeof OperationRecords>) =>
  process.env.HEIMDALL_TEST_MANAGED_OPERATIONS_V4 === "1"
    ? OperationRecords.forRetainedRuntime(...args)
    : new OperationRecords(...args);

import { WorkspaceRecords } from "../src/internal/postgres/workspaces.js";
import { CandidateRecords } from "../src/internal/postgres/candidates.js";
export const workspaceRecords = (...args: ConstructorParameters<typeof WorkspaceRecords>) =>
  process.env.HEIMDALL_TEST_MANAGED_OPERATIONS_V4 === "1"
    ? WorkspaceRecords.forRetainedRuntime(...args)
    : new WorkspaceRecords(...args);
export const candidateRecords = (...args: ConstructorParameters<typeof CandidateRecords>) =>
  process.env.HEIMDALL_TEST_MANAGED_OPERATIONS_V4 === "1"
    ? CandidateRecords.forRetainedRuntime(...args)
    : new CandidateRecords(...args);

import { ManagedExport } from "../src/internal/postgres/export.js";
export const managedExport = (...args: ConstructorParameters<typeof ManagedExport>) =>
  process.env.HEIMDALL_TEST_MANAGED_OPERATIONS_V4 === "1"
    ? ManagedExport.forRetainedRuntime(...args)
    : new ManagedExport(...args);
