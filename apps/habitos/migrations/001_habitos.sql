-- Hábitos y diario. Cada persona ve solo lo suyo. Lo que dice qué hábitos
-- tiene (nombre, emoji, objetivo, unidad, frecuencia, hora del recordatorio),
-- cuánto ha hecho cada día y todo el diario (ánimo, energía, sueño, nota y
-- etiquetas) va cifrado (platform/seal). En claro solo el dueño, los ids, las
-- fechas, el interruptor de archivado y una huella del nombre para no
-- repetirlo.
--
-- Conservación: hábitos, sus marcas y el diario, hasta que la persona los
-- borra (al borrar un hábito se van sus marcas); los avisos, 7 días.

-- Cada hábito.
CREATE TABLE habits (
    id          bigserial PRIMARY KEY,
    owner       text NOT NULL,
    name_token  bigint NOT NULL,                -- huella del nombre
    archived    boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    reminded_on date,                           -- último día en que se avisó
    sealed      bytea NOT NULL,                 -- nombre, emoji, tipo, objetivo, unidad, frecuencia, recordatorio
    UNIQUE (owner, name_token)
);

-- Lo hecho cada día de cada hábito (una fila por día; sin fila, no hecho).
CREATE TABLE checkins (
    habit_id   bigint NOT NULL REFERENCES habits ON DELETE CASCADE,
    owner      text NOT NULL,
    day        date NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    sealed     bytea NOT NULL,                  -- cantidad y origen
    PRIMARY KEY (habit_id, day)
);
CREATE INDEX checkins_owner ON checkins (owner, day);

-- El diario: una entrada por día.
CREATE TABLE journal (
    owner      text NOT NULL,
    day        date NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    sealed     bytea NOT NULL,                  -- ánimo, energía, sueño, nota, etiquetas
    PRIMARY KEY (owner, day)
);

-- Preferencias: el aviso de la tarde para escribir el diario.
CREATE TABLE settings (
    owner     text PRIMARY KEY,
    nudged_on date,                             -- último día en que se avisó
    sealed    bytea NOT NULL
);

-- Avisos para Telegram: los recoge la pasarela cada minuto.
CREATE TABLE notices (
    id           bigserial PRIMARY KEY,
    owner        text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    delivered_at timestamptz,
    sealed       bytea NOT NULL
);
CREATE INDEX notices_pending ON notices (owner) WHERE delivered_at IS NULL;
