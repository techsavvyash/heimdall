-- Install after 0006 in a deployment-owned transaction.
ALTER TABLE heimdall_directory.enrollments
    ADD COLUMN reactivation_revision bigint NOT NULL DEFAULT 0 CHECK (reactivation_revision >= 0),
    DROP CONSTRAINT enrollments_pkey,
    ADD PRIMARY KEY (product_id, environment, issuer, organization_id, subject, workspace_id, rule_id, reactivation_revision);
ALTER TABLE heimdall_directory.deactivations
    ADD COLUMN reactivation_released boolean NOT NULL DEFAULT false;
