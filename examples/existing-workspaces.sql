-- Run with psql -v product_schema=ledger or -v product_schema=support.
-- An established product can query its own workspace catalog instead.
CREATE TABLE :"product_schema".workspaces (
    id text COLLATE "C" PRIMARY KEY CHECK (id ~ '^[A-Za-z0-9_-]{1,128}$'),
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 256),
    active boolean NOT NULL
);
