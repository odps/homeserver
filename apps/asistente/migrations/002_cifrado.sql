-- Conversaciones y notas cifradas (platform/seal). El paso en Go 002 cifra lo
-- que ya hubiera; la 003 quita las columnas en claro.
ALTER TABLE messages ADD COLUMN sealed bytea;
ALTER TABLE notes ADD COLUMN sealed bytea, ADD COLUMN tokens bigint[] NOT NULL DEFAULT '{}';
