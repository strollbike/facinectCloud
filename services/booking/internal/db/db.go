package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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
CREATE TABLE IF NOT EXISTS booking_courts (
  id            BIGSERIAL PRIMARY KEY,
  facility_id   BIGINT NOT NULL,
  name          VARCHAR(128) NOT NULL,
  sport_id      BIGINT NULL,
  status        VARCHAR(16) NOT NULL DEFAULT 'active'
                CHECK (status IN ('active','disabled')),
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_booking_courts_facility ON booking_courts(facility_id);

CREATE TABLE IF NOT EXISTS booking_slots (
  id            BIGSERIAL PRIMARY KEY,
  facility_id   BIGINT NOT NULL,
  court_id      BIGINT NOT NULL REFERENCES booking_courts(id) ON DELETE CASCADE,
  slot_date     DATE NOT NULL,
  start_time    TIME NOT NULL,
  end_time      TIME NOT NULL,
  status        VARCHAR(16) NOT NULL DEFAULT 'available'
                CHECK (status IN ('available','booked','blocked','held')),
  notes         TEXT NULL,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (court_id, slot_date, start_time)
);
CREATE INDEX IF NOT EXISTS idx_booking_slots_facility_date
  ON booking_slots(facility_id, slot_date, status);

CREATE TABLE IF NOT EXISTS booking_payment_holds (
  id                BIGSERIAL PRIMARY KEY,
  facility_id       BIGINT NOT NULL,
  public_token      VARCHAR(64) NOT NULL UNIQUE,
  whatsapp          VARCHAR(32) NOT NULL,
  customer_name     VARCHAR(191) NULL,
  category          VARCHAR(64) NULL,
  sport_name        VARCHAR(128) NULL,
  logo_url          TEXT NULL,
  slot_ids          BIGINT[] NOT NULL,
  slots_summary     TEXT NULL,
  full_amount       NUMERIC(12,2) NOT NULL DEFAULT 0,
  pay_amount        NUMERIC(12,2) NOT NULL DEFAULT 0,
  amount_type       VARCHAR(16) NOT NULL DEFAULT 'advance'
                    CHECK (amount_type IN ('advance','full')),
  payment_url       TEXT NULL,
  provider_link_id  VARCHAR(128) NULL,
  status            VARCHAR(16) NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending','paid','expired','cancelled')),
  expires_at        TIMESTAMPTZ NOT NULL,
  paid_at           TIMESTAMPTZ NULL,
  created_by        VARCHAR(128) NULL,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_booking_payment_holds_facility
  ON booking_payment_holds(facility_id, status);
CREATE INDEX IF NOT EXISTS idx_booking_payment_holds_expires
  ON booking_payment_holds(expires_at) WHERE status='pending';

CREATE TABLE IF NOT EXISTS booking_bookings (
  id               BIGSERIAL PRIMARY KEY,
  facility_id      BIGINT NOT NULL,
  slot_id          BIGINT NOT NULL REFERENCES booking_slots(id),
  user_id          BIGINT NULL,
  customer_name    VARCHAR(191) NULL,
  customer_phone   VARCHAR(32) NULL,
  customer_email   VARCHAR(191) NULL,
  status           VARCHAR(16) NOT NULL DEFAULT 'confirmed'
                   CHECK (status IN ('confirmed','cancelled')),
  notes            TEXT NULL,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_booking_bookings_facility
  ON booking_bookings(facility_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_booking_bookings_slot ON booking_bookings(slot_id);

ALTER TABLE booking_bookings
  ADD COLUMN IF NOT EXISTS payment_status VARCHAR(32) NOT NULL DEFAULT 'unpaid';
ALTER TABLE booking_bookings
  ADD COLUMN IF NOT EXISTS pay_mode VARCHAR(64) NULL;
ALTER TABLE booking_bookings
  ADD COLUMN IF NOT EXISTS amount_paid NUMERIC(12,2) NULL;

CREATE TABLE IF NOT EXISTS booking_coupons (
  id              BIGSERIAL PRIMARY KEY,
  facility_id     BIGINT NOT NULL,
  code            VARCHAR(64) NOT NULL,
  discount        NUMERIC(12,2) NOT NULL DEFAULT 0,
  status          VARCHAR(16) NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active','used','disabled')),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (facility_id, code)
);
CREATE INDEX IF NOT EXISTS idx_booking_coupons_facility ON booking_coupons(facility_id);

CREATE TABLE IF NOT EXISTS booking_user_status (
  facility_id     BIGINT NOT NULL,
  user_id         BIGINT NOT NULL,
  status          VARCHAR(16) NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active','blocked')),
  reason          TEXT NULL,
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (facility_id, user_id)
);

CREATE TABLE IF NOT EXISTS booking_blocks (
  id              BIGSERIAL PRIMARY KEY,
  facility_id     BIGINT NOT NULL,
  court_id        BIGINT NULL REFERENCES booking_courts(id) ON DELETE SET NULL,
  block_date      DATE NOT NULL,
  start_time      TIME NOT NULL,
  end_time        TIME NOT NULL,
  reason          TEXT NULL,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_booking_blocks_facility_date
  ON booking_blocks(facility_id, block_date);

-- Permanent weekly rules (1=Mon … 7=Sun). Re-applied whenever slots are generated.
CREATE TABLE IF NOT EXISTS booking_recurring_blocks (
  id              BIGSERIAL PRIMARY KEY,
  facility_id     BIGINT NOT NULL,
  court_id        BIGINT NULL REFERENCES booking_courts(id) ON DELETE SET NULL,
  weekdays        INT[] NOT NULL,
  start_time      TIME NOT NULL,
  end_time        TIME NOT NULL,
  reason          TEXT NULL,
  active          BOOLEAN NOT NULL DEFAULT TRUE,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_booking_recurring_blocks_facility
  ON booking_recurring_blocks(facility_id) WHERE active;

-- Last manual Generate hours (used by monthly cron / fill — not default 6–22).
CREATE TABLE IF NOT EXISTS booking_facility_schedule (
  facility_id     BIGINT PRIMARY KEY,
  open_hour       INT NOT NULL DEFAULT 6,
  close_hour      INT NOT NULL DEFAULT 22,
  slot_minutes    INT NOT NULL DEFAULT 60,
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
`)
	if err != nil {
		return err
	}
	// Widen slot status to include payment-hold "held" (idempotent).
	_, _ = db.Exec(`
ALTER TABLE booking_slots DROP CONSTRAINT IF EXISTS booking_slots_status_check`)
	_, err = db.Exec(`
ALTER TABLE booking_slots
  ADD CONSTRAINT booking_slots_status_check
  CHECK (status IN ('available','booked','blocked','held'))`)
	// Constraint may already exist with same definition — ignore duplicate.
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		// try recreate only if missing
		var one int
		qerr := db.QueryRow(`
SELECT 1 FROM pg_constraint WHERE conname='booking_slots_status_check'`).Scan(&one)
		if qerr != nil {
			return err
		}
	}
	return nil
}
