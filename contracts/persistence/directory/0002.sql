-- Install after 0001 in a deployment-owned transaction.
CREATE SEQUENCE heimdall_directory.cleanup_revision AS bigint NO CYCLE;
ALTER TABLE heimdall_directory.deactivations
    ADD COLUMN revision bigint NOT NULL DEFAULT nextval('heimdall_directory.cleanup_revision'),
    ADD COLUMN completed boolean NOT NULL DEFAULT false;
CREATE INDEX directory_pending_cleanup ON heimdall_directory.deactivations
    (product_id, environment, issuer, organization_id, revision) WHERE NOT completed;
