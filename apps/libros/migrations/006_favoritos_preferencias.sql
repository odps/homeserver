-- Favoritos de cada persona (libros y series) y preferencias de la app
-- (idioma de búsqueda, fuente de cómics, visibilidad al añadir, tema).
ALTER TABLE reading ADD COLUMN favorite boolean NOT NULL DEFAULT false;
ALTER TABLE comic_follows ADD COLUMN favorite boolean NOT NULL DEFAULT false;

ALTER TABLE settings ADD COLUMN prefs jsonb NOT NULL DEFAULT '{}';
-- Quien guarda preferencias sin haber tocado nunca los ajustes del lector no
-- tiene fecha de ajustes: cualquier dispositivo puede fijarlos.
ALTER TABLE settings ALTER COLUMN updated_at DROP NOT NULL;
