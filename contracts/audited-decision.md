# Internal audited decision execution

The TS AuditedDecision and Go auditflow.Decision compose the existing durable
append port with a **read-only** authorization evaluator. The caller supplies
trusted, schema-valid requested-event metadata and a structural validator.
The runner also enforces built-in semantic validation and its configured
product/environment. Authenticating the actor and binding the tenant/model to
runtime configuration remain the calling SDK factory's responsibility. This is
not a public endpoint that accepts browser-supplied actors or arbitrary evaluators.

Each invocation snapshots its input and assigns fresh request-event and decision
IDs. A repeated API invocation is a new decision, not an idempotent replay of a
previous evaluation. The request and terminal events share the decision ID; they
have distinct event IDs. Parent/request correlation metadata is preserved.
Append retries, if performed by the durable port, must retain the exact event.
The runner never automatically retries an evaluation or an uncertain append.

1. Validate and durably append authorization.requested. Verify the returned
   product/environment, event ID, recorded timestamp and full payload digest.
2. Only after that receipt, call the bounded read-only evaluator.
3. Record authorization.completed with allow/deny or the sanitized error outcome.
   Unknown exceptions become unavailable; never persist exception text or tokens.
4. Require a matching durable terminal receipt before returning either true,
   false or the evaluation error. Missing/mismatched/failed/uncertain audit
   receipts take precedence and return audit_unavailable. No protected work is
   released, even if the underlying audit transaction actually committed.

Cancellation or an expired deadline prevents evaluation and can interrupt a
non-cooperative evaluator's wait. Arbitrary callbacks cannot be forcibly stopped;
they must honor cancellation and must never mutate business or authorization
state. A late callback result cannot replace the already-recorded outcome.

The operation budget is five seconds from invocation, shortened by its caller.
Audit persistence uses its own bounded five-second context so a cancelled caller
cannot suppress the required cancellation record. The requested append consumes
the operation budget; terminal finalization may add up to five seconds. An already
cancelled invocation can therefore take up to ten seconds to persist its two
audit records. This is bounded cleanup, not permission to run the evaluator after
its deadline. Structural validation and clocks are synchronous trusted functions.

The terminal event describes the observed evaluation result. If cancellation
arrives during its commit, an allow event may commit but the caller receives
cancelled/deadline_exceeded and no allow value. Such an audit does not assert
that protected business work was released or committed. Both SDKs test this race.

A crash or failed terminal append leaves the requested event unresolved, unless
the terminal append actually committed. Recovery must reconcile durable records
and append outcome_unknown for genuinely unresolved decisions; it must not rerun
the historical evaluation. The recovery scanner, public authorization/session
composition, custom events and external delivery workers remain separate release
requirements. This runner does not perform mutations or replace their authorized
intent/quarantine/reconciliation workflow.
