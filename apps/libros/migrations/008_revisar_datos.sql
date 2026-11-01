-- Se vuelven a buscar los datos de los libros a los que no se les encontró
-- nada (sin ISBN): ahora el autor se limpia antes de buscar («Navarro, Joe,
-- 1953-» → «Joe Navarro»).
UPDATE books SET meta_checked_at = NULL WHERE isbn IS NULL;
