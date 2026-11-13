CREATE TABLE IF NOT EXISTS ledger.invitations (
    environment text NOT NULL,
    token_hash text NOT NULL,
    workspace_id text NOT NULL,
    issuer text NOT NULL,
    recipient text NOT NULL,
    inviter text NOT NULL,
    roles jsonb NOT NULL,
    expires_at timestamptz NOT NULL,
    claimed_at timestamptz,
    operation_id text,
    PRIMARY KEY (environment, token_hash)
);
