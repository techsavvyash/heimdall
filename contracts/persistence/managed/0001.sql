-- Managed application state. Install separately after the shared session/audit schema.
-- Execute in one deployment-owned transaction. Runtime never executes this file.
CREATE SCHEMA heimdall_managed;
REVOKE ALL ON SCHEMA heimdall_managed FROM PUBLIC;

CREATE TABLE heimdall_managed.schema_metadata (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    database_version integer NOT NULL CHECK (database_version BETWEEN 1 AND 1000),
    state text NOT NULL CHECK (state IN ('ready', 'migrating')),
    reader_min integer NOT NULL CHECK (reader_min BETWEEN 1 AND 1000),
    reader_max integer NOT NULL CHECK (reader_max BETWEEN reader_min AND 1000),
    writer_min integer NOT NULL CHECK (writer_min BETWEEN 1 AND 1000),
    writer_max integer NOT NULL CHECK (writer_max BETWEEN writer_min AND 1000)
);
CREATE TABLE heimdall_managed.migrations (
    version integer PRIMARY KEY CHECK (version BETWEEN 1 AND 1000),
    checksum text NOT NULL CHECK (checksum ~ '^[a-f0-9]{64}$'),
    applied_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE SEQUENCE heimdall_managed.revision_seq AS bigint NO CYCLE;
CREATE SEQUENCE heimdall_managed.candidate_position_seq AS bigint NO CYCLE;

CREATE TABLE heimdall_managed.workspaces (
    product_id text NOT NULL CHECK (product_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    environment text NOT NULL CHECK (environment ~ '^[A-Za-z0-9_-]{1,64}$'),
    workspace_id text NOT NULL CHECK (workspace_id ~ '^[A-Za-z0-9_-]{1,128}$'),
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 256 AND octet_length(name) <= 1024),
    lifecycle text NOT NULL CHECK (lifecycle IN ('pending', 'active', 'suspended', 'tombstoned')),
    mutation_blocked boolean NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    created_at bigint NOT NULL CHECK (created_at BETWEEN 0 AND 9007199254740991),
    updated_at bigint NOT NULL CHECK (updated_at BETWEEN created_at AND 9007199254740991),
    PRIMARY KEY (product_id, environment, workspace_id)
);

CREATE TABLE heimdall_managed.operations (
    product_id text NOT NULL,
    environment text NOT NULL,
    operation_id text NOT NULL CHECK (operation_id ~ '^[A-Za-z0-9_-]{1,128}$'),
    workspace_id text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('create_workspace', 'add_member', 'remove_member', 'assign_roles', 'transfer_owner')),
    actor_hash bytea NOT NULL CHECK (octet_length(actor_hash) = 32),
    target_hash bytea NOT NULL CHECK (octet_length(target_hash) = 32),
    key_digest bytea NOT NULL CHECK (octet_length(key_digest) = 32),
    fingerprint bytea NOT NULL CHECK (octet_length(fingerprint) = 32),
    -- UTF-8 JSON bytes preserve escaped U+0000 identities without JSONB coercion.
    -- SDKs validate the schema, canonical digests and all indexed bindings.
    intent_payload bytea NOT NULL CHECK (octet_length(intent_payload) BETWEEN 1 AND 1048576),
    store_id text NOT NULL CHECK (store_id ~ '^[A-Za-z0-9_-]{1,128}$'),
    model_id text NOT NULL CHECK (model_id ~ '^[A-Za-z0-9_-]{1,128}$'),
    model_hash bytea NOT NULL CHECK (octet_length(model_hash) = 32),
    manifest_version integer NOT NULL CHECK (manifest_version > 0),
    state text NOT NULL CHECK (state IN ('pending', 'retryable', 'dispatching', 'confirmed', 'uncertain', 'completed', 'cancelled')),
    dispatch_count bigint NOT NULL CHECK (dispatch_count BETWEEN 0 AND 9007199254740991),
    generation bigint NOT NULL CHECK (generation BETWEEN 1 AND 9007199254740991),
    next_attempt_at bigint NOT NULL CHECK (next_attempt_at BETWEEN 0 AND 9007199254740991),
    created_at bigint NOT NULL CHECK (created_at BETWEEN 0 AND 9007199254740991),
    updated_at bigint NOT NULL CHECK (updated_at BETWEEN created_at AND 9007199254740991),
    CHECK (state NOT IN ('dispatching', 'confirmed', 'uncertain', 'completed') OR dispatch_count >= 1),
    CHECK (state <> 'cancelled' OR dispatch_count = 0),
    PRIMARY KEY (product_id, environment, operation_id),
    UNIQUE (product_id, environment, key_digest),
    FOREIGN KEY (product_id, environment, workspace_id)
        REFERENCES heimdall_managed.workspaces (product_id, environment, workspace_id) ON DELETE RESTRICT
);
-- An uncertain operation retains this slot indefinitely. Time cannot release it.
CREATE UNIQUE INDEX operations_one_unfinished ON heimdall_managed.operations
    (product_id, environment, workspace_id) WHERE state NOT IN ('completed', 'cancelled');
