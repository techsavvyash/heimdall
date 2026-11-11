-- Prepared capacity storage only. Requires the v2 decision tables and drained
-- writers. Does not publish a new runtime protocol or account existing history.
CREATE SCHEMA heimdall_capacity;
REVOKE ALL ON SCHEMA heimdall_capacity FROM PUBLIC;

CREATE TABLE heimdall_capacity.policy (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    state text NOT NULL DEFAULT 'preparing' CHECK (state IN ('preparing','ready')),
    event_limit bigint NOT NULL CHECK (event_limit BETWEEN 1 AND 9007199254740991),
    byte_limit bigint NOT NULL CHECK (byte_limit BETWEEN 1 AND 9007199254740991),
    delivery_limit bigint NOT NULL CHECK (delivery_limit BETWEEN 1 AND 9007199254740991),
    used_events bigint NOT NULL DEFAULT 0 CHECK (used_events BETWEEN 0 AND event_limit),
    used_bytes bigint NOT NULL DEFAULT 0 CHECK (used_bytes BETWEEN 0 AND byte_limit),
    used_deliveries bigint NOT NULL DEFAULT 0 CHECK (used_deliveries BETWEEN 0 AND delivery_limit),
    reserved_events bigint NOT NULL DEFAULT 0 CHECK (reserved_events BETWEEN 0 AND event_limit-used_events),
    reserved_bytes bigint NOT NULL DEFAULT 0 CHECK (reserved_bytes BETWEEN 0 AND byte_limit-used_bytes),
    reserved_deliveries bigint NOT NULL DEFAULT 0 CHECK (reserved_deliveries BETWEEN 0 AND delivery_limit-used_deliveries),
    CHECK (reserved_events%2=0 AND reserved_bytes%3000000=0 AND reserved_deliveries%64=0),
    CHECK (reserved_events/2=reserved_bytes/3000000 AND reserved_events/2=reserved_deliveries/64),
    CHECK (used_events>=reserved_events/2 AND used_bytes>=used_events),
    CHECK (used_bytes/1500000+CASE WHEN used_bytes%1500000=0 THEN 0 ELSE 1 END<=used_events),
    CHECK (used_deliveries/32+CASE WHEN used_deliveries%32=0 THEN 0 ELSE 1 END<=used_events)
);

CREATE TABLE heimdall_capacity.event_charges (
    product_id text NOT NULL,
    environment text NOT NULL,
    event_id text NOT NULL,
    envelope_bytes bigint NOT NULL DEFAULT 0 CHECK (envelope_bytes BETWEEN 1 AND 1500000),
    delivery_rows integer NOT NULL DEFAULT 0 CHECK (delivery_rows BETWEEN 0 AND 32),
    PRIMARY KEY (product_id,environment,event_id),
    FOREIGN KEY (product_id,environment,event_id)
      REFERENCES heimdall.audit_events(product_id,environment,event_id) ON DELETE RESTRICT
);
CREATE TABLE heimdall_capacity.reservations (
    product_id text NOT NULL,
    environment text NOT NULL,
    decision_id text NOT NULL,
    request_event_id text NOT NULL,
    resolution text NOT NULL DEFAULT 'pending' CHECK (resolution IN ('pending','completed','recovered')),
    terminal_event_id text,
    recovery_event_id text,
    PRIMARY KEY (product_id,environment,decision_id),
    UNIQUE (product_id,environment,request_event_id),
    UNIQUE (product_id,environment,terminal_event_id),
    UNIQUE (product_id,environment,recovery_event_id),
    CHECK ((resolution='pending' AND terminal_event_id IS NULL AND recovery_event_id IS NULL)
       OR (resolution='completed' AND terminal_event_id IS NOT NULL AND recovery_event_id IS NULL)
       OR (resolution='recovered' AND terminal_event_id IS NOT NULL AND recovery_event_id IS NOT NULL)),
    CHECK (terminal_event_id IS NULL OR terminal_event_id<>request_event_id),
    CHECK (recovery_event_id IS NULL OR (recovery_event_id<>request_event_id AND recovery_event_id<>terminal_event_id)),
    FOREIGN KEY (product_id,environment,decision_id)
      REFERENCES heimdall.audit_decisions(product_id,environment,decision_id) ON DELETE RESTRICT,
    FOREIGN KEY (product_id,environment,request_event_id)
      REFERENCES heimdall_capacity.event_charges(product_id,environment,event_id) ON DELETE RESTRICT,
    FOREIGN KEY (product_id,environment,terminal_event_id)
      REFERENCES heimdall_capacity.event_charges(product_id,environment,event_id) DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY (product_id,environment,recovery_event_id)
      REFERENCES heimdall_capacity.event_charges(product_id,environment,event_id) DEFERRABLE INITIALLY DEFERRED
);

