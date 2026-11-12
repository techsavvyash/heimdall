import { OperationRecords } from "../src/internal/postgres/operations.js";
export const operationRecords = (...args: ConstructorParameters<typeof OperationRecords>) =>
  process.env.HEIMDALL_TEST_MANAGED_OPERATIONS_V4 === "1"
    ? OperationRecords.forRetainedRuntime(...args)
    : new OperationRecords(...args);
