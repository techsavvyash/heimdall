-- Offline capacity upgrade receipt and bounded backfill checkpoint. Install only
-- after the prepared capacity schema, with all writers stopped. No activation.
CREATE TABLE heimdall_capacity.upgrade (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    capacity_checksum text NOT NULL CHECK (capacity_checksum ~ '^[a-f0-9]{64}$'),
    preparation_checksum text NOT NULL CHECK (preparation_checksum ~ '^[a-f0-9]{64}$'),
    event_limit bigint NOT NULL CHECK (event_limit BETWEEN 1 AND 9007199254740991),
    byte_limit bigint NOT NULL CHECK (byte_limit BETWEEN 1 AND 9007199254740991),
    delivery_limit bigint NOT NULL CHECK (delivery_limit BETWEEN 1 AND 9007199254740991),
    prepared_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE heimdall_capacity.backfill (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    phase text NOT NULL DEFAULT 'events' CHECK (phase IN ('events','decisions','complete')),
    after_product text,
    after_environment text,
    after_id text,
    scanned_events bigint NOT NULL DEFAULT 0 CHECK (scanned_events BETWEEN 0 AND 9007199254740991),
    scanned_decisions bigint NOT NULL DEFAULT 0 CHECK (scanned_decisions BETWEEN 0 AND 9007199254740991),
    CHECK ((after_product IS NULL AND after_environment IS NULL AND after_id IS NULL) OR
      (after_product IS NOT NULL AND after_environment IS NOT NULL AND after_id IS NOT NULL
       AND length(after_product) BETWEEN 1 AND 64 AND after_product !~ '[^A-Za-z0-9_-]'
       AND length(after_environment) BETWEEN 1 AND 64 AND after_environment !~ '[^A-Za-z0-9_-]'
       AND length(after_id) BETWEEN 1 AND 128 AND after_id !~ '[^A-Za-z0-9_-]')),
    CHECK (phase<>'complete' OR after_id IS NULL)
);
REVOKE ALL ON heimdall_capacity.upgrade,heimdall_capacity.backfill FROM PUBLIC;

-- Completed history has no future reservation. Its immutable binding still needs
-- importing for retry validation; it must never be rewritten through pending.
CREATE OR REPLACE FUNCTION heimdall_capacity.guard_reservation() RETURNS trigger
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
        IF NEW.resolution<>'pending' AND NOT EXISTS(SELECT 1 FROM heimdall_capacity.policy WHERE singleton AND state='preparing') THEN
            RAISE EXCEPTION 'resolved import requires offline preparation' USING ERRCODE='23514';
        END IF;
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

CREATE OR REPLACE FUNCTION heimdall_capacity.account_reservation() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
    IF TG_OP='INSERT' THEN
        IF NEW.resolution<>'pending' THEN RETURN NULL; END IF;
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

REVOKE ALL ON FUNCTION heimdall_capacity.guard_reservation(),heimdall_capacity.account_reservation() FROM PUBLIC;
