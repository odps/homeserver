-- Lista de la compra. Una lista por persona, no compartida. Lo de cada
-- persona va cifrado (platform/seal): en claro solo el dueño, huellas de los
-- nombres para no duplicar y para el historial, si está tachada, el origen y
-- las fechas que necesita el borrado periódico.
--
-- Conservación: la lista, hasta que se tacha y se termina (o se vacía); el
-- historial y las secciones elegidas a mano, 18 meses sin comprarlo.

-- Cada cosa de la lista.
CREATE TABLE items (
    id         bigserial PRIMARY KEY,
    owner      text NOT NULL,
    name_token bigint NOT NULL,             -- huella del nombre normalizado
    done       boolean NOT NULL DEFAULT false,
    -- En la fase 2 las recetas añadirán cosas con origen «receta».
    source     text NOT NULL DEFAULT 'web' CHECK (source IN ('web', 'asistente', 'receta')),
    sealed     bytea NOT NULL               -- nombre, cantidad, nota, sección, cuándo se añadió
);
CREATE INDEX items_owner ON items (owner, name_token);

-- Lo comprado, por nombre normalizado: cuántas veces y cada cuánto.
CREATE TABLE history (
    owner       text NOT NULL,
    name_token  bigint NOT NULL,
    last_bought date NOT NULL,              -- para borrar lo que no se compra en 18 meses
    sealed      bytea NOT NULL,             -- nombre, veces, intervalo medio
    PRIMARY KEY (owner, name_token)
);
CREATE INDEX history_last ON history (last_bought);

-- La sección que la persona eligió a mano para una cosa.
CREATE TABLE section_prefs (
    owner      text NOT NULL,
    name_token bigint NOT NULL,
    updated_on date NOT NULL,               -- para el borrado periódico
    sealed     bytea NOT NULL,              -- nombre y sección
    PRIMARY KEY (owner, name_token)
);

-- Preferencias: el orden de las secciones (el recorrido de su súper).
CREATE TABLE settings (
    owner  text PRIMARY KEY,
    sealed bytea NOT NULL
);
