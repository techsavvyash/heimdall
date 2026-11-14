ALTER TABLE ledger.invoices
    ADD COLUMN approved_at timestamptz,
    ADD COLUMN approved_by text,
    ADD CONSTRAINT invoice_approval_pair CHECK ((approved_at IS NULL) = (approved_by IS NULL));
