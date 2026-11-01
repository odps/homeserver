-- Otra vuelta para los libros sin ISBN: ahora se busca sin el subtítulo
-- (con títulos largos los buscadores no encontraban nada).
UPDATE books SET meta_checked_at = NULL WHERE isbn IS NULL;
