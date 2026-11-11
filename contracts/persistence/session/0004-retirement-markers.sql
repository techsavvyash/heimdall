-- Prepared retirement integrity only. Requires identities, holds and capacity.
-- No runtime grants, v4 publication or authorized purge API are installed.
CREATE TABLE heimdall_retention.retirements (
    product_id text NOT NULL,
    environment text NOT NULL,
    retirement_id text NOT NULL CHECK (retirement_id ~ '^[A-Za-z0-9_-]{1,128}$'),
    target_event_id text NOT NULL,
    tenant_id text CHECK (tenant_id ~ '^[A-Za-z0-9_-]{1,128}$'),
    retired_at bigint NOT NULL CHECK (retired_at BETWEEN 0 AND 9007199254740991),
    retain_for_ms bigint NOT NULL CHECK (retain_for_ms BETWEEN 1 AND 9007199254740991),
    administration_event_id text NOT NULL,
    member_event_ids jsonb NOT NULL CHECK (jsonb_typeof(member_event_ids)='array' AND jsonb_array_length(member_event_ids) BETWEEN 1 AND 3),
    PRIMARY KEY(product_id,environment,retirement_id),
    UNIQUE(product_id,environment,target_event_id),
    FOREIGN KEY(product_id,environment,target_event_id) REFERENCES heimdall_retention.event_identities(product_id,environment,event_id),
    FOREIGN KEY(product_id,environment,administration_event_id) REFERENCES heimdall_retention.event_identities(product_id,environment,event_id)
);
CREATE FUNCTION heimdall_retention.guard_retirement() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE target record; decision record; members text[]; expected jsonb; verified bigint;
BEGIN
    IF TG_OP='DELETE' THEN RAISE EXCEPTION 'retirement history cannot be deleted' USING ERRCODE='23514'; END IF;
    IF TG_OP='UPDATE' THEN
        IF NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION 'immutable retirement' USING ERRCODE='23514'; END IF;
        RETURN NEW;
    END IF;
    SELECT * INTO target FROM heimdall_retention.event_identities WHERE product_id=NEW.product_id AND environment=NEW.environment AND event_id=NEW.target_event_id;
    IF NOT FOUND OR target.tenant_id IS DISTINCT FROM NEW.tenant_id THEN RAISE EXCEPTION 'retirement target mismatch' USING ERRCODE='23514'; END IF;
    IF target.kind='ordinary' THEN members:=ARRAY[target.event_id];
    ELSIF target.kind='request' THEN
        SELECT * INTO decision FROM heimdall_retention.decision_identities WHERE product_id=NEW.product_id AND environment=NEW.environment AND decision_id=target.decision_id;
        IF NOT FOUND OR decision.resolution='pending' OR decision.tenant_id IS DISTINCT FROM NEW.tenant_id OR decision.request_event_id<>target.event_id THEN
            RAISE EXCEPTION 'retirement decision incomplete' USING ERRCODE='23514';
        END IF;
        members:=array_remove(ARRAY[decision.request_event_id,decision.terminal_event_id,decision.recovery_event_id],NULL);
    ELSE RAISE EXCEPTION 'retirement requires whole group' USING ERRCODE='23514';
    END IF;
    SELECT to_jsonb(array_agg(id ORDER BY id COLLATE "C")) INTO expected FROM unnest(members) id;
    IF NEW.member_event_ids IS DISTINCT FROM expected OR NEW.administration_event_id=ANY(members) OR NEW.retired_at<NEW.retain_for_ms THEN
        RAISE EXCEPTION 'retirement members or time mismatch' USING ERRCODE='23514';
    END IF;
    PERFORM event_id FROM heimdall_retention.event_identities WHERE product_id=NEW.product_id AND environment=NEW.environment AND event_id=ANY(members) ORDER BY event_id COLLATE "C" FOR UPDATE;
    PERFORM event_id FROM heimdall.audit_deliveries WHERE product_id=NEW.product_id AND environment=NEW.environment AND event_id=ANY(members) ORDER BY event_id COLLATE "C",sink_id COLLATE "C" FOR UPDATE;
    IF EXISTS(SELECT 1 FROM heimdall_retention.event_holds WHERE product_id=NEW.product_id AND environment=NEW.environment AND event_id=ANY(members) AND released_at IS NULL) THEN
        RAISE EXCEPTION 'retirement group held' USING ERRCODE='23514';
    END IF;
    SELECT count(*) INTO verified FROM heimdall_retention.event_identities i JOIN heimdall.audit_events e USING(product_id,environment,event_id)
      JOIN heimdall_capacity.event_charges c USING(product_id,environment,event_id)
      WHERE i.product_id=NEW.product_id AND i.environment=NEW.environment AND i.event_id=ANY(members)
      AND i.tenant_id IS NOT DISTINCT FROM NEW.tenant_id AND i.recorded_at=e.recorded_at AND i.payload_digest=e.payload_digest
      AND i.recorded_at<=NEW.retired_at-NEW.retain_for_ms AND c.envelope_bytes=octet_length(e.event::text)
      AND i.routing=to_jsonb(ARRAY(SELECT sink_id FROM heimdall.audit_deliveries d WHERE d.product_id=i.product_id AND d.environment=i.environment AND d.event_id=i.event_id ORDER BY sink_id COLLATE "C"))
      AND c.delivery_rows=jsonb_array_length(i.routing);
    IF verified<>cardinality(members) OR EXISTS(SELECT 1 FROM heimdall.audit_deliveries WHERE product_id=NEW.product_id AND environment=NEW.environment AND event_id=ANY(members)
      AND (state<>'delivered' OR delivered_at IS NULL OR delivered_at>NEW.retired_at-NEW.retain_for_ms)) THEN
        RAISE EXCEPTION 'retirement history ineligible' USING ERRCODE='23514';
    END IF;
    IF NOT EXISTS(SELECT 1 FROM heimdall_retention.event_identities i JOIN heimdall.audit_events e USING(product_id,environment,event_id)
      WHERE i.product_id=NEW.product_id AND i.environment=NEW.environment AND i.event_id=NEW.administration_event_id
      AND i.tenant_id IS NOT DISTINCT FROM NEW.tenant_id AND i.recorded_at=NEW.retired_at AND e.recorded_at=i.recorded_at AND e.payload_digest=i.payload_digest) THEN
        RAISE EXCEPTION 'retirement audit reference mismatch' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
