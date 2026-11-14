import { expect, test } from "vitest";
import { verifySessionStore } from "../src/sessionstoretest.js";
import { memorySessionStore } from "./fixtures/memory-session-store.js";

test("the session-store conformance suite accepts an atomic store", async () => {
  const { store } = memorySessionStore();
  await expect(verifySessionStore(store, store)).resolves.toBeUndefined();
});
