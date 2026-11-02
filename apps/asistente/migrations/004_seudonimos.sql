-- Seudónimos: los datos personales (teléfonos, direcciones, correos…) llegan
-- al modelo como «TEL_1», «DIR_2:Madrid»…; aquí se guarda su valor, cifrado,
-- para ponerlo de vuelta al llamar a una herramienta o al contestar. Duran lo
-- mismo que la conversación (30 días sin usarse).
CREATE TABLE pseudonyms (
    chat_id   bigint NOT NULL,
    label     text NOT NULL,          -- TEL_1
    kind      text NOT NULL,
    value_key bigint NOT NULL,        -- huella del dato: el mismo dato, la misma etiqueta
    sealed    bytea NOT NULL,
    used_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (chat_id, label),
    UNIQUE (chat_id, value_key)
);