REVOKE ALL ON FUNCTION heimdall_retention.guard_retirement() FROM PUBLIC;
CREATE TRIGGER guard_retirement BEFORE INSERT OR UPDATE OR DELETE ON heimdall_retention.retirements FOR EACH ROW EXECUTE FUNCTION heimdall_retention.guard_retirement();
CREATE TRIGGER charge_metadata AFTER INSERT OR UPDATE ON heimdall_retention.retirements FOR EACH ROW EXECUTE FUNCTION heimdall_retention.charge_metadata();
CREATE FUNCTION heimdall_retention.verify_retirement_commit() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
    IF EXISTS(SELECT 1 FROM heimdall.audit_events WHERE product_id=NEW.product_id AND environment=NEW.environment AND NEW.member_event_ids ? event_id)
      OR EXISTS(SELECT 1 FROM heimdall_retention.event_holds WHERE product_id=NEW.product_id AND environment=NEW.environment AND NEW.member_event_ids ? event_id AND released_at IS NULL)
      OR NOT EXISTS(SELECT 1 FROM heimdall.audit_events WHERE product_id=NEW.product_id AND environment=NEW.environment AND event_id=NEW.administration_event_id) THEN
        RAISE EXCEPTION 'retirement transaction incomplete' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
REVOKE ALL ON FUNCTION heimdall_retention.verify_retirement_commit() FROM PUBLIC;
CREATE CONSTRAINT TRIGGER verify_retirement_commit AFTER INSERT ON heimdall_retention.retirements DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION heimdall_retention.verify_retirement_commit();

-- Include permanent markers when adopting existing metadata.
CREATE OR REPLACE FUNCTION heimdall_retention.initialize_capacity() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
    -- DDL locks and drained writers are required while adopting existing rows.
    IF EXISTS(SELECT 1 FROM heimdall_retention.retirements r WHERE octet_length(to_jsonb(r)::text)>8192)
      OR EXISTS(SELECT 1 FROM heimdall_retention.event_identities i WHERE octet_length(to_jsonb(i)::text)>8192)
      OR EXISTS(SELECT 1 FROM heimdall_retention.decision_identities d WHERE octet_length(to_jsonb(d)::text)>8192)
      OR EXISTS(SELECT 1 FROM heimdall_retention.event_holds h WHERE octet_length(to_jsonb(h)::text)>8192) THEN
        RAISE EXCEPTION 'retention metadata exceeds slot size' USING ERRCODE='23514';
    END IF;
    SELECT (SELECT count(*) FROM heimdall_retention.retirements)+(SELECT count(*) FROM heimdall_retention.event_identities)+(SELECT count(*) FROM heimdall_retention.decision_identities)+(SELECT count(*) FROM heimdall_retention.event_holds) INTO NEW.used_rows;
    SELECT coalesce(sum(2-(SELECT count(*) FROM heimdall_retention.event_identities i
      WHERE i.product_id=d.product_id AND i.environment=d.environment AND i.decision_id=d.decision_id AND i.kind IN ('terminal','recovery'))),0)
      INTO NEW.reserved_rows FROM heimdall_retention.decision_identities d WHERE d.resolution='pending';
    RETURN NEW;
END;
$$;
