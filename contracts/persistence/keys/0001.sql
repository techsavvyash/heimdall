-- Shared PostgreSQL foundation. Execute once in a deployment-owned transaction.
CREATE SCHEMA heimdall_keys;
REVOKE ALL ON SCHEMA heimdall_keys FROM PUBLIC;

CREATE TABLE heimdall_keys.schema_metadata (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    database_version integer NOT NULL CHECK (database_version BETWEEN 1 AND 1000),
    state text NOT NULL CHECK (state IN ('ready', 'migrating')),
    reader_min integer NOT NULL CHECK (reader_min BETWEEN 1 AND 1000),
    reader_max integer NOT NULL CHECK (reader_max BETWEEN reader_min AND 1000),
    writer_min integer NOT NULL CHECK (writer_min BETWEEN 1 AND 1000),
    writer_max integer NOT NULL CHECK (writer_max BETWEEN writer_min AND 1000)
);
CREATE TABLE heimdall_keys.migrations (
    version integer PRIMARY KEY CHECK (version BETWEEN 1 AND 1000),
    checksum text NOT NULL CHECK (checksum ~ '^[a-f0-9]{64}$'),
    applied_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

-- One row per actual AES key across products, environments and key aliases.
-- Runtime gets SELECT and UPDATE(used), never INSERT, DELETE or policy changes.
CREATE TABLE heimdall_keys.record_key_usage (
    fingerprint bytea PRIMARY KEY CHECK (octet_length(fingerprint)=32),
    encryption_limit bigint NOT NULL CHECK (encryption_limit BETWEEN 1 AND 4294967295),
    used bigint NOT NULL DEFAULT 0 CHECK (used BETWEEN 0 AND encryption_limit),
    state text NOT NULL DEFAULT 'active' CHECK (state IN ('active','retired'))
);
CREATE FUNCTION heimdall_keys.guard_record_key_usage() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
    IF TG_OP='DELETE' THEN
        RAISE EXCEPTION 'key usage history cannot be deleted' USING ERRCODE='23514';
    END IF;
    IF TG_OP='INSERT' THEN
        IF NEW.used<>0 OR NEW.state<>'active' THEN
            RAISE EXCEPTION 'new key starts unused and active' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.fingerprint IS DISTINCT FROM OLD.fingerprint
       OR NEW.encryption_limit IS DISTINCT FROM OLD.encryption_limit
       OR NEW.used<OLD.used
       OR (OLD.state='retired' AND NEW IS DISTINCT FROM OLD) THEN
        RAISE EXCEPTION 'immutable key identity policy or consumed usage' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
REVOKE ALL ON FUNCTION heimdall_keys.guard_record_key_usage() FROM PUBLIC;
CREATE TRIGGER record_key_usage_guard BEFORE INSERT OR UPDATE OR DELETE ON heimdall_keys.record_key_usage
    FOR EACH ROW EXECUTE FUNCTION heimdall_keys.guard_record_key_usage();
