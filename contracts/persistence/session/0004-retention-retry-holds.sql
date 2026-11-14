-- Prepared explicit retry obligations. Install transactionally with writers drained.
-- Reuse the hold ledger's scope, audit references, identity lock and metadata slot.
ALTER TABLE heimdall_retention.event_holds ADD COLUMN purpose text NOT NULL DEFAULT 'administrative'
    CHECK (purpose IN ('administrative','retry'));
DO $$
BEGIN
    IF EXISTS(SELECT 1 FROM heimdall_retention.event_holds h WHERE octet_length(to_jsonb(h)::text)>8192) THEN
        RAISE EXCEPTION 'retention metadata exceeds slot size' USING ERRCODE='23514';
    END IF;
END;
$$;
CREATE FUNCTION heimdall_retention.guard_hold_purpose() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
    IF NEW.purpose IS DISTINCT FROM OLD.purpose THEN
        RAISE EXCEPTION 'immutable hold purpose' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
REVOKE ALL ON FUNCTION heimdall_retention.guard_hold_purpose() FROM PUBLIC;
CREATE TRIGGER guard_hold_purpose BEFORE UPDATE ON heimdall_retention.event_holds
    FOR EACH ROW EXECUTE FUNCTION heimdall_retention.guard_hold_purpose();
-- Existing retirement guards check every active hold, so retry obligations inherit
-- whole-group exclusion and the deferred commit check. There is no expiry column,
-- runtime grant, automatic release, or v4 activation in this preparation.
