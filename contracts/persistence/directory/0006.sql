-- Install after 0005 in a deployment-owned transaction.
ALTER TABLE heimdall_directory.deactivations
    ADD COLUMN reactivation_started_at bigint
        CHECK (reactivation_started_at BETWEEN 0 AND 9007199254740991);
