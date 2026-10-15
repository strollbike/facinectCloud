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
CREATE TABLE IF NOT EXISTS members (
  id              BIGSERIAL PRIMARY KEY,
  full_name       VARCHAR(191) NOT NULL,
  contact_email   VARCHAR(191) NULL,
  contact_phone   VARCHAR(32) NULL,
  whatsapp        VARCHAR(32) NULL,
  status          VARCHAR(16) NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active','inactive')),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_members_phone ON members(contact_phone);
CREATE INDEX IF NOT EXISTS idx_members_email ON members(contact_email);

CREATE TABLE IF NOT EXISTS memberships (
  id                 BIGSERIAL PRIMARY KEY,
  facility_id        BIGINT NOT NULL,
  member_id          BIGINT NOT NULL REFERENCES members(id) ON DELETE CASCADE,
  sport_id           BIGINT NULL,
  plan_name          VARCHAR(128) NULL,
  team_name          VARCHAR(128) NULL,
  start_date         DATE NULL,
  end_date           DATE NULL,
  subscription_fee   NUMERIC(12,2) NULL,
  primary_member     BOOLEAN NOT NULL DEFAULT FALSE,
  status             VARCHAR(16) NOT NULL DEFAULT 'active'
                     CHECK (status IN ('active','inactive','expired')),
  created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_memberships_facility
  ON memberships(facility_id, status);
CREATE INDEX IF NOT EXISTS idx_memberships_member
  ON memberships(member_id);

ALTER TABLE memberships
  ADD COLUMN IF NOT EXISTS payment_status VARCHAR(32) NOT NULL DEFAULT 'unpaid';
ALTER TABLE memberships
  ADD COLUMN IF NOT EXISTS plan_id BIGINT NULL;
ALTER TABLE memberships
  ADD COLUMN IF NOT EXISTS request_status VARCHAR(32) NOT NULL DEFAULT '';
ALTER TABLE memberships
  ADD COLUMN IF NOT EXISTS request_box TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS member_plans (
  id              BIGSERIAL PRIMARY KEY,
  facility_id     BIGINT NOT NULL,
  name            VARCHAR(128) NOT NULL,
  price           NUMERIC(12,2) NULL,
  duration_days   INT NULL,
  status          VARCHAR(16) NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active','inactive')),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_member_plans_facility ON member_plans(facility_id, status);

CREATE TABLE IF NOT EXISTS member_plan_settings (
  plan_id         BIGINT PRIMARY KEY REFERENCES member_plans(id) ON DELETE CASCADE,
  facility_id     BIGINT NOT NULL,
  settings_json   JSONB NOT NULL DEFAULT '{}'::jsonb,
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS member_requests (
  id              BIGSERIAL PRIMARY KEY,
  facility_id     BIGINT NOT NULL,
  member_id       BIGINT NULL,
  membership_id   BIGINT NULL,
  full_name       VARCHAR(191) NULL,
  contact_phone   VARCHAR(32) NULL,
  request_box     TEXT NULL,
  status          VARCHAR(16) NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending','approved','rejected')),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_member_requests_facility
  ON member_requests(facility_id, status, created_at DESC);

CREATE TABLE IF NOT EXISTS member_coupons (
  id              BIGSERIAL PRIMARY KEY,
  facility_id     BIGINT NOT NULL,
  plan_id         BIGINT NULL,
  code            VARCHAR(64) NOT NULL,
  status          VARCHAR(16) NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active','used','disabled')),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (facility_id, code)
);
CREATE INDEX IF NOT EXISTS idx_member_coupons_facility ON member_coupons(facility_id);

CREATE TABLE IF NOT EXISTS member_open_slots (
  id              BIGSERIAL PRIMARY KEY,
  facility_id     BIGINT NOT NULL,
  sport_id        BIGINT NULL,
  sub_facility    VARCHAR(128) NULL,
  slot_time       VARCHAR(64) NULL,
  from_date       DATE NULL,
  to_date         DATE NULL,
  status          VARCHAR(16) NOT NULL DEFAULT 'open'
                  CHECK (status IN ('open','closed')),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_member_open_slots_facility ON member_open_slots(facility_id);
`)
	return err
}
