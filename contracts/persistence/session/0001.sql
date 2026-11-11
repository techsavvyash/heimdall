-- Shared PostgreSQL foundation. Execute once in a deployment-owned transaction.
CREATE SCHEMA heimdall;
REVOKE ALL ON SCHEMA heimdall FROM PUBLIC;

CREATE TABLE heimdall.schema_metadata (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    database_version integer NOT NULL CHECK (database_version BETWEEN 1 AND 1000),
    state text NOT NULL CHECK (state IN ('ready', 'migrating')),
    reader_min integer NOT NULL CHECK (reader_min BETWEEN 1 AND 1000),
    reader_max integer NOT NULL CHECK (reader_max BETWEEN reader_min AND 1000),
    writer_min integer NOT NULL CHECK (writer_min BETWEEN 1 AND 1000),
    writer_max integer NOT NULL CHECK (writer_max BETWEEN writer_min AND 1000)
);
CREATE TABLE heimdall.migrations (
    version integer PRIMARY KEY CHECK (version BETWEEN 1 AND 1000),
    checksum text NOT NULL CHECK (checksum ~ '^[a-f0-9]{64}$'),
    applied_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

-- Runtime gets USAGE only: it cannot reset this sequence. Revisions are decimal
-- strings in TS/Go storage APIs, never JS floating point numbers.
CREATE SEQUENCE heimdall.record_revision_seq AS bigint NO CYCLE;
CREATE TABLE heimdall.records (
    product_id text NOT NULL CHECK (product_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    environment text NOT NULL CHECK (environment ~ '^[A-Za-z0-9_-]{1,64}$'),
    kind text NOT NULL CHECK (kind IN ('session', 'login')),
    key_hash bytea NOT NULL CHECK (octet_length(key_hash) = 32),
    revision bigint NOT NULL CHECK (revision > 0),
    key_id text NOT NULL CHECK (key_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    envelope text NOT NULL CHECK (octet_length(envelope) BETWEEN 1 AND 1500000),
    expires_at bigint NOT NULL CHECK (expires_at BETWEEN 0 AND 9007199254740991),
    identity_hash bytea CHECK (identity_hash IS NULL OR octet_length(identity_hash) = 32),
    PRIMARY KEY (product_id, environment, kind, key_hash)
);
CREATE INDEX records_expiry ON heimdall.records (product_id, environment, expires_at);
CREATE INDEX records_identity ON heimdall.records (product_id, environment, identity_hash)
    WHERE identity_hash IS NOT NULL;
CREATE FUNCTION heimdall.assign_record_revision() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    -- Callers cannot preserve or choose a revision, even after delete/recreate.
    NEW.revision := nextval('heimdall.record_revision_seq');
    RETURN NEW;
END;
$$;
REVOKE ALL ON FUNCTION heimdall.assign_record_revision() FROM PUBLIC;
CREATE TRIGGER record_revision BEFORE INSERT OR UPDATE ON heimdall.records
    FOR EACH ROW EXECUTE FUNCTION heimdall.assign_record_revision();

CREATE TABLE heimdall.identity_denials (
    product_id text NOT NULL CHECK (product_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    environment text NOT NULL CHECK (environment ~ '^[A-Za-z0-9_-]{1,64}$'),
    identity_hash bytea NOT NULL CHECK (octet_length(identity_hash) = 32),
    observed_at bigint NOT NULL CHECK (observed_at BETWEEN 0 AND 9007199254740991),
    reason text NOT NULL CHECK (reason IN ('inactive', 'deleted')),
    PRIMARY KEY (product_id, environment, identity_hash)
);

CREATE TABLE heimdall.audit_events (
    product_id text NOT NULL CHECK (product_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    environment text NOT NULL CHECK (environment ~ '^[A-Za-z0-9_-]{1,64}$'),
    event_id text NOT NULL CHECK (event_id ~ '^[A-Za-z0-9_-]{1,128}$'),
    payload_digest bytea NOT NULL CHECK (octet_length(payload_digest) = 32),
    recorded_at bigint NOT NULL CHECK (recorded_at BETWEEN 0 AND 9007199254740991),
    event jsonb NOT NULL CHECK (
        jsonb_typeof(event) = 'object' AND octet_length(event::text) <= 1500000
        AND event ?& ARRAY['eventId', 'productId', 'environment', 'recordedAt']
        AND event->>'eventId' = event_id
        AND event->>'productId' = product_id
        AND event->>'environment' = environment
        AND jsonb_typeof(event->'recordedAt') = 'number'
        AND (event->>'recordedAt')::numeric = recorded_at
    ),
    PRIMARY KEY (product_id, environment, event_id)
);
CREATE TABLE heimdall.audit_deliveries (
    product_id text NOT NULL,
    environment text NOT NULL,
    event_id text NOT NULL,
    sink_id text NOT NULL CHECK (sink_id ~ '^[A-Za-z0-9_-]{1,128}$'),
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'delivered', 'quarantined')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at bigint NOT NULL DEFAULT 0 CHECK (next_attempt_at BETWEEN 0 AND 9007199254740991),
    delivered_at bigint CHECK (delivered_at BETWEEN 0 AND 9007199254740991),
    CHECK ((state = 'delivered') = (delivered_at IS NOT NULL)),
    PRIMARY KEY (product_id, environment, event_id, sink_id),
    FOREIGN KEY (product_id, environment, event_id)
        REFERENCES heimdall.audit_events (product_id, environment, event_id) ON DELETE RESTRICT
);
CREATE INDEX audit_deliveries_pending ON heimdall.audit_deliveries
    (product_id, environment, sink_id, next_attempt_at) WHERE state = 'pending';
