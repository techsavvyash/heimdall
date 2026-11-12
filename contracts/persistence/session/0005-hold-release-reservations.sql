-- Prepared physical extension. Apply in a transaction with writers drained.
-- The deployment command must verify source history and publish readiness separately.
SELECT pg_catalog.pg_advisory_xact_lock(74162401);
LOCK TABLE heimdall_capacity.policy, heimdall_retention.capacity,
    heimdall_retention.event_holds IN SHARE ROW EXCLUSIVE MODE;

CREATE TABLE heimdall_retention.hold_release_reservations (
    product_id text NOT NULL,
    environment text NOT NULL,
    hold_id text NOT NULL,
    release_event_id text,
    PRIMARY KEY (product_id, environment, hold_id),
    FOREIGN KEY (product_id, environment, hold_id)
        REFERENCES heimdall_retention.event_holds(product_id, environment, hold_id),
    FOREIGN KEY (product_id, environment, release_event_id)
        REFERENCES heimdall_retention.event_identities(product_id, environment, event_id)
        DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE heimdall_retention.hold_release_capacity (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    active_count bigint NOT NULL CHECK (active_count >= 0)
);

INSERT INTO heimdall_retention.hold_release_reservations
    (product_id, environment, hold_id, release_event_id)
SELECT product_id, environment, hold_id, release_event_id
FROM heimdall_retention.event_holds;

INSERT INTO heimdall_retention.hold_release_capacity(active_count)
SELECT count(*) FROM heimdall_retention.hold_release_reservations WHERE release_event_id IS NULL;

-- Division keeps admission safe at the largest supported quota.
CREATE FUNCTION heimdall_retention.check_hold_release_headroom(
    active_count bigint, payload heimdall_capacity.policy, metadata heimdall_retention.capacity
) RETURNS void
LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF payload.singleton IS DISTINCT FROM true OR metadata.singleton IS DISTINCT FROM true
       OR active_count > payload.event_limit - payload.used_events - payload.reserved_events
       OR active_count > (payload.byte_limit - payload.used_bytes - payload.reserved_bytes) / 1500000
       OR active_count > (payload.delivery_limit - payload.used_deliveries - payload.reserved_deliveries) / 32
       OR active_count > metadata.row_limit - metadata.used_rows - metadata.reserved_rows
       OR active_count > metadata.byte_limit / 8192 - metadata.used_rows - metadata.reserved_rows THEN
        RAISE EXCEPTION 'insufficient audit hold release capacity' USING ERRCODE = '23514';
    END IF;
END;
$$;

DO $$
BEGIN
    PERFORM heimdall_retention.check_hold_release_headroom(
        (SELECT active_count FROM heimdall_retention.hold_release_capacity),
        (SELECT p FROM heimdall_capacity.policy p WHERE singleton),
        (SELECT c FROM heimdall_retention.capacity c WHERE singleton)
    );
    IF EXISTS (
        SELECT 1 FROM heimdall_retention.event_holds h
        JOIN heimdall_retention.hold_release_reservations r USING(product_id, environment, hold_id)
        WHERE octet_length(to_jsonb(h)::text) + octet_length(to_jsonb(r)::text) > 8192
    ) THEN
        RAISE EXCEPTION 'hold reservation exceeds metadata slot' USING ERRCODE = '23514';
    END IF;
END;
$$;

CREATE FUNCTION heimdall_retention.guard_hold_release_capacity() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE active bigint; payload heimdall_capacity.policy; metadata heimdall_retention.capacity;
BEGIN
    SELECT active_count INTO STRICT active FROM heimdall_retention.hold_release_capacity;
    IF TG_TABLE_SCHEMA = 'heimdall_capacity' THEN
        payload := NEW;
        SELECT * INTO STRICT metadata FROM heimdall_retention.capacity;
    ELSE
        metadata := NEW;
        SELECT * INTO STRICT payload FROM heimdall_capacity.policy;
    END IF;
    PERFORM heimdall_retention.check_hold_release_headroom(active, payload, metadata);
    RETURN NEW;
END;
$$;

CREATE TRIGGER guard_hold_release_capacity AFTER INSERT OR UPDATE ON heimdall_capacity.policy
    FOR EACH ROW EXECUTE FUNCTION heimdall_retention.guard_hold_release_capacity();
CREATE TRIGGER guard_hold_release_capacity AFTER INSERT OR UPDATE ON heimdall_retention.capacity
    FOR EACH ROW EXECUTE FUNCTION heimdall_retention.guard_hold_release_capacity();

CREATE FUNCTION heimdall_retention.account_hold_release_reservation() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE active bigint; payload heimdall_capacity.policy; metadata heimdall_retention.capacity;
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'immutable hold release reservation' USING ERRCODE = '23514';
    ELSIF TG_OP = 'UPDATE' THEN
        IF (NEW.product_id, NEW.environment, NEW.hold_id)
           IS DISTINCT FROM (OLD.product_id, OLD.environment, OLD.hold_id)
           OR (OLD.release_event_id IS NOT NULL AND NEW.release_event_id IS DISTINCT FROM OLD.release_event_id) THEN
            RAISE EXCEPTION 'immutable hold release reservation' USING ERRCODE = '23514';
        END IF;
        IF NEW IS NOT DISTINCT FROM OLD THEN RETURN NEW; END IF;
    ELSIF NEW.release_event_id IS NOT NULL THEN
        RAISE EXCEPTION 'new hold reservation must be active' USING ERRCODE = '23514';
    END IF;

    SELECT * INTO STRICT payload FROM heimdall_capacity.policy WHERE singleton FOR UPDATE;
    SELECT * INTO STRICT metadata FROM heimdall_retention.capacity WHERE singleton FOR UPDATE;
    UPDATE heimdall_retention.hold_release_capacity
        SET active_count = active_count + CASE WHEN TG_OP = 'INSERT' THEN 1 ELSE -1 END
        WHERE singleton RETURNING active_count INTO STRICT active;
    PERFORM heimdall_retention.check_hold_release_headroom(active, payload, metadata);
    RETURN NEW;
END;
$$;

CREATE TRIGGER account_hold_release_reservation
    BEFORE INSERT OR UPDATE OR DELETE ON heimdall_retention.hold_release_reservations
    FOR EACH ROW EXECUTE FUNCTION heimdall_retention.account_hold_release_reservation();

CREATE FUNCTION heimdall_retention.reserve_hold_release() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    INSERT INTO heimdall_retention.hold_release_reservations(product_id, environment, hold_id)
        VALUES(NEW.product_id, NEW.environment, NEW.hold_id);
    RETURN NEW;
END;
$$;
CREATE TRIGGER reserve_hold_release AFTER INSERT ON heimdall_retention.event_holds
    FOR EACH ROW EXECUTE FUNCTION heimdall_retention.reserve_hold_release();

CREATE FUNCTION heimdall_retention.verify_hold_release_reservation() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM heimdall_retention.event_holds h
        JOIN heimdall_retention.hold_release_reservations r USING(product_id, environment, hold_id)
        WHERE h.product_id = NEW.product_id AND h.environment = NEW.environment AND h.hold_id = NEW.hold_id
          AND h.release_event_id IS NOT DISTINCT FROM r.release_event_id
          AND octet_length(to_jsonb(h)::text) + octet_length(to_jsonb(r)::text) <= 8192
    ) THEN
        RAISE EXCEPTION 'hold release reservation does not match hold' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE CONSTRAINT TRIGGER verify_hold_release_reservation
    AFTER INSERT OR UPDATE ON heimdall_retention.event_holds
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION heimdall_retention.verify_hold_release_reservation();
CREATE CONSTRAINT TRIGGER verify_hold_release_reservation
    AFTER INSERT OR UPDATE ON heimdall_retention.hold_release_reservations
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION heimdall_retention.verify_hold_release_reservation();

REVOKE ALL ON TABLE heimdall_retention.hold_release_reservations, heimdall_retention.hold_release_capacity FROM PUBLIC;
REVOKE ALL ON FUNCTION heimdall_retention.check_hold_release_headroom(bigint, heimdall_capacity.policy, heimdall_retention.capacity),
    heimdall_retention.guard_hold_release_capacity(), heimdall_retention.account_hold_release_reservation(),
    heimdall_retention.reserve_hold_release(), heimdall_retention.verify_hold_release_reservation() FROM PUBLIC;
