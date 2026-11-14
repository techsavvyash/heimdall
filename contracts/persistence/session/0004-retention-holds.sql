-- Prepared hold ledger only. Requires the prepared retention identity tables.
-- Does not grant runtime privileges, publish v4, or authorize a hold operation.
CREATE TABLE heimdall_retention.event_holds (
    product_id text NOT NULL,
    environment text NOT NULL,
    hold_id text NOT NULL CHECK (hold_id ~ '^[A-Za-z0-9_-]{1,128}$'),
    event_id text NOT NULL,
    tenant_id text CHECK (tenant_id ~ '^[A-Za-z0-9_-]{1,128}$'),
    placed_at bigint NOT NULL CHECK (placed_at BETWEEN 0 AND 9007199254740991),
    placement_event_id text NOT NULL,
    released_at bigint CHECK (released_at BETWEEN placed_at AND 9007199254740991),
    release_event_id text,
    CHECK ((released_at IS NULL)=(release_event_id IS NULL)),
    CHECK (event_id<>placement_event_id),
    CHECK (release_event_id IS NULL OR (release_event_id<>event_id AND release_event_id<>placement_event_id)),
    PRIMARY KEY(product_id,environment,hold_id),
    FOREIGN KEY(product_id,environment,event_id) REFERENCES heimdall_retention.event_identities(product_id,environment,event_id),
    FOREIGN KEY(product_id,environment,placement_event_id) REFERENCES heimdall_retention.event_identities(product_id,environment,event_id),
    FOREIGN KEY(product_id,environment,release_event_id) REFERENCES heimdall_retention.event_identities(product_id,environment,event_id)
);
CREATE INDEX active_event_holds ON heimdall_retention.event_holds(product_id,environment,event_id) WHERE released_at IS NULL;
CREATE FUNCTION heimdall_retention.guard_event_hold() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE target record; reference_id text; reference_at bigint;
BEGIN
    IF TG_OP='DELETE' THEN RAISE EXCEPTION 'hold history cannot be deleted' USING ERRCODE='23514'; END IF;
    IF TG_OP='UPDATE' THEN
        IF (NEW.product_id,NEW.environment,NEW.hold_id,NEW.event_id,NEW.tenant_id,NEW.placed_at,NEW.placement_event_id)
          IS DISTINCT FROM (OLD.product_id,OLD.environment,OLD.hold_id,OLD.event_id,OLD.tenant_id,OLD.placed_at,OLD.placement_event_id) THEN
            RAISE EXCEPTION 'immutable hold placement' USING ERRCODE='23514';
        END IF;
        IF NEW IS NOT DISTINCT FROM OLD THEN RETURN NEW; END IF;
        IF OLD.released_at IS NOT NULL OR NEW.released_at IS NULL THEN
            RAISE EXCEPTION 'immutable hold release' USING ERRCODE='23514';
        END IF;
        reference_id:=NEW.release_event_id; reference_at:=NEW.released_at;
    ELSE
        IF NEW.released_at IS NOT NULL OR NEW.release_event_id IS NOT NULL THEN
            RAISE EXCEPTION 'new hold must be active' USING ERRCODE='23514';
        END IF;
        reference_id:=NEW.placement_event_id; reference_at:=NEW.placed_at;
    END IF;
    -- Future retirement must acquire this same identity lock before checking all
    -- member holds. A hold cannot be placed on an already absent payload.
    SELECT tenant_id,recorded_at INTO target FROM heimdall_retention.event_identities
      WHERE product_id=NEW.product_id AND environment=NEW.environment AND event_id=NEW.event_id FOR UPDATE;
    IF NOT FOUND OR target.tenant_id IS DISTINCT FROM NEW.tenant_id OR NEW.placed_at<target.recorded_at THEN
        RAISE EXCEPTION 'hold target mismatch' USING ERRCODE='23514';
    END IF;
    IF NOT EXISTS(SELECT 1 FROM heimdall.audit_events WHERE product_id=NEW.product_id AND environment=NEW.environment AND event_id=NEW.event_id) THEN
        RAISE EXCEPTION 'hold target payload missing' USING ERRCODE='23514';
    END IF;
    IF NOT EXISTS(SELECT 1 FROM heimdall_retention.event_identities i JOIN heimdall.audit_events e USING(product_id,environment,event_id)
      WHERE i.product_id=NEW.product_id AND i.environment=NEW.environment AND i.event_id=reference_id
      AND i.tenant_id IS NOT DISTINCT FROM NEW.tenant_id AND i.recorded_at=reference_at
      AND e.recorded_at=i.recorded_at AND e.payload_digest=i.payload_digest) THEN
        RAISE EXCEPTION 'hold audit reference mismatch' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
REVOKE ALL ON FUNCTION heimdall_retention.guard_event_hold() FROM PUBLIC;
CREATE TRIGGER guard_event_hold BEFORE INSERT OR UPDATE OR DELETE ON heimdall_retention.event_holds
    FOR EACH ROW EXECUTE FUNCTION heimdall_retention.guard_event_hold();
