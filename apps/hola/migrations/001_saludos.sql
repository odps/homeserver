CREATE TABLE saludos (
    id     bigserial PRIMARY KEY,
    texto  text NOT NULL CHECK (length(texto) BETWEEN 1 AND 500),
    creado timestamptz NOT NULL DEFAULT now()
);
