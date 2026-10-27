-- Tournament categories + payment options (StrollX create/edit form).
ALTER TABLE tournaments
  ADD COLUMN IF NOT EXISTS group_url TEXT NULL;
ALTER TABLE tournaments
  ADD COLUMN IF NOT EXISTS allow_full_payment BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE tournaments
  ADD COLUMN IF NOT EXISTS allow_advance_payment BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE tournaments
  ADD COLUMN IF NOT EXISTS allow_spot_payment BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE tournaments
  ADD COLUMN IF NOT EXISTS advance_amount NUMERIC(12,2) NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS tournament_categories (
  id               BIGSERIAL PRIMARY KEY,
  tournament_id    BIGINT NOT NULL REFERENCES tournaments(id) ON DELETE CASCADE,
  name             VARCHAR(64) NOT NULL,
  fee              NUMERIC(12,2) NOT NULL DEFAULT 0,
  team_size        INT NOT NULL DEFAULT 1,
  size_label       VARCHAR(64) NOT NULL DEFAULT '',
  sort_order       INT NOT NULL DEFAULT 0,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (tournament_id, name)
);
CREATE INDEX IF NOT EXISTS idx_tournament_categories_tid
  ON tournament_categories(tournament_id, sort_order, id);
