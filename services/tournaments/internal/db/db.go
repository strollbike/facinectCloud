package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func Open(host, port, user, password, name string) (*sql.DB, error) {
	dsn := fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s?sslmode=disable",
		user, password, host, port, name,
	)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func EnsureSchema(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS tournaments (
  id                 BIGSERIAL PRIMARY KEY,
  facility_id        BIGINT NOT NULL,
  sport_id           BIGINT NULL,
  name               VARCHAR(191) NOT NULL,
  start_date         DATE NULL,
  end_date           DATE NULL,
  venue_address      TEXT NULL,
  location_url       TEXT NULL,
  contact_numbers    VARCHAR(255) NULL,
  rules              TEXT NULL,
  slug               VARCHAR(191) NULL,
  status             VARCHAR(16) NOT NULL DEFAULT 'draft'
                     CHECK (status IN ('draft','upcoming','live','completed','cancelled')),
  created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_tournaments_facility
  ON tournaments(facility_id, status);
CREATE INDEX IF NOT EXISTS idx_tournaments_facility_dates
  ON tournaments(facility_id, start_date DESC NULLS LAST);
CREATE UNIQUE INDEX IF NOT EXISTS idx_tournaments_facility_slug
  ON tournaments(facility_id, slug) WHERE slug IS NOT NULL AND slug <> '';

CREATE TABLE IF NOT EXISTS tournament_entries (
  id               BIGSERIAL PRIMARY KEY,
  tournament_id    BIGINT NOT NULL REFERENCES tournaments(id) ON DELETE CASCADE,
  category         VARCHAR(64) NOT NULL DEFAULT '',
  gender           VARCHAR(32) NOT NULL DEFAULT '',
  player_name      VARCHAR(191) NOT NULL,
  phone            VARCHAR(32) NULL,
  email            VARCHAR(191) NULL,
  payment_status   VARCHAR(32) NOT NULL DEFAULT 'pending',
  status           VARCHAR(32) NOT NULL DEFAULT 'active',
  created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_tournament_entries_tid
  ON tournament_entries(tournament_id, category, gender);

CREATE TABLE IF NOT EXISTS tournament_fixtures (
  id               BIGSERIAL PRIMARY KEY,
  tournament_id    BIGINT NOT NULL REFERENCES tournaments(id) ON DELETE CASCADE,
  category         VARCHAR(64) NOT NULL DEFAULT '',
  gender           VARCHAR(32) NOT NULL DEFAULT '',
  round_no         INT NOT NULL DEFAULT 1,
  match_no         INT NOT NULL DEFAULT 1,
  team1            VARCHAR(191) NOT NULL DEFAULT '',
  team2            VARCHAR(191) NOT NULL DEFAULT '',
  score1           INT NULL,
  score2           INT NULL,
  winner           VARCHAR(191) NULL,
  status           VARCHAR(32) NOT NULL DEFAULT 'scheduled',
  court            VARCHAR(64) NULL,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_tournament_fixtures_tid
  ON tournament_fixtures(tournament_id, category, round_no, match_no);

CREATE TABLE IF NOT EXISTS tournament_templates (
  id               BIGSERIAL PRIMARY KEY,
  tournament_id    BIGINT NOT NULL UNIQUE REFERENCES tournaments(id) ON DELETE CASCADE,
  image_url        TEXT NOT NULL DEFAULT '',
  ext              VARCHAR(16) NULL,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS tournament_score_events (
  id               BIGSERIAL PRIMARY KEY,
  tournament_id    BIGINT NOT NULL REFERENCES tournaments(id) ON DELETE CASCADE,
  category         VARCHAR(64) NOT NULL DEFAULT '',
  match_id         BIGINT NOT NULL,
  score1           INT NULL,
  score2           INT NULL,
  status           VARCHAR(32) NULL,
  winner           VARCHAR(191) NULL,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_tournament_score_events_poll
  ON tournament_score_events(tournament_id, category, id);

CREATE TABLE IF NOT EXISTS tournament_referees (
  id               BIGSERIAL PRIMARY KEY,
  tournament_id    BIGINT NOT NULL REFERENCES tournaments(id) ON DELETE CASCADE,
  category         VARCHAR(64) NOT NULL DEFAULT '',
  gender           VARCHAR(32) NOT NULL DEFAULT '',
  court            VARCHAR(64) NOT NULL DEFAULT '',
  email            VARCHAR(191) NULL,
  ref_id           BIGINT NULL,
  pool_no          INT NULL,
  round_no         INT NULL,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_tournament_referees_tid
  ON tournament_referees(tournament_id, category, court);

CREATE TABLE IF NOT EXISTS tournament_form_fields (
  tournament_id    BIGINT PRIMARY KEY REFERENCES tournaments(id) ON DELETE CASCADE,
  fields           JSONB NOT NULL DEFAULT '[]'::jsonb,
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS tournament_ref_tokens (
  token            VARCHAR(64) PRIMARY KEY,
  tournament_id    BIGINT NOT NULL REFERENCES tournaments(id) ON DELETE CASCADE,
  category         VARCHAR(64) NOT NULL DEFAULT '',
  gender           VARCHAR(32) NOT NULL DEFAULT '',
  court            VARCHAR(64) NOT NULL DEFAULT '',
  round_no         INT NULL,
  pool_no          INT NULL,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_tournament_ref_tokens_tid
  ON tournament_ref_tokens(tournament_id);

ALTER TABLE tournament_fixtures
  ADD COLUMN IF NOT EXISTS ref_id BIGINT NULL;

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
`)
	return err
}
