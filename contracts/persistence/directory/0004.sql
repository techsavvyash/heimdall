-- Install after 0003 in a deployment-owned transaction.
CREATE TABLE heimdall_directory.polling (
    product_id text NOT NULL,
    environment text NOT NULL,
    issuer text NOT NULL,
    organization_id text NOT NULL,
    start_index bigint NOT NULL CHECK (start_index BETWEEN 1 AND 9007199254740991),
    revision bigint NOT NULL DEFAULT 0 CHECK (revision >= 0),
    PRIMARY KEY (product_id, environment, issuer, organization_id)
);
CREATE TABLE heimdall_directory.subjects (
    product_id text NOT NULL,
    environment text NOT NULL,
    issuer text NOT NULL,
    organization_id text NOT NULL,
    subject text COLLATE "C" NOT NULL CHECK (subject ~ '^[A-Za-z0-9_-]{1,128}$'),
    PRIMARY KEY (product_id, environment, issuer, organization_id, subject)
);
