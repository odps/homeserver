-- Los feeds se identifican por una huella (platform/seal), no por su URL: la
-- de un interés lleva la búsqueda en claro y la de un feed propio dice qué
-- lees. El título y el error de cada feed van cifrados (el error suele llevar
-- la URL). El paso en Go 005 rellena lo nuevo; la 006 quita lo antiguo.
ALTER TABLE items ADD COLUMN feed_key bigint;
ALTER TABLE feeds ADD COLUMN key bigint, ADD COLUMN sealed bytea;
