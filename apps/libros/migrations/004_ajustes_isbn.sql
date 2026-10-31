-- ISBN de cada libro (normalizado a 13 dígitos cuando es válido) y ajustes de
-- lectura de cada persona, para que se apliquen en todos sus dispositivos.
ALTER TABLE books ADD COLUMN isbn text CHECK (isbn ~ '^[0-9]{9}[0-9X]$|^[0-9]{13}$');
CREATE INDEX books_isbn ON books (isbn) WHERE isbn IS NOT NULL;

CREATE TABLE settings (
    username   text PRIMARY KEY,
    reader     jsonb NOT NULL,
    updated_at timestamptz NOT NULL
);
