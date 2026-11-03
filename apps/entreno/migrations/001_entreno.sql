-- Entrenos. Lo de cada persona va cifrado (platform/seal): en claro solo el
-- dueño, las fechas y huellas para filtrar. El historial se conserva mientras
-- se use la app (es su razón de ser); se borra al quitar la cuenta.

-- Una sesión de gimnasio.
CREATE TABLE sessions (
    id         bigserial PRIMARY KEY,
    owner      text NOT NULL,
    day        date NOT NULL,
    started_at timestamptz NOT NULL,
    source     text NOT NULL DEFAULT 'web' CHECK (source IN ('web', 'asistente', 'whatsapp')),
    sealed     bytea NOT NULL,              -- notas de la sesión
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_owner_day ON sessions (owner, day DESC);

-- Cada ejercicio de una sesión, con sus series.
CREATE TABLE entries (
    id         bigserial PRIMARY KEY,
    session_id bigint NOT NULL REFERENCES sessions ON DELETE CASCADE,
    owner      text NOT NULL,
    position   int NOT NULL,
    ex_token   bigint NOT NULL,             -- huella del ejercicio, para su historial
    sealed     bytea NOT NULL               -- ejercicio, cómo se escribió, series y nota
);
CREATE INDEX entries_session ON entries (session_id, position);
CREATE INDEX entries_owner_ex ON entries (owner, ex_token);

-- Cómo llama la persona a cada ejercicio («Pressa» → prensa de piernas).
CREATE TABLE aliases (
    owner       text NOT NULL,
    alias_token bigint NOT NULL,
    sealed      bytea NOT NULL,             -- {alias, ejercicio}
    PRIMARY KEY (owner, alias_token)
);

-- Ejercicios propios que no están en el catálogo.
CREATE TABLE custom_exercises (
    id     bigserial PRIMARY KEY,
    owner  text NOT NULL,
    sealed bytea NOT NULL                   -- nombre, tipo, músculos
);
CREATE INDEX custom_owner ON custom_exercises (owner);
