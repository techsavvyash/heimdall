-- Prepared immutable retention identity storage only. Requires audit/capacity v3
-- and drained writers. Does not enable retirement, register v4 or backfill rows.
CREATE SCHEMA heimdall_retention;
REVOKE ALL ON SCHEMA heimdall_retention FROM PUBLIC;

CREATE TABLE heimdall_retention.event_identities (
    product_id text NOT NULL CHECK (product_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    environment text NOT NULL CHECK (environment ~ '^[A-Za-z0-9_-]{1,64}$'),
    event_id text NOT NULL CHECK (event_id ~ '^[A-Za-z0-9_-]{1,128}$'),
    tenant_id text CHECK (tenant_id ~ '^[A-Za-z0-9_-]{1,128}$'),
    recorded_at bigint NOT NULL CHECK (recorded_at BETWEEN 0 AND 9007199254740991),
    payload_digest bytea NOT NULL CHECK (octet_length(payload_digest)=32),
    kind text NOT NULL CHECK (kind IN ('ordinary','request','terminal','recovery')),
    decision_id text CHECK (decision_id ~ '^[A-Za-z0-9_-]{1,128}$'),
    routing jsonb NOT NULL CHECK (jsonb_typeof(routing)='array' AND jsonb_array_length(routing)<=32),
    CHECK ((kind='ordinary')=(decision_id IS NULL)),
    PRIMARY KEY(product_id,environment,event_id)
);
CREATE UNIQUE INDEX retained_decision_member ON heimdall_retention.event_identities
    (product_id,environment,decision_id,kind) WHERE decision_id IS NOT NULL;

CREATE TABLE heimdall_retention.decision_identities (
    product_id text NOT NULL,
    environment text NOT NULL,
    decision_id text NOT NULL CHECK (decision_id ~ '^[A-Za-z0-9_-]{1,128}$'),
    tenant_id text CHECK (tenant_id ~ '^[A-Za-z0-9_-]{1,128}$'),
    request_event_id text NOT NULL,
    requested_at bigint NOT NULL CHECK (requested_at BETWEEN 0 AND 9007199254740991),
    resolution text NOT NULL CHECK (resolution IN ('pending','completed','recovered')),
    terminal_event_id text,
    recovery_event_id text,
    resolved_at bigint CHECK (resolved_at BETWEEN requested_at AND 9007199254740991),
    CHECK ((resolution='pending' AND terminal_event_id IS NULL AND recovery_event_id IS NULL AND resolved_at IS NULL)
        OR (resolution='completed' AND terminal_event_id IS NOT NULL AND recovery_event_id IS NULL AND resolved_at IS NOT NULL)
        OR (resolution='recovered' AND terminal_event_id IS NOT NULL AND recovery_event_id IS NOT NULL AND resolved_at IS NOT NULL)),
    CHECK (terminal_event_id IS NULL OR terminal_event_id<>request_event_id),
    CHECK (recovery_event_id IS NULL OR (recovery_event_id<>request_event_id AND recovery_event_id<>terminal_event_id)),
    PRIMARY KEY(product_id,environment,decision_id),
    UNIQUE(product_id,environment,request_event_id),
    UNIQUE(product_id,environment,terminal_event_id),
    UNIQUE(product_id,environment,recovery_event_id),
    FOREIGN KEY(product_id,environment,request_event_id)
      REFERENCES heimdall_retention.event_identities(product_id,environment,event_id) ON DELETE RESTRICT,
    FOREIGN KEY(product_id,environment,terminal_event_id)
      REFERENCES heimdall_retention.event_identities(product_id,environment,event_id) ON DELETE RESTRICT,
    FOREIGN KEY(product_id,environment,recovery_event_id)
      REFERENCES heimdall_retention.event_identities(product_id,environment,event_id) ON DELETE RESTRICT
);

CREATE FUNCTION heimdall_retention.guard_event_identity() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE source record; expected_kind text; expected_decision text; expected_routing jsonb; matches bigint;
BEGIN
    IF TG_OP='DELETE' THEN RAISE EXCEPTION 'retained identity cannot be deleted' USING ERRCODE='23514'; END IF;
    IF TG_OP='UPDATE' THEN
        IF NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION 'immutable event identity' USING ERRCODE='23514'; END IF;
        RETURN NEW;
    END IF;
    SELECT e.recorded_at,e.payload_digest,c.delivery_rows INTO source FROM heimdall.audit_events e
      JOIN heimdall_capacity.event_charges c USING(product_id,environment,event_id)
      WHERE e.product_id=NEW.product_id AND e.environment=NEW.environment AND e.event_id=NEW.event_id;
    IF NOT FOUND OR NEW.recorded_at IS DISTINCT FROM source.recorded_at OR NEW.payload_digest IS DISTINCT FROM source.payload_digest THEN
        RAISE EXCEPTION 'identity source mismatch' USING ERRCODE='23514';
    END IF;
    SELECT to_jsonb(ARRAY(SELECT sink_id FROM heimdall.audit_deliveries
      WHERE product_id=NEW.product_id AND environment=NEW.environment AND event_id=NEW.event_id
      ORDER BY sink_id COLLATE "C")) INTO expected_routing;
    IF NEW.routing IS DISTINCT FROM expected_routing OR jsonb_array_length(expected_routing)<>source.delivery_rows THEN
        RAISE EXCEPTION 'identity routing mismatch' USING ERRCODE='23514';
    END IF;
    SELECT count(*),min(kind),min(decision_id) INTO matches,expected_kind,expected_decision FROM (
      SELECT 'request' AS kind,d.decision_id FROM heimdall.audit_decisions d
        WHERE d.product_id=NEW.product_id AND d.environment=NEW.environment AND d.request_event_id=NEW.event_id
      UNION ALL SELECT 'terminal',d.decision_id FROM heimdall.audit_decisions d
        WHERE d.product_id=NEW.product_id AND d.environment=NEW.environment AND d.terminal_event_id=NEW.event_id
      UNION ALL SELECT 'recovery',r.decision_id FROM heimdall_capacity.reservations r
        WHERE r.product_id=NEW.product_id AND r.environment=NEW.environment AND r.recovery_event_id=NEW.event_id
    ) bindings;
    IF matches>1 THEN RAISE EXCEPTION 'ambiguous decision identity' USING ERRCODE='23514'; END IF;
    IF matches=0 THEN expected_kind:='ordinary'; expected_decision:=NULL; END IF;
    IF NEW.kind IS DISTINCT FROM expected_kind OR NEW.decision_id IS DISTINCT FROM expected_decision THEN
        RAISE EXCEPTION 'identity decision mismatch' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
REVOKE ALL ON FUNCTION heimdall_retention.guard_event_identity() FROM PUBLIC;
CREATE TRIGGER guard_event_identity BEFORE INSERT OR UPDATE OR DELETE ON heimdall_retention.event_identities
    FOR EACH ROW EXECUTE FUNCTION heimdall_retention.guard_event_identity();

CREATE FUNCTION heimdall_retention.guard_decision_identity() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE source record; member record;
BEGIN
    IF TG_OP='DELETE' THEN RAISE EXCEPTION 'retained decision cannot be deleted' USING ERRCODE='23514'; END IF;
    IF TG_OP='UPDATE' THEN
        IF (NEW.product_id,NEW.environment,NEW.decision_id,NEW.tenant_id,NEW.request_event_id,NEW.requested_at)
          IS DISTINCT FROM (OLD.product_id,OLD.environment,OLD.decision_id,OLD.tenant_id,OLD.request_event_id,OLD.requested_at) THEN
            RAISE EXCEPTION 'immutable retained request' USING ERRCODE='23514';
        END IF;
        IF NEW IS NOT DISTINCT FROM OLD THEN RETURN NEW; END IF;
        IF OLD.resolution<>'pending' THEN RAISE EXCEPTION 'immutable retained resolution' USING ERRCODE='23514'; END IF;
    END IF;
    SELECT d.request_event_id,d.requested_at,d.resolution,d.terminal_event_id,d.resolved_at,r.recovery_event_id
      INTO source FROM heimdall.audit_decisions d JOIN heimdall_capacity.reservations r USING(product_id,environment,decision_id)
      WHERE d.product_id=NEW.product_id AND d.environment=NEW.environment AND d.decision_id=NEW.decision_id
      AND r.request_event_id=d.request_event_id AND r.resolution=d.resolution
      AND r.terminal_event_id IS NOT DISTINCT FROM d.terminal_event_id;
    IF NOT FOUND OR (NEW.request_event_id,NEW.requested_at,NEW.resolution,NEW.terminal_event_id,NEW.resolved_at,NEW.recovery_event_id)
      IS DISTINCT FROM (source.request_event_id,source.requested_at,source.resolution,source.terminal_event_id,source.resolved_at,source.recovery_event_id) THEN
        RAISE EXCEPTION 'retained decision source mismatch' USING ERRCODE='23514';
    END IF;
    FOR member IN SELECT event_id,kind,decision_id,tenant_id,recorded_at FROM heimdall_retention.event_identities
      WHERE product_id=NEW.product_id AND environment=NEW.environment
      AND event_id IN (NEW.request_event_id,NEW.terminal_event_id,NEW.recovery_event_id)
    LOOP
        IF member.decision_id IS DISTINCT FROM NEW.decision_id OR member.tenant_id IS DISTINCT FROM NEW.tenant_id
          OR (member.event_id=NEW.request_event_id AND (member.kind<>'request' OR member.recorded_at<>NEW.requested_at))
          OR (member.event_id=NEW.terminal_event_id AND (member.kind<>'terminal' OR member.recorded_at<>NEW.resolved_at))
          OR (member.event_id=NEW.recovery_event_id AND (member.kind<>'recovery' OR member.recorded_at<>NEW.resolved_at)) THEN
            RAISE EXCEPTION 'retained member mismatch' USING ERRCODE='23514';
        END IF;
    END LOOP;
    RETURN NEW;
END;
$$;
REVOKE ALL ON FUNCTION heimdall_retention.guard_decision_identity() FROM PUBLIC;
CREATE TRIGGER guard_decision_identity BEFORE INSERT OR UPDATE OR DELETE ON heimdall_retention.decision_identities
    FOR EACH ROW EXECUTE FUNCTION heimdall_retention.guard_decision_identity();
