-- Cuándo se buscaron en internet los datos que le faltaban al libro (portada,
-- autores, sinopsis…). NULL: aún no se ha intentado.
ALTER TABLE books ADD COLUMN meta_checked_at timestamptz;
