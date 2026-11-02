-- Fuera las columnas en claro (el paso 002 ya lo ha cifrado todo). Al acabar,
-- las tablas se reescriben con VACUUM FULL para que no quede el texto antiguo
-- en disco.
ALTER TABLE users DROP COLUMN prefs, ALTER COLUMN prefs_sealed SET NOT NULL;
ALTER TABLE objects DROP COLUMN data, DROP COLUMN summary, DROP COLUMN location, DROP COLUMN description,
    DROP COLUMN search, ALTER COLUMN sealed SET NOT NULL;
ALTER TABLE contacts DROP COLUMN data, DROP COLUMN fn, DROP COLUMN birthday, DROP COLUMN search,
    ALTER COLUMN sealed SET NOT NULL;
ALTER TABLE notices DROP COLUMN text, ALTER COLUMN sealed SET NOT NULL;
CREATE INDEX objects_tokens ON objects USING gin (tokens);
CREATE INDEX contacts_tokens ON contacts USING gin (tokens);
CREATE INDEX contacts_birthday ON contacts (birthday_token) WHERE birthday_token IS NOT NULL;
