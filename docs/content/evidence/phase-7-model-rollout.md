---
title: Phase 7 model rollout rehearsal
---

The Ledger and Support rollout cases passed against two disposable product stores on the Railway verification deployment of OpenFGA 1.22.0, deployment `cfd87a9f-da43-493c-b659-72ce4a5ba98e`. The Vitest run passed both product cases in 22.63 seconds. It ran the Go model-binding checks with `go test -race` at each stage over the same live tuples. The harness deleted both stores.

| Product | Existing grant | Old model | Candidate model |
| --- | --- | --- | --- |
| Ledger | `approver` grants `invoice.approve` | `01M7C517VK2CKC6JW993DW8RHW` | `01M7C5187RV3A8ZAZ2P3FKC2W4` |
| Support | `agent` grants `ticket.assign` | `01M7C51K1W499MJ2AHH4P0PY4X` | `01M7C51KE3F3QYZX4EZRBD14E6` |

Run ID `3f2d5162-798e-42e1-af75-f8c1d255a5a5`.

```mermaid
sequenceDiagram
  participant Runner as Rehearsal harness
  participant FGA as Railway OpenFGA
  participant SDK as TypeScript and Go model bindings
  Runner->>FGA: Publish old model and seed Acme membership
  Runner->>FGA: Publish candidate with appended roles and permissions
  SDK->>FGA: Verify pins and check existing grants with both models
  Runner->>FGA: Submit batch containing an invalid relation
  FGA-->>Runner: Reject batch
  SDK->>FGA: Check existing allow and new-role deny
  Runner->>FGA: Retry with valid new-role tuple
  SDK->>FGA: Check Acme allow and Globex deny
  SDK->>FGA: Read direct membership using old model
  Note over SDK: SDK returns unavailable for the unknown relation
  Runner->>FGA: Remove candidate-only tuple using candidate model
  SDK->>FGA: Verify original membership and permission
```

Both implementations rejected the old manifest paired with the candidate model ID. The candidate `z_reviewer` role and `z.review` permission preserved existing relation mappings. The compiler test also showed that inserting a name before the existing names reuses the original first relation. The original rehearsal did not publish that configuration. The rollout procedure and recovery limits are in [[operations#model-rollout]].

Run from the repository root with an HTTPS OpenFGA origin in `HEIMDALL_PROBE_OPENFGA_URL` and the verification credential in `HEIMDALL_PROBE_OPENFGA_KEY`. The credential needs permission to create and delete disposable stores.

```sh
HEIMDALL_PROBE_MODEL_ROLLOUT=1 pnpm --filter @heimdall/sdk exec vitest run test/model-rollout.live.test.ts --disableConsoleIntercept
```

This exercises two model and manifest versions through the current TS and Go implementations. It covers a deterministic rejected tuple batch with a corrected retry. Versioned SDK artifact combinations, coordinated adapter restore and recovery after uncertain writes remain separate release checks under [[phase-7-release-qualification]].


## Provisioning inspection

Run `2fb725b0-2cf5-4848-9701-1ca1a57c61c6` repeated both live cases on Railway in 31.86 seconds. For each product, both SDKs returned a compatible report for the append-only candidate and an incompatible report for each change to ownership, relation ordinals, an existing permission's grants or an existing role's delegation. Both rejected a manifest paired with a different published model and rejected a candidate in a different store. The probe published incompatible models only in its disposable test stores and did not change live application pins. Each probe store was removed after the test.

The TypeScript check imports the provisioning entry point and invokes the Go check under the race detector with the same inputs and expected reports. See [[operations#model-rollout]] for both public APIs.
