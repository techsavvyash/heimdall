-- Additive decision-resolution storage. Deployment must drain old audit writers
-- before activating v2. This file does not alter metadata or run a backfill.
CREATE TABLE heimdall.audit_decisions (
    product_id text NOT NULL,
    environment text NOT NULL,
    decision_id text NOT NULL CHECK (decision_id ~ '^[A-Za-z0-9_-]{1,128}$'),
    request_event_id text NOT NULL,
    requested_at bigint NOT NULL CHECK (requested_at BETWEEN 0 AND 9007199254740991),
    resolution text NOT NULL DEFAULT 'pending' CHECK (resolution IN ('pending', 'completed', 'recovered')),
    terminal_event_id text,
    resolved_at bigint CHECK (resolved_at BETWEEN requested_at AND 9007199254740991),
    CHECK ((resolution = 'pending' AND terminal_event_id IS NULL AND resolved_at IS NULL)
        OR (resolution <> 'pending' AND terminal_event_id IS NOT NULL AND resolved_at IS NOT NULL)),
    CHECK (terminal_event_id IS NULL OR terminal_event_id <> request_event_id),
    PRIMARY KEY (product_id, environment, decision_id),
    UNIQUE (product_id, environment, request_event_id),
    UNIQUE (product_id, environment, terminal_event_id),
    FOREIGN KEY (product_id, environment, request_event_id)
        REFERENCES heimdall.audit_events (product_id, environment, event_id) ON DELETE RESTRICT,
    FOREIGN KEY (product_id, environment, terminal_event_id)
        REFERENCES heimdall.audit_events (product_id, environment, event_id) ON DELETE RESTRICT
);
CREATE INDEX audit_decisions_unresolved ON heimdall.audit_decisions
    (product_id, environment, requested_at, decision_id COLLATE "C") WHERE resolution = 'pending';

CREATE FUNCTION heimdall.guard_decision_resolution() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.resolution <> 'pending' THEN
            RAISE EXCEPTION 'decision must start pending' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;
    IF (NEW.product_id, NEW.environment, NEW.decision_id, NEW.request_event_id, NEW.requested_at)
        IS DISTINCT FROM (OLD.product_id, OLD.environment, OLD.decision_id, OLD.request_event_id, OLD.requested_at) THEN
        RAISE EXCEPTION 'immutable decision request' USING ERRCODE = '23514';
    END IF;
    IF OLD.resolution <> 'pending' AND NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'immutable decision resolution' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
REVOKE ALL ON FUNCTION heimdall.guard_decision_resolution() FROM PUBLIC;
CREATE TRIGGER decision_resolution BEFORE INSERT OR UPDATE ON heimdall.audit_decisions
    FOR EACH ROW EXECUTE FUNCTION heimdall.guard_decision_resolution();