CREATE FUNCTION heimdall_capacity.prepare_event_charge() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
    IF TG_OP='UPDATE' THEN
        IF NEW IS DISTINCT FROM OLD THEN
            RAISE EXCEPTION 'immutable event charge' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;
    PERFORM singleton FROM heimdall_capacity.policy FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'missing capacity policy' USING ERRCODE='23514'; END IF;
    SELECT octet_length(event::text) INTO STRICT NEW.envelope_bytes
      FROM heimdall.audit_events WHERE product_id=NEW.product_id AND environment=NEW.environment AND event_id=NEW.event_id;
    SELECT count(*) INTO NEW.delivery_rows FROM heimdall.audit_deliveries
      WHERE product_id=NEW.product_id AND environment=NEW.environment AND event_id=NEW.event_id;
    RETURN NEW;
END;
$$;
CREATE TRIGGER prepare_event_charge BEFORE INSERT OR UPDATE ON heimdall_capacity.event_charges
  FOR EACH ROW EXECUTE FUNCTION heimdall_capacity.prepare_event_charge();

CREATE FUNCTION heimdall_capacity.account_event_charge() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
    IF TG_OP='INSERT' THEN
        UPDATE heimdall_capacity.policy SET used_events=used_events+1,
          used_bytes=used_bytes+NEW.envelope_bytes,used_deliveries=used_deliveries+NEW.delivery_rows WHERE singleton;
    ELSE
        UPDATE heimdall_capacity.policy SET used_events=used_events-1,
          used_bytes=used_bytes-OLD.envelope_bytes,used_deliveries=used_deliveries-OLD.delivery_rows WHERE singleton;
    END IF;
    IF NOT FOUND THEN RAISE EXCEPTION 'missing capacity policy' USING ERRCODE='23514'; END IF;
    RETURN NULL;
END;
$$;
CREATE TRIGGER account_event_charge AFTER INSERT OR DELETE ON heimdall_capacity.event_charges
  FOR EACH ROW EXECUTE FUNCTION heimdall_capacity.account_event_charge();

CREATE FUNCTION heimdall_capacity.guard_reservation() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE slot record;
BEGIN
    IF TG_OP='DELETE' THEN
        IF OLD.resolution='pending' THEN RAISE EXCEPTION 'unresolved reservation' USING ERRCODE='23514'; END IF;
        RETURN OLD;
    END IF;
    SELECT request_event_id,resolution,terminal_event_id INTO STRICT slot FROM heimdall.audit_decisions
      WHERE product_id=NEW.product_id AND environment=NEW.environment AND decision_id=NEW.decision_id;
    IF NEW.request_event_id IS DISTINCT FROM slot.request_event_id OR NEW.resolution IS DISTINCT FROM slot.resolution
       OR NEW.terminal_event_id IS DISTINCT FROM slot.terminal_event_id THEN
        RAISE EXCEPTION 'reservation decision binding' USING ERRCODE='23514';
    END IF;
    IF TG_OP='INSERT' THEN
        IF NEW.resolution<>'pending' THEN RAISE EXCEPTION 'reservation must start pending' USING ERRCODE='23514'; END IF;
    ELSE
        IF (NEW.product_id,NEW.environment,NEW.decision_id,NEW.request_event_id)
          IS DISTINCT FROM (OLD.product_id,OLD.environment,OLD.decision_id,OLD.request_event_id)
          OR (OLD.resolution<>'pending' AND NEW IS DISTINCT FROM OLD) THEN
            RAISE EXCEPTION 'immutable reservation binding or resolution' USING ERRCODE='23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER guard_reservation BEFORE INSERT OR UPDATE OR DELETE ON heimdall_capacity.reservations
  FOR EACH ROW EXECUTE FUNCTION heimdall_capacity.guard_reservation();

