ALTER TABLE support.tickets
    ADD COLUMN assigned_at timestamptz,
    ADD COLUMN assigned_to text,
    ADD CONSTRAINT ticket_assignment_pair CHECK ((assigned_at IS NULL) = (assigned_to IS NULL));
