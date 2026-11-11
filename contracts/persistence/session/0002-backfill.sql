-- Prepared v2 deployment checkpoint; apply with 0002.sql while v1 writers are drained.
-- This is deployment-owned state, never a runtime-created table or readiness signal.
CREATE TABLE heimdall.audit_decision_backfill (
    product_id text NOT NULL CHECK (product_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    environment text NOT NULL CHECK (environment ~ '^[A-Za-z0-9_-]{1,64}$'),
    phase text NOT NULL DEFAULT 'requests' CHECK (phase IN ('requests','outcomes','complete')),
    last_event_id text CHECK (last_event_id ~ '^[A-Za-z0-9_-]{1,128}$'),
    scanned_count bigint NOT NULL DEFAULT 0 CHECK (scanned_count BETWEEN 0 AND 9007199254740991),
    expected_count bigint CHECK (expected_count BETWEEN 0 AND 9007199254740991),
    CHECK ((phase='requests' AND expected_count IS NULL)
        OR (phase<>'requests' AND expected_count IS NOT NULL)),
    CHECK (phase<>'complete' OR (last_event_id IS NULL AND scanned_count=expected_count)),
    CHECK (phase='complete' OR ((last_event_id IS NULL)=(scanned_count=0))),
    PRIMARY KEY (product_id,environment)
);
REVOKE ALL ON heimdall.audit_decision_backfill FROM PUBLIC;
