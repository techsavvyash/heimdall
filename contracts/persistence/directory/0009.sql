-- Install after 0008 before upgrading identity collectors.
ALTER TABLE heimdall_directory.event_checkpoints
    ADD COLUMN mapping_revision bigint NOT NULL DEFAULT 0
        CHECK (mapping_revision BETWEEN 0 AND 9007199254740991);
