CREATE SCHEMA ledger;
CREATE TABLE ledger.invoices (
    workspace_id text NOT NULL,
    invoice_id text NOT NULL,
    total_cents integer NOT NULL CHECK (total_cents >= 0),
    PRIMARY KEY (workspace_id, invoice_id)
);
