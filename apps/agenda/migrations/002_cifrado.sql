-- Datos personales cifrados (platform/seal). El texto de eventos, tareas,
-- contactos, avisos y ajustes pasa a "sealed"; para buscar quedan huellas de
-- las palabras ("tokens"), no las palabras. El paso en Go 002 cifra lo que ya
-- hubiera y la migración 003 quita las columnas en claro.
ALTER TABLE users ADD COLUMN prefs_sealed bytea;
ALTER TABLE objects ADD COLUMN sealed bytea, ADD COLUMN tokens bigint[] NOT NULL DEFAULT '{}';
ALTER TABLE contacts ADD COLUMN sealed bytea, ADD COLUMN tokens bigint[] NOT NULL DEFAULT '{}',
    ADD COLUMN birthday_token bigint;
ALTER TABLE notices ADD COLUMN sealed bytea;

-- El correo se vuelve a descargar del buzón: así se guarda cifrado y se
-- clasifica por sus cabeceras (que antes no se guardaban).
DROP TABLE messages;
CREATE TABLE messages (
    id          bigserial PRIMARY KEY,
    account_id  bigint NOT NULL REFERENCES mail_accounts ON DELETE CASCADE,
    uid         bigint NOT NULL,
    sent_at     timestamptz NOT NULL,
    seen        boolean NOT NULL DEFAULT false,
    flagged     boolean NOT NULL DEFAULT false,
    category    text NOT NULL DEFAULT 'personal'
                CHECK (category IN ('personal', 'importante', 'notificacion', 'comercial')),
    sealed      bytea NOT NULL,           -- remitente, destinatarios, asunto, texto, adjuntos…
    tokens      bigint[] NOT NULL DEFAULT '{}',
    UNIQUE (account_id, uid)
);
CREATE INDEX messages_recent ON messages (account_id, sent_at DESC);
CREATE INDEX messages_tokens ON messages USING gin (tokens);
UPDATE mail_accounts SET uidvalidity = 0, last_uid = 0;
