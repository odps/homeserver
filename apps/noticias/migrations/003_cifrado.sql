-- Datos personales cifrados (platform/seal). Los intereses (nombre y
-- búsqueda), los feeds propios y los digests pasan a "sealed"; para no
-- repetir un interés o un feed quedan huellas del nombre y de la dirección,
-- no el texto. El paso en Go 003 cifra lo que ya hubiera y la migración 004
-- quita las columnas en claro. Las noticias y los ajustes numéricos siguen en
-- claro: son públicas o no dicen nada de la persona.
ALTER TABLE topics ADD COLUMN sealed bytea, ADD COLUMN name_token bigint;
ALTER TABLE own_feeds ADD COLUMN sealed bytea, ADD COLUMN url_token bigint;
ALTER TABLE digests ADD COLUMN sealed bytea;
