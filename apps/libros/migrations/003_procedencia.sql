-- De dónde viene un libro importado de una biblioteca pública, por ejemplo
-- «gutenberg:2000». Vacío en los subidos a mano.
ALTER TABLE books ADD COLUMN source text;
CREATE INDEX books_source ON books (source) WHERE source IS NOT NULL;
