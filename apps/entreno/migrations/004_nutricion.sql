-- Nutrición. Como el resto: cifrado (platform/seal), en claro solo el dueño,
-- las fechas y los identificadores. Se conserva mientras se use la app; cada
-- cosa se puede borrar desde la web.

-- Objetivos del día: kcal, proteína y, si se quiere, hidratos y grasa.
CREATE TABLE nutrition_goals (
    owner      text PRIMARY KEY,
    sealed     bytea NOT NULL,              -- {kcal, proteina, hidratos, grasa}
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Alimentos y comidas habituales de cada persona (para apuntar con un toque).
CREATE TABLE foods (
    id         bigserial PRIMARY KEY,
    owner      text NOT NULL,
    sealed     bytea NOT NULL,              -- nombre, base, valores, ración, ingredientes
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX foods_owner ON foods (owner);

-- Lo que se come: una fila por cosa apuntada, con sus valores ya calculados
-- (si luego cambia el alimento, lo apuntado no cambia).
CREATE TABLE food_logs (
    id        bigserial PRIMARY KEY,
    owner     text NOT NULL,
    day       date NOT NULL,
    logged_at timestamptz NOT NULL DEFAULT now(),
    source    text NOT NULL DEFAULT 'web' CHECK (source IN ('web', 'asistente')),
    sealed    bytea NOT NULL                -- comida, nombre, cantidad, kcal, proteína…
);
CREATE INDEX food_logs_owner_day ON food_logs (owner, day DESC, logged_at);
