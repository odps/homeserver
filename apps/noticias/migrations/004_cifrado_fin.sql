-- Fuera las columnas en claro (el paso 003 ya lo ha cifrado todo). Al acabar,
-- las tablas se reescriben con VACUUM FULL para que no quede el texto antiguo
-- en disco. Las restricciones únicas pasan a las huellas.
ALTER TABLE topics DROP COLUMN name, DROP COLUMN query, ALTER COLUMN sealed SET NOT NULL,
    ALTER COLUMN name_token SET NOT NULL, ADD CONSTRAINT topics_name_token UNIQUE (username, name_token);
ALTER TABLE own_feeds DROP COLUMN url, ALTER COLUMN sealed SET NOT NULL,
    ALTER COLUMN url_token SET NOT NULL, ADD CONSTRAINT own_feeds_url_token UNIQUE (username, url_token);
ALTER TABLE digests DROP COLUMN content, DROP COLUMN summary, ALTER COLUMN sealed SET NOT NULL;
