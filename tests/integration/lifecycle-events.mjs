import assert from "node:assert/strict";

// Capability probe only: durable checkpoints and product routing belong in the SDK worker.
export async function probeLifecycleEvents({ ok, request, orgs, users, scopedToken, runId }) {
  const path = "/admin/v1/events/_search";
  const filter = { resourceOwner: orgs[0], aggregateId: users[0], asc: true, limit: 2 };
  const denied = await request("POST", path, filter, scopedToken);
  assert.equal(denied.status, 403, "SCIM authority must not grant instance event access");
  const machine = await ok("POST", "/v2/users/new", {
    organizationId: orgs[0],
    username: `event-reader-${runId}`,
    machine: { name: "Disposable event reader" },
  });
  const resource = { instance: true };
  let granted = false;
  try {
    await ok(
      "POST",
      "/zitadel.internal_permission.v2.InternalPermissionService/CreateAdministrator",
      {
        userId: machine.id,
        resource,
        roles: ["IAM_OWNER_VIEWER"],
      },
    );
    granted = true;
    const token = await ok("POST", `/v2/users/${machine.id}/pats`, {
      expirationDate: new Date(Date.now() + 3600000).toISOString(),
    });
    const events = [];
    let sequence = "0";
    let pages = 0;
    for (; pages < 100; pages++) {
      const page = await ok("POST", path, { ...filter, sequence }, token.token);
      const batch = page.events ?? [];
      if (!batch.length) break;
      assert(batch.length <= 2);
      for (const event of batch) {
        assert.equal(event.aggregate.id, users[0]);
        assert.equal(event.aggregate.resourceOwner, orgs[0]);
        assert.equal(typeof event.sequence, "string");
        assert(
          BigInt(event.sequence) > BigInt(sequence),
          "Exclusive ascending cursor must advance",
        );
        sequence = event.sequence;
        events.push(event);
      }
    }
    assert(pages < 100, "Event pagination did not terminate");
    assert(pages > 1, "Expected multiple event pages");
    const types = events.map((event) => event.type.type);
    for (const type of ["user.deactivated", "user.reactivated", "user.removed"])
      assert(types.includes(type), `Missing lifecycle event ${type}; got ${types.join(",")}`);
    const replay = await ok("POST", path, { ...filter, sequence: events[0].sequence }, token.token);
    assert.deepEqual(
      replay.events.map((event) => event.sequence),
      events.slice(1, 3).map((event) => event.sequence),
    );
    const wrongOwner = await ok("POST", path, { ...filter, resourceOwner: orgs[1] }, token.token);
    assert.equal((wrongOwner.events ?? []).length, 0);
    const otherOrg = await ok(
      "POST",
      path,
      { ...filter, resourceOwner: orgs[1], aggregateId: users[1] },
      token.token,
    );
    assert((otherOrg.events ?? []).length > 0, "Instance viewer is not tenant isolated");
    assert(otherOrg.events.every((event) => event.aggregate.resourceOwner === orgs[1]));
    const overlappingSequences = otherOrg.events
      .map((event) => event.sequence)
      .filter((value) => events.some((event) => event.sequence === value));
    assert(overlappingSequences.length > 0, "Expected aggregate-local sequence overlap");
    const unsafeGlobalCursor = await ok(
      "POST",
      path,
      {
        ...filter,
        resourceOwner: orgs[1],
        aggregateId: users[1],
        sequence,
      },
      token.token,
    );
    assert.equal((unsafeGlobalCursor.events ?? []).length, 0);
    const mutation = await request("POST", `/v2/users/${users[1]}/deactivate`, {}, token.token);
    assert.equal(mutation.status, 403);
    // Replaying a historical activation is only a signal to re-read current state.
    assert.equal(
      (await request("GET", `/v2/users/${users[0]}`, undefined, scopedToken)).status,
      404,
    );
    return {
      scimCredentialEventsStatus: denied.status,
      collectorRole: "IAM_OWNER_VIEWER",
      collectorScope: "instance-wide; resourceOwner is a filter, not a credential boundary",
      viewerMutationStatus: mutation.status,
      pages,
      events: events.map((event) => ({ sequence: event.sequence, type: event.type.type })),
      exclusiveCursorReplayMatched: true,
      wrongOwnerEmpty: true,
      otherOrganizationVisible: true,
      overlappingSequences,
      crossAggregateCursorSkippedKnownEvents: true,
      historicalActivationCurrentState: "deleted",
      payloadsOmitted: true,
    };
  } finally {
    if (granted)
      await ok(
        "POST",
        "/zitadel.internal_permission.v2.InternalPermissionService/DeleteAdministrator",
        {
          userId: machine.id,
          resource,
        },
      );
  }
}
