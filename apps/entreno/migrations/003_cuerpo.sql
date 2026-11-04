-- Cuerpo y cardio. Como el resto: cifrado (platform/seal), en claro solo el
-- dueño, las fechas y los identificadores. Se conserva mientras se use la
-- app; cada cosa se puede borrar desde la web.

-- Peso, grasa y medidas: una fila por persona y día.
CREATE TABLE body_logs (
    owner      text NOT NULL,
    day        date NOT NULL,
    sealed     bytea NOT NULL,              -- {kg, grasa, medidas, nota}
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (owner, day)
);

-- Fotos de progreso. La imagen va cifrada en DATA_DIR/fotos/<id>; aquí, de
-- quién es, de qué día y (cifrado) la pose y la nota.
CREATE TABLE photos (
    id         text PRIMARY KEY,            -- aleatorio: también el nombre del fichero
    owner      text NOT NULL,
    day        date NOT NULL,
    sealed     bytea NOT NULL,              -- {pose, nota, ancho, alto}
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX photos_owner_day ON photos (owner, day DESC);

-- Cardio: correr, bici, elíptica, remo…
CREATE TABLE cardio (
    id         bigserial PRIMARY KEY,
    owner      text NOT NULL,
    day        date NOT NULL,
    started_at timestamptz NOT NULL,
    source     text NOT NULL DEFAULT 'web' CHECK (source IN ('web', 'asistente')),
    sealed     bytea NOT NULL               -- tipo, duración, distancia, pulso, kcal, nota
);
CREATE INDEX cardio_owner_day ON cardio (owner, day DESC, started_at DESC);
