-- Create the job login through secret-managed deployment tooling before applying.
-- Supply runtime_role and audit_role as psql variables.
GRANT :"runtime_role" TO :"audit_role";
GRANT UPDATE (state, attempts, next_attempt_at, delivered_at)
    ON heimdall.audit_deliveries TO :"audit_role";
