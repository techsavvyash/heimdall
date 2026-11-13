"""Verify committed events for the exact reference onboarding operations."""

import base64
from collections import Counter
import json
import sys


def verify(products, payloads):
    events = [json.loads(base64.urlsafe_b64decode(payload + "===")) for payload in payloads]
    assert {item["product"] for item in products} == {"ledger", "support"}
    assert len(products) == 2
    for item in products:
        product = item["product"]
        operation = item["onboarding"]
        scoped = [e for e in events if e["productId"] == product and e["environment"] == "verification"]
        lifecycle = [e for e in scoped if e.get("operationId") == operation["operationId"]]
        assert lifecycle and all(e["tenantId"] == operation["workspaceId"] for e in lifecycle)
        assert Counter((e["action"], e["outcome"]) for e in lifecycle) == {
            ("workspace.create", "requested"): 1,
            ("create_workspace.dispatch", "attempted"): 1,
            ("create_workspace.confirm", "succeeded"): 1,
            ("create_workspace.complete", "succeeded"): 1,
        }
        actor = lifecycle[0]["actor"]
        assert all(e["actor"] == actor for e in lifecycle)
        decisions = [e for e in scoped if e["actor"] == actor and e["eventType"].startswith("heimdall.authorization.")]
        requests = {e["decisionId"]: e for e in decisions if e["outcome"] == "requested"}
        results = {e["decisionId"]: e for e in decisions if e["eventType"] == "heimdall.authorization.completed"}
        assert requests.keys() == results.keys() and len(decisions) == len(requests) * 2
        for decision, request in requests.items():
            assert all(request.get(k) == results[decision].get(k)
                       for k in ("action", "permission", "tenantId", "target", "parentDecisionId"))
        counts = Counter((e["action"], e["outcome"]) for e in results.values())
        assert counts["workspace.create", "allow"] == 2
        assert counts["workspace.create", "unavailable"] == 1
        assert sum(e.get("reasonCode") == "conflict" and e["action"] == "workspace.create"
                   for e in results.values()) == 1
        assert counts["workspace.operation.read", "allow"] == 1
        assert counts["workspace.operation.resume", "allow"] == 1
        permission = {"ledger": "invoice.read", "support": "ticket.read"}[product]
        for action in ("roles.list", permission):
            assert sum(e["action"] == action and e["outcome"] == "allow" and
                       e["tenantId"] == operation["workspaceId"] for e in results.values()) >= 1
        if "administration" in item:
            for step, kind in (("grant", "add_member"), ("assignment", "assign_roles"),
                               ("removal", "remove_member"), ("regrant", "add_member"),
                               ("transfer", "transfer_owner")):
                receipt = item["administration"][step]
                assert receipt["status"] == "confirmed" and receipt["workspaceId"] == operation["workspaceId"]
                events_for_step = [e for e in scoped if e.get("operationId") == receipt["operationId"]]
                assert events_for_step
                initiating_actor = events_for_step[0]["actor"] if step == "regrant" else actor
                assert (initiating_actor != actor) == (step == "regrant")
                assert all(e["tenantId"] == operation["workspaceId"] and e["actor"] == initiating_actor
                           for e in events_for_step)
                assert Counter((e["action"], e["outcome"]) for e in events_for_step) == {
                    (kind, "requested"): 1, (kind + ".dispatch", "attempted"): 1,
                    (kind + ".confirm", "succeeded"): 1, (kind + ".complete", "succeeded"): 1,
                }
            invitation_decisions = [e for e in scoped if e["tenantId"] == operation["workspaceId"]
                                    and e["action"] in ("invitation.issue", "invitation.accept")]
            assert Counter((e["action"], e["outcome"]) for e in invitation_decisions) == {
                ("invitation.issue", "requested"): 4, ("invitation.issue", "allow"): 3,
                ("invitation.issue", "deny"): 1, ("invitation.accept", "requested"): 2,
                ("invitation.accept", "allow"): 1, ("invitation.accept", "deny"): 1,
            }
            invitation_requests = {e["decisionId"]: e for e in invitation_decisions
                                   if e["outcome"] == "requested"}
            for e in invitation_decisions:
                if e["outcome"] != "requested":
                    assert all(e[key] == invitation_requests[e["decisionId"]][key]
                               for key in ("actor", "action", "tenantId"))
        if "lifecycle" in item:
            lifecycle_events = [e for e in scoped if e["tenantId"] == operation["workspaceId"]
                                and e["action"] in ("workspace.suspend", "workspace.resume")]
            assert Counter((e["action"], e["outcome"]) for e in lifecycle_events) == {
                ("workspace.suspend", "requested"): 2, ("workspace.suspend", "deny"): 1,
                ("workspace.suspend", "allow"): 1, ("workspace.suspend", "succeeded"): 1,
                ("workspace.resume", "requested"): 3, ("workspace.resume", "allow"): 3,
                ("workspace.resume", "succeeded"): 1,
            }
            assert all(e["actor"] == actor and e["eventType"] == "heimdall.policy.completed"
                       for e in lifecycle_events if e["outcome"] == "succeeded")
        print(f"{product}: four lifecycle events, {len(requests)} matched authorization decisions")


if __name__ == "__main__":
    inputs = json.load(sys.stdin)
    verify(inputs["products"], inputs["payloads"])
