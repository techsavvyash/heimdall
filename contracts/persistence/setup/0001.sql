-- Independent operator setup journal. Install with heimdall-migrate.
CREATE SCHEMA heimdall_setup;
REVOKE ALL ON SCHEMA heimdall_setup FROM PUBLIC;

CREATE TABLE heimdall_setup.schema_metadata (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    database_version integer NOT NULL CHECK (database_version BETWEEN 1 AND 1000),
    state text NOT NULL CHECK (state IN ('ready', 'migrating')),
    reader_min integer NOT NULL CHECK (reader_min BETWEEN 1 AND 1000),
    reader_max integer NOT NULL CHECK (reader_max BETWEEN reader_min AND 1000),
    writer_min integer NOT NULL CHECK (writer_min BETWEEN 1 AND 1000),
    writer_max integer NOT NULL CHECK (writer_max BETWEEN writer_min AND 1000)
);
CREATE TABLE heimdall_setup.migrations (
    version integer PRIMARY KEY CHECK (version BETWEEN 1 AND 1000),
    checksum text NOT NULL CHECK (checksum ~ '^[a-f0-9]{64}$'),
    applied_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE heimdall_setup.plans (
    product_id text NOT NULL CHECK (product_id ~ '^[a-z][a-z0-9_-]{0,63}$'),
    environment text NOT NULL CHECK (environment ~ '^[a-z][a-z0-9_-]{0,63}$'),
    fingerprint text NOT NULL CHECK (fingerprint ~ '^[a-f0-9]{64}$'),
    step_count integer NOT NULL CHECK (step_count BETWEEN 1 AND 35),
    PRIMARY KEY (product_id, environment)
);

CREATE TABLE heimdall_setup.steps (
    product_id text NOT NULL,
    environment text NOT NULL,
    ordinal integer NOT NULL CHECK (ordinal BETWEEN 1 AND 35),
    dispatch_id uuid NOT NULL UNIQUE,
    resource_id text CHECK (resource_id ~ '^[A-Za-z0-9_-]{1,128}$'),
    dispatched_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    confirmed_at timestamptz,
    CHECK ((resource_id IS NULL) = (confirmed_at IS NULL)),
    PRIMARY KEY (product_id, environment, ordinal),
    FOREIGN KEY (product_id, environment)
        REFERENCES heimdall_setup.plans (product_id, environment) ON DELETE RESTRICT
);

-- Lock the plan before reading the predecessor or claiming a step. An unresolved
-- dispatch survives process death and permanently prevents automatic redispatch.
CREATE FUNCTION heimdall_setup.claim(
    p_product text, p_environment text, p_fingerprint text,
    p_count integer, p_ordinal integer, p_dispatch uuid
) RETURNS TABLE(state text, dispatch_id uuid, resource_id text)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
DECLARE plan heimdall_setup.plans%ROWTYPE;
DECLARE step heimdall_setup.steps%ROWTYPE;
BEGIN
    IF p_ordinal IS NULL OR p_count IS NULL OR p_dispatch IS NULL
        OR p_ordinal NOT BETWEEN 1 AND p_count THEN
        RETURN QUERY SELECT 'conflict'::text,NULL::uuid,NULL::text;
        RETURN;
    END IF;
    INSERT INTO heimdall_setup.plans VALUES (p_product,p_environment,p_fingerprint,p_count)
        ON CONFLICT DO NOTHING;
    SELECT * INTO STRICT plan FROM heimdall_setup.plans p
        WHERE p.product_id=p_product AND p.environment=p_environment FOR UPDATE;
    IF plan.fingerprint<>p_fingerprint OR plan.step_count<>p_count THEN
        RETURN QUERY SELECT 'conflict'::text,NULL::uuid,NULL::text;
        RETURN;
    END IF;
    SELECT * INTO step FROM heimdall_setup.steps s
        WHERE s.product_id=p_product AND s.environment=p_environment AND s.ordinal=p_ordinal;
    IF FOUND THEN
        RETURN QUERY SELECT CASE WHEN step.resource_id IS NULL THEN 'unresolved' ELSE 'confirmed' END,
            step.dispatch_id,step.resource_id;
        RETURN;
    END IF;
    IF p_ordinal>1 AND NOT EXISTS (SELECT 1 FROM heimdall_setup.steps s
        WHERE s.product_id=p_product AND s.environment=p_environment AND s.ordinal=p_ordinal-1
        AND s.resource_id IS NOT NULL) THEN
        RETURN QUERY SELECT 'conflict'::text,NULL::uuid,NULL::text;
        RETURN;
    END IF;
    INSERT INTO heimdall_setup.steps (product_id,environment,ordinal,dispatch_id)
        VALUES (p_product,p_environment,p_ordinal,p_dispatch);
    RETURN QUERY SELECT 'claimed'::text,p_dispatch,NULL::text;
END
$$;

-- The caller must verify the resource and its link to the dispatch before confirming.
-- Exact replay is safe when the database committed but the response was lost.
CREATE FUNCTION heimdall_setup.confirm(
    p_product text, p_environment text, p_fingerprint text,
    p_ordinal integer, p_dispatch uuid, p_resource text
) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
DECLARE recorded text;
BEGIN
    PERFORM 1 FROM heimdall_setup.plans p
        WHERE p.product_id=p_product AND p.environment=p_environment
        AND p.fingerprint=p_fingerprint FOR UPDATE;
    IF NOT FOUND OR p_resource IS NULL THEN RETURN false; END IF;
    UPDATE heimdall_setup.steps s SET resource_id=p_resource,confirmed_at=clock_timestamp()
        WHERE s.product_id=p_product AND s.environment=p_environment AND s.ordinal=p_ordinal
        AND s.dispatch_id=p_dispatch AND s.resource_id IS NULL;
    IF FOUND THEN RETURN true; END IF;
    SELECT s.resource_id INTO recorded FROM heimdall_setup.steps s
        WHERE s.product_id=p_product AND s.environment=p_environment AND s.ordinal=p_ordinal
        AND s.dispatch_id=p_dispatch;
    RETURN COALESCE(recorded=p_resource,false);
END
$$;

REVOKE EXECUTE ON ALL FUNCTIONS IN SCHEMA heimdall_setup FROM PUBLIC;
