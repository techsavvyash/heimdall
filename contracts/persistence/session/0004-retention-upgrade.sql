-- Offline retention receipt and bounded backfill checkpoint. No v4 activation.
CREATE TABLE heimdall_retention.upgrade (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    checksums text[] NOT NULL CHECK (array_ndims(checksums)=1 AND array_lower(checksums,1)=1 AND cardinality(checksums)=6 AND array_position(checksums,NULL) IS NULL),
    row_limit bigint NOT NULL CHECK (row_limit BETWEEN 1 AND 1099511627775),
    byte_limit bigint NOT NULL CHECK (byte_limit BETWEEN 8192 AND 9007199254740991),
    prepared_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE heimdall_retention.backfill (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    phase text NOT NULL DEFAULT 'ordinary' CHECK (phase IN ('ordinary','decisions','complete')),
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
REVOKE ALL ON heimdall_retention.upgrade,heimdall_retention.backfill FROM PUBLIC;
