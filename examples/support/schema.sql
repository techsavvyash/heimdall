CREATE SCHEMA support;
CREATE TABLE support.tickets (
    workspace_id text NOT NULL,
    ticket_id text NOT NULL,
    summary text NOT NULL,
    PRIMARY KEY (workspace_id, ticket_id)
);
