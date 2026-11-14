-- Install after 0004 in a deployment-owned transaction.
CREATE TABLE heimdall_directory.enrollments (
    product_id text NOT NULL,
    environment text NOT NULL,
    issuer text NOT NULL,
    organization_id text NOT NULL,
    subject text NOT NULL,
    workspace_id text NOT NULL,
    rule_id text NOT NULL,
    plan jsonb NOT NULL CHECK (jsonb_typeof(plan) = 'array' AND octet_length(plan::text) <= 20000),
    operation_id text,
    PRIMARY KEY (product_id, environment, issuer, organization_id, subject, workspace_id, rule_id)
);
