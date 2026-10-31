-- Varias personas: cada libro tiene dueño y es privado o compartido; el estado
-- de lectura y el progreso pasan a ser de cada persona. Los datos anteriores
-- quedan con usuario '' (se asignan después a la cuenta del propietario).

ALTER TABLE books ADD COLUMN owner text NOT NULL DEFAULT '';
ALTER TABLE books ADD COLUMN visibility text NOT NULL DEFAULT 'compartido'
    CHECK (visibility IN ('privado', 'compartido'));

-- El mismo fichero puede estar en la biblioteca privada de dos personas: se
-- guarda una vez en disco y hay una fila por dueño.
ALTER TABLE books DROP CONSTRAINT books_sha256_key;
CREATE UNIQUE INDEX books_owner_sha ON books (owner, sha256);
CREATE INDEX books_sha ON books (sha256);

CREATE TABLE reading (
    book_id     bigint NOT NULL REFERENCES books ON DELETE CASCADE,
    username    text NOT NULL,
    status      text NOT NULL CHECK (status IN ('pendiente', 'leyendo', 'leido')),
    finished_at timestamptz,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (book_id, username)
);
INSERT INTO reading (book_id, username, status, finished_at, updated_at)
    SELECT id, '', status, finished_at, updated_at FROM books WHERE status <> 'pendiente';
ALTER TABLE books DROP COLUMN status;
ALTER TABLE books DROP COLUMN finished_at;

ALTER TABLE progress ADD COLUMN username text NOT NULL DEFAULT '';
ALTER TABLE progress DROP CONSTRAINT progress_pkey;
ALTER TABLE progress ADD PRIMARY KEY (book_id, username, device_id);
ALTER TABLE progress ALTER COLUMN username DROP DEFAULT;