CREATE FUNCTION heimdall_capacity.account_reservation() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
    IF TG_OP='INSERT' THEN
        UPDATE heimdall_capacity.policy SET reserved_events=reserved_events+2,
          reserved_bytes=reserved_bytes+3000000,reserved_deliveries=reserved_deliveries+64 WHERE singleton;
    ELSIF OLD.resolution='pending' AND NEW.resolution<>'pending' THEN
        UPDATE heimdall_capacity.policy SET reserved_events=reserved_events-2,
          reserved_bytes=reserved_bytes-3000000,reserved_deliveries=reserved_deliveries-64 WHERE singleton;
    ELSE RETURN NULL;
    END IF;
    IF NOT FOUND THEN RAISE EXCEPTION 'missing capacity policy' USING ERRCODE='23514'; END IF;
    RETURN NULL;
END;
$$;
CREATE TRIGGER account_reservation AFTER INSERT OR UPDATE ON heimdall_capacity.reservations
  FOR EACH ROW EXECUTE FUNCTION heimdall_capacity.account_reservation();

CREATE FUNCTION heimdall_capacity.guard_charged_delivery() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
    IF TG_OP='UPDATE' THEN
        IF (NEW.product_id,NEW.environment,NEW.event_id,NEW.sink_id)
          IS DISTINCT FROM (OLD.product_id,OLD.environment,OLD.event_id,OLD.sink_id) THEN
            RAISE EXCEPTION 'immutable delivery identity' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;
    PERFORM singleton FROM heimdall_capacity.policy FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'missing capacity policy' USING ERRCODE='23514'; END IF;
    IF EXISTS(SELECT 1 FROM heimdall_capacity.event_charges
      WHERE product_id=NEW.product_id AND environment=NEW.environment AND event_id=NEW.event_id) THEN
        RAISE EXCEPTION 'cannot add a sink after charging an event' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER guard_charged_delivery BEFORE INSERT OR UPDATE ON heimdall.audit_deliveries
  FOR EACH ROW EXECUTE FUNCTION heimdall_capacity.guard_charged_delivery();
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA heimdall_capacity FROM PUBLIC;

CREATE FUNCTION heimdall_capacity.guard_policy() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
    IF TG_OP='DELETE' THEN RAISE EXCEPTION 'capacity policy cannot be deleted' USING ERRCODE='23514'; END IF;
    IF TG_OP='INSERT' THEN
        IF NEW.state<>'preparing' OR NEW.used_events<>0 OR NEW.used_bytes<>0 OR NEW.used_deliveries<>0
          OR NEW.reserved_events<>0 OR NEW.reserved_bytes<>0 OR NEW.reserved_deliveries<>0
          OR EXISTS(SELECT 1 FROM heimdall_capacity.event_charges)
          OR EXISTS(SELECT 1 FROM heimdall_capacity.reservations) THEN
            RAISE EXCEPTION 'capacity policy must start empty and preparing' USING ERRCODE='23514';
        END IF;
    ELSIF pg_trigger_depth()<2 AND
      (NEW.used_events,NEW.used_bytes,NEW.used_deliveries,NEW.reserved_events,NEW.reserved_bytes,NEW.reserved_deliveries)
      IS DISTINCT FROM (OLD.used_events,OLD.used_bytes,OLD.used_deliveries,OLD.reserved_events,OLD.reserved_bytes,OLD.reserved_deliveries) THEN
        RAISE EXCEPTION 'capacity counters derive from charges and reservations' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
REVOKE ALL ON FUNCTION heimdall_capacity.guard_policy() FROM PUBLIC;
CREATE TRIGGER guard_policy BEFORE INSERT OR UPDATE OR DELETE ON heimdall_capacity.policy
  FOR EACH ROW EXECUTE FUNCTION heimdall_capacity.guard_policy();
