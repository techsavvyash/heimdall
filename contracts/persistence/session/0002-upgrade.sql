-- Deployment-owned receipt for prepared v2 DDL. No runtime activation occurs here.
CREATE TABLE heimdall.audit_upgrade (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    base_checksum text NOT NULL CHECK (base_checksum ~ '^[a-f0-9]{64}$'),
    decision_checksum text NOT NULL CHECK (decision_checksum ~ '^[a-f0-9]{64}$'),
    backfill_checksum text NOT NULL CHECK (backfill_checksum ~ '^[a-f0-9]{64}$'),
    preparation_checksum text NOT NULL CHECK (preparation_checksum ~ '^[a-f0-9]{64}$'),
    prepared_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);
REVOKE ALL ON heimdall.audit_upgrade FROM PUBLIC;
