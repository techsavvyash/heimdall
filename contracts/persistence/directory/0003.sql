-- Install after 0002 in a deployment-owned transaction.
ALTER TABLE heimdall_directory.deactivations
    ADD COLUMN cleanup_plan jsonb
        CHECK (cleanup_plan IS NULL OR (jsonb_typeof(cleanup_plan) = 'array' AND octet_length(cleanup_plan::text) <= 20000));
