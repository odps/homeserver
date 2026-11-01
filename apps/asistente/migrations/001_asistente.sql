-- Conversaciones (solo el texto de cada turno), notas, uso diario del modelo
-- y valores sueltos (posición en la cola de Telegram).
CREATE TABLE chats (
    chat_id  bigint PRIMARY KEY,
    reset_at timestamptz NOT NULL DEFAULT now(), -- /nuevo: se olvida lo anterior
    last_at  timestamptz
);

CREATE TABLE messages (
    id         bigserial PRIMARY KEY,
    chat_id    bigint NOT NULL,
    role       text NOT NULL CHECK (role IN ('user', 'assistant')),
    content    text NOT NULL CHECK (length(content) <= 20000),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX messages_chat ON messages (chat_id, id DESC);

CREATE TABLE notes (
    id         bigserial PRIMARY KEY,
    text       text NOT NULL CHECK (length(text) BETWEEN 1 AND 4000),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE usage (
    day               date PRIMARY KEY,
    requests          int NOT NULL DEFAULT 0,
    prompt_tokens     bigint NOT NULL DEFAULT 0,
    completion_tokens bigint NOT NULL DEFAULT 0
);

CREATE TABLE kv (
    key   text PRIMARY KEY,
    value text NOT NULL
);
