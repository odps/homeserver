-- Última ubicación compartida en Telegram, cifrada (platform/seal). Caduca:
-- until es cuándo deja de valer y se borra.
CREATE TABLE locations (
    chat_id bigint PRIMARY KEY,
    sealed  bytea NOT NULL,
    until   timestamptz NOT NULL
);
