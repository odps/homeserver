-- Rutinas: los días fijos (empuje, tirón, pierna…) con sus ejercicios,
-- series, rango de repeticiones, RIR objetivo y descanso. Cifradas.
CREATE TABLE routines (
    id         bigserial PRIMARY KEY,
    owner      text NOT NULL,
    position   int NOT NULL DEFAULT 0,
    sealed     bytea NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX routines_owner ON routines (owner, position);
