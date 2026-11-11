-- Prepared logical metadata quota, not v4 activation. Install with writers drained.
-- Every identity or hold occupies one conservative 8192-byte logical slot.
-- Fixed allocation covers later decision resolution and hold release growth.
CREATE TABLE heimdall_retention.capacity (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    row_limit bigint NOT NULL CHECK (row_limit BETWEEN 1 AND 1099511627775),
    byte_limit bigint NOT NULL CHECK (byte_limit BETWEEN 8192 AND 9007199254740991),
    used_rows bigint NOT NULL DEFAULT 0 CHECK (used_rows>=0),
    reserved_rows bigint NOT NULL DEFAULT 0 CHECK (reserved_rows>=0),
    CHECK (used_rows+reserved_rows<=row_limit),
    CHECK ((used_rows+reserved_rows)*8192<=byte_limit)
);
CREATE FUNCTION heimdall_retention.initialize_capacity() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
    -- DDL locks and drained writers are required while adopting existing rows.
    IF EXISTS(SELECT 1 FROM heimdall_retention.event_identities i WHERE octet_length(to_jsonb(i)::text)>8192)
      OR EXISTS(SELECT 1 FROM heimdall_retention.decision_identities d WHERE octet_length(to_jsonb(d)::text)>8192)
      OR EXISTS(SELECT 1 FROM heimdall_retention.event_holds h WHERE octet_length(to_jsonb(h)::text)>8192) THEN
        RAISE EXCEPTION 'retention metadata exceeds slot size' USING ERRCODE='23514';
    END IF;
    SELECT (SELECT count(*) FROM heimdall_retention.event_identities)+(SELECT count(*) FROM heimdall_retention.decision_identities)+(SELECT count(*) FROM heimdall_retention.event_holds) INTO NEW.used_rows;
    SELECT coalesce(sum(2-(SELECT count(*) FROM heimdall_retention.event_identities i
      WHERE i.product_id=d.product_id AND i.environment=d.environment AND i.decision_id=d.decision_id AND i.kind IN ('terminal','recovery'))),0)
      INTO NEW.reserved_rows FROM heimdall_retention.decision_identities d WHERE d.resolution='pending';
    RETURN NEW;
END;
$$;
REVOKE ALL ON FUNCTION heimdall_retention.initialize_capacity() FROM PUBLIC;
CREATE TRIGGER initialize_capacity BEFORE INSERT ON heimdall_retention.capacity
    FOR EACH ROW EXECUTE FUNCTION heimdall_retention.initialize_capacity();

CREATE FUNCTION heimdall_retention.charge_metadata() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE used_delta bigint:=0; reserved_delta bigint:=0; members bigint;
BEGIN
    IF octet_length(to_jsonb(NEW)::text)>8192 THEN
        RAISE EXCEPTION 'retention metadata exceeds slot size' USING ERRCODE='23514';
    END IF;
    IF TG_OP='INSERT' THEN used_delta:=1; END IF;
    IF TG_TABLE_NAME='event_identities' THEN
      IF TG_OP='INSERT' AND NEW.kind IN ('terminal','recovery') THEN
        IF EXISTS(SELECT 1 FROM heimdall_retention.decision_identities d WHERE d.product_id=NEW.product_id
          AND d.environment=NEW.environment AND d.decision_id=NEW.decision_id AND d.resolution='pending') THEN
            reserved_delta:=-1;
        END IF;
      END IF;
    ELSIF TG_TABLE_NAME='decision_identities' THEN
        SELECT count(*) INTO members FROM heimdall_retention.event_identities i
          WHERE i.product_id=NEW.product_id AND i.environment=NEW.environment AND i.decision_id=NEW.decision_id AND i.kind IN ('terminal','recovery');
        IF TG_OP='INSERT' AND NEW.resolution='pending' THEN reserved_delta:=2-members;
        ELSIF TG_OP='UPDATE' AND OLD.resolution='pending' AND NEW.resolution<>'pending' THEN reserved_delta:=-(2-members);
        END IF;
    END IF;
    -- Updates serialize on this singleton; quota failure rolls back source rows.
    -- Exact retries and hold releases consume no new slot and refund nothing.
    UPDATE heimdall_retention.capacity SET used_rows=used_rows+used_delta,reserved_rows=reserved_rows+reserved_delta WHERE singleton;
    IF NOT FOUND THEN RAISE EXCEPTION 'retention capacity uninitialized' USING ERRCODE='23514'; END IF;
    RETURN NEW;
END;
$$;
REVOKE ALL ON FUNCTION heimdall_retention.charge_metadata() FROM PUBLIC;
CREATE TRIGGER charge_metadata AFTER INSERT OR UPDATE ON heimdall_retention.event_identities
    FOR EACH ROW EXECUTE FUNCTION heimdall_retention.charge_metadata();
CREATE TRIGGER charge_metadata AFTER INSERT OR UPDATE ON heimdall_retention.decision_identities
    FOR EACH ROW EXECUTE FUNCTION heimdall_retention.charge_metadata();
CREATE TRIGGER charge_metadata AFTER INSERT OR UPDATE ON heimdall_retention.event_holds
    FOR EACH ROW EXECUTE FUNCTION heimdall_retention.charge_metadata();