CREATE INDEX operations_due ON heimdall_managed.operations
    (product_id, environment, next_attempt_at, operation_id COLLATE "C") WHERE state IN ('pending', 'retryable');

CREATE TABLE heimdall_managed.membership_candidates (
    product_id text NOT NULL,
    environment text NOT NULL,
    identity_hash bytea NOT NULL CHECK (octet_length(identity_hash) = 32),
    principal text NOT NULL CHECK (principal ~ '^user:[A-Za-z0-9_-]+$' AND octet_length(principal) <= 32768),
    workspace_id text NOT NULL,
    position bigint NOT NULL CHECK (position > 0),
    revision bigint NOT NULL CHECK (revision > 0),
    observed_at bigint NOT NULL CHECK (observed_at BETWEEN 0 AND 9007199254740991),
    PRIMARY KEY (product_id, environment, identity_hash, workspace_id),
    UNIQUE (product_id, environment, position),
    FOREIGN KEY (product_id, environment, workspace_id)
        REFERENCES heimdall_managed.workspaces (product_id, environment, workspace_id) ON DELETE RESTRICT
);
CREATE INDEX candidates_scan ON heimdall_managed.membership_candidates
    (product_id, environment, identity_hash, position);

CREATE FUNCTION heimdall_managed.workspace_revision() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND (NEW.product_id, NEW.environment, NEW.workspace_id, NEW.created_at)
        IS DISTINCT FROM (OLD.product_id, OLD.environment, OLD.workspace_id, OLD.created_at) THEN
        RAISE EXCEPTION 'immutable workspace identity' USING ERRCODE = '23514';
    END IF;
    NEW.revision := nextval('heimdall_managed.revision_seq');
    RETURN NEW;
END;
$$;
CREATE TRIGGER workspace_revision BEFORE INSERT OR UPDATE ON heimdall_managed.workspaces
    FOR EACH ROW EXECUTE FUNCTION heimdall_managed.workspace_revision();

CREATE FUNCTION heimdall_managed.immutable_intent() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF (NEW.product_id, NEW.environment, NEW.operation_id, NEW.workspace_id, NEW.kind,
        NEW.actor_hash, NEW.target_hash, NEW.key_digest, NEW.fingerprint, NEW.intent_payload,
        NEW.store_id, NEW.model_id, NEW.model_hash, NEW.manifest_version, NEW.created_at)
        IS DISTINCT FROM
       (OLD.product_id, OLD.environment, OLD.operation_id, OLD.workspace_id, OLD.kind,
        OLD.actor_hash, OLD.target_hash, OLD.key_digest, OLD.fingerprint, OLD.intent_payload,
        OLD.store_id, OLD.model_id, OLD.model_hash, OLD.manifest_version, OLD.created_at) THEN
        RAISE EXCEPTION 'immutable operation intent' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER immutable_intent BEFORE UPDATE ON heimdall_managed.operations
    FOR EACH ROW EXECUTE FUNCTION heimdall_managed.immutable_intent();

CREATE FUNCTION heimdall_managed.candidate_revision() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.position := nextval('heimdall_managed.candidate_position_seq');
    ELSE
        IF (NEW.product_id, NEW.environment, NEW.identity_hash, NEW.principal, NEW.workspace_id, NEW.position)
            IS DISTINCT FROM
           (OLD.product_id, OLD.environment, OLD.identity_hash, OLD.principal, OLD.workspace_id, OLD.position) THEN
            RAISE EXCEPTION 'immutable candidate identity and position' USING ERRCODE = '23514';
        END IF;
    END IF;
    NEW.revision := nextval('heimdall_managed.revision_seq');
    RETURN NEW;
END;
$$;
CREATE TRIGGER candidate_revision BEFORE INSERT OR UPDATE ON heimdall_managed.membership_candidates
    FOR EACH ROW EXECUTE FUNCTION heimdall_managed.candidate_revision();
REVOKE ALL ON FUNCTION heimdall_managed.workspace_revision() FROM PUBLIC;
REVOKE ALL ON FUNCTION heimdall_managed.immutable_intent() FROM PUBLIC;
REVOKE ALL ON FUNCTION heimdall_managed.candidate_revision() FROM PUBLIC;
