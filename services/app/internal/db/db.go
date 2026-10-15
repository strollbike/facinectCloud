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
	db.SetMaxOpenConns(15)
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
CREATE TABLE IF NOT EXISTS app_users (
  id                    BIGSERIAL PRIMARY KEY,
  full_name             VARCHAR(191) NOT NULL DEFAULT '',
  whatsapp_no           VARCHAR(32) NOT NULL,
  phone                 VARCHAR(32) NOT NULL DEFAULT '',
  email                 VARCHAR(191) NOT NULL DEFAULT '',
  accessed_facility_ids BIGINT[] NOT NULL DEFAULT '{}',
  status                VARCHAR(16) NOT NULL DEFAULT 'active',
  created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (whatsapp_no)
);
CREATE INDEX IF NOT EXISTS idx_app_users_wa ON app_users(whatsapp_no);

CREATE TABLE IF NOT EXISTS app_sports (
  id         BIGSERIAL PRIMARY KEY,
  name       VARCHAR(128) NOT NULL UNIQUE,
  status     VARCHAR(16) NOT NULL DEFAULT 'active'
);

CREATE TABLE IF NOT EXISTS app_cities (
  id         BIGSERIAL PRIMARY KEY,
  name       VARCHAR(128) NOT NULL,
  state_name VARCHAR(128) NOT NULL DEFAULT '',
  latitude   DOUBLE PRECISION NOT NULL DEFAULT 0,
  longitude  DOUBLE PRECISION NOT NULL DEFAULT 0,
  geohash    VARCHAR(12) NOT NULL DEFAULT '',
  geohash5   CHAR(5) NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS app_areas (
  id         BIGSERIAL PRIMARY KEY,
  city_id    BIGINT NOT NULL REFERENCES app_cities(id) ON DELETE CASCADE,
  name       VARCHAR(128) NOT NULL,
  latitude   DOUBLE PRECISION NOT NULL DEFAULT 0,
  longitude  DOUBLE PRECISION NOT NULL DEFAULT 0,
  geohash    VARCHAR(12) NOT NULL DEFAULT '',
  geohash5   CHAR(5) NOT NULL DEFAULT '',
  aliases    TEXT[] NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS app_facilities (
  id              BIGSERIAL PRIMARY KEY,
  name            VARCHAR(191) NOT NULL,
  location        VARCHAR(255) NOT NULL DEFAULT '',
  city_name       VARCHAR(128) NOT NULL DEFAULT '',
  logo_url        TEXT NOT NULL DEFAULT '',
  latitude        DOUBLE PRECISION NOT NULL DEFAULT 0,
  longitude       DOUBLE PRECISION NOT NULL DEFAULT 0,
  geohash         VARCHAR(12) NOT NULL DEFAULT '',
  geohash5        CHAR(5) NOT NULL DEFAULT '',
  customer_ok     BOOLEAN NOT NULL DEFAULT TRUE,
  maintenance     BOOLEAN NOT NULL DEFAULT FALSE,
  booking_blocked BOOLEAN NOT NULL DEFAULT FALSE,
  status          VARCHAR(16) NOT NULL DEFAULT 'active',
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS app_facility_sports (
  facility_id BIGINT NOT NULL REFERENCES app_facilities(id) ON DELETE CASCADE,
  sport_id    BIGINT NOT NULL REFERENCES app_sports(id) ON DELETE CASCADE,
  PRIMARY KEY (facility_id, sport_id)
);

CREATE TABLE IF NOT EXISTS app_price_list (
  id              BIGSERIAL PRIMARY KEY,
  facility_id     BIGINT NOT NULL,
  sport_id        BIGINT NOT NULL,
  court_name      VARCHAR(128) NOT NULL DEFAULT 'Court 1',
  day_type        VARCHAR(32) NOT NULL DEFAULT 'weekday',
  price           NUMERIC(12,2) NOT NULL DEFAULT 500,
  advance_amount  NUMERIC(12,2) NOT NULL DEFAULT 100
);

CREATE TABLE IF NOT EXISTS app_slots (
  id              BIGSERIAL PRIMARY KEY,
  facility_id     BIGINT NOT NULL,
  sport_id        BIGINT NOT NULL,
  court_name      VARCHAR(128) NOT NULL,
  slot_date       DATE NOT NULL,
  time_range      VARCHAR(64) NOT NULL,
  status          VARCHAR(32) NOT NULL DEFAULT 'available',
  held_by_user_id BIGINT NULL,
  onhold_update_time TIMESTAMPTZ NULL,
  UNIQUE (facility_id, sport_id, court_name, slot_date, time_range)
);
ALTER TABLE app_slots ADD COLUMN IF NOT EXISTS onhold_update_time TIMESTAMPTZ NULL;
CREATE INDEX IF NOT EXISTS idx_app_slots_lookup
  ON app_slots(facility_id, sport_id, slot_date);
CREATE INDEX IF NOT EXISTS idx_app_slots_onhold
  ON app_slots(facility_id, status, onhold_update_time)
  WHERE LOWER(status)='onhold';

CREATE TABLE IF NOT EXISTS app_bookings (
  id                BIGSERIAL PRIMARY KEY,
  facility_id       BIGINT NOT NULL,
  sport_id          BIGINT NOT NULL DEFAULT 0,
  user_id           BIGINT NOT NULL,
  status            VARCHAR(32) NOT NULL DEFAULT 'confirmed',
  payment_mode      VARCHAR(64) NOT NULL DEFAULT '',
  payment_method    VARCHAR(64) NOT NULL DEFAULT '',
  transaction_id    VARCHAR(128) NOT NULL DEFAULT '',
  amount            NUMERIC(12,2) NOT NULL DEFAULT 0,
  notes             TEXT NOT NULL DEFAULT '',
  slots_json        JSONB NOT NULL DEFAULT '[]',
  formatted_time    VARCHAR(128) NOT NULL DEFAULT '',
  formatted_date    VARCHAR(64) NOT NULL DEFAULT '',
  raw_timestamp     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  court_names       TEXT NOT NULL DEFAULT '',
  created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  cancelled_at      TIMESTAMPTZ NULL
);
CREATE INDEX IF NOT EXISTS idx_app_bookings_user ON app_bookings(user_id, raw_timestamp DESC);

CREATE TABLE IF NOT EXISTS app_coupons (
  id           BIGSERIAL PRIMARY KEY,
  facility_id  BIGINT NOT NULL,
  code         VARCHAR(64) NOT NULL,
  value        NUMERIC(12,2) NOT NULL DEFAULT 0,
  ctype        VARCHAR(32) NOT NULL DEFAULT 'flat',
  label        VARCHAR(128) NOT NULL DEFAULT '',
  description  TEXT NOT NULL DEFAULT '',
  status       VARCHAR(16) NOT NULL DEFAULT 'active',
  UNIQUE (facility_id, code)
);

CREATE TABLE IF NOT EXISTS app_user_coupons (
  id            BIGSERIAL PRIMARY KEY,
  user_id       BIGINT NOT NULL,
  facility_id   BIGINT NOT NULL,
  coupon_code   VARCHAR(64) NOT NULL,
  amount        NUMERIC(12,2) NOT NULL DEFAULT 0,
  expiry_date   DATE NULL,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS app_feedback (
  id           BIGSERIAL PRIMARY KEY,
  facility_id  BIGINT NOT NULL,
  user_id      BIGINT NULL,
  rating       INT NOT NULL CHECK (rating BETWEEN 1 AND 5),
  message      TEXT NOT NULL DEFAULT '',
  whatsapp_no  VARCHAR(32) NOT NULL DEFAULT '',
  created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS app_fcm_devices (
  id         BIGSERIAL PRIMARY KEY,
  user_id    BIGINT NOT NULL,
  token      TEXT NOT NULL,
  platform   VARCHAR(32) NOT NULL DEFAULT 'android',
  device_id  VARCHAR(128) NOT NULL DEFAULT '',
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (token)
);

CREATE TABLE IF NOT EXISTS app_fcm_audience_map (
  id             BIGSERIAL PRIMARY KEY,
  audience       VARCHAR(32) NOT NULL,
  role_id        BIGINT NOT NULL,
  users_user_id  BIGINT NOT NULL,
  phone_norm     VARCHAR(20),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (audience, role_id)
);
CREATE INDEX IF NOT EXISTS idx_fcm_aud_user ON app_fcm_audience_map(users_user_id);
CREATE INDEX IF NOT EXISTS idx_fcm_aud_phone ON app_fcm_audience_map(phone_norm);
CREATE INDEX IF NOT EXISTS idx_fcm_aud_audience ON app_fcm_audience_map(audience, role_id);

CREATE TABLE IF NOT EXISTS app_notifications (
  id           BIGSERIAL PRIMARY KEY,
  user_id      BIGINT NOT NULL,
  facility_id  BIGINT NULL,
  booking_id   VARCHAR(64) NULL,
  ntype        VARCHAR(64) NOT NULL DEFAULT 'update',
  title        VARCHAR(255) NOT NULL DEFAULT '',
  body         TEXT NOT NULL DEFAULT '',
  facility_name VARCHAR(191) NOT NULL DEFAULT '',
  facility_logo TEXT NOT NULL DEFAULT '',
  seen         BOOLEAN NOT NULL DEFAULT FALSE,
  source       VARCHAR(32) NOT NULL DEFAULT 'user',
  meta_json    JSONB NOT NULL DEFAULT '{}',
  created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  expires_at   TIMESTAMPTZ NULL,
  deleted_at   TIMESTAMPTZ NULL
);
CREATE INDEX IF NOT EXISTS idx_app_notif_user ON app_notifications(user_id, created_at DESC);
ALTER TABLE app_notifications ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ NULL;

-- Geohash for proximity Nearby (precision 6 + coarse 5).
ALTER TABLE app_facilities ADD COLUMN IF NOT EXISTS geohash VARCHAR(12) NOT NULL DEFAULT '';
ALTER TABLE app_facilities ADD COLUMN IF NOT EXISTS geohash5 CHAR(5) NOT NULL DEFAULT '';
ALTER TABLE app_areas ADD COLUMN IF NOT EXISTS geohash VARCHAR(12) NOT NULL DEFAULT '';
ALTER TABLE app_areas ADD COLUMN IF NOT EXISTS geohash5 CHAR(5) NOT NULL DEFAULT '';
ALTER TABLE app_cities ADD COLUMN IF NOT EXISTS geohash VARCHAR(12) NOT NULL DEFAULT '';
ALTER TABLE app_cities ADD COLUMN IF NOT EXISTS geohash5 CHAR(5) NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_app_facilities_geohash5 ON app_facilities (geohash5);
CREATE INDEX IF NOT EXISTS idx_app_facilities_geohash ON app_facilities (geohash);
CREATE INDEX IF NOT EXISTS idx_app_areas_geohash5 ON app_areas (geohash5);
CREATE INDEX IF NOT EXISTS idx_app_notif_user_active
  ON app_notifications(user_id, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_app_notif_deleted_at
  ON app_notifications(deleted_at) WHERE deleted_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS app_score_follows (
  id          BIGSERIAL PRIMARY KEY,
  user_id     BIGINT NOT NULL,
  tour_id     BIGINT NOT NULL DEFAULT 0,
  match_id    BIGINT NOT NULL DEFAULT 0,
  target_type VARCHAR(32) NOT NULL DEFAULT 'match',
  target_key  VARCHAR(128) NOT NULL DEFAULT '',
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS app_score_prefs (
  user_id         BIGINT PRIMARY KEY,
  my_matches      BOOLEAN NOT NULL DEFAULT TRUE,
  friend_matches  BOOLEAN NOT NULL DEFAULT TRUE
);

CREATE TABLE IF NOT EXISTS app_fee_bills (
  id              BIGSERIAL PRIMARY KEY,
  user_id         BIGINT NOT NULL,
  facility_id     BIGINT NOT NULL,
  enrollment_id   BIGINT NOT NULL DEFAULT 0,
  bill_type       VARCHAR(32) NOT NULL DEFAULT 'student',
  bill_amount     NUMERIC(12,2) NOT NULL DEFAULT 0,
  paid_amount     NUMERIC(12,2) NOT NULL DEFAULT 0,
  billing_month   VARCHAR(16) NOT NULL DEFAULT '',
  due_date        DATE NULL,
  payment_status  VARCHAR(32) NOT NULL DEFAULT 'pending',
  plan_name       VARCHAR(128) NOT NULL DEFAULT '',
  display_name    VARCHAR(191) NOT NULL DEFAULT '',
  transaction_id  VARCHAR(128) NOT NULL DEFAULT '',
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS fee_bills (
  id              BIGSERIAL PRIMARY KEY,
  facility_id     BIGINT NOT NULL,
  user_id         BIGINT NOT NULL DEFAULT 0,
  contact_phone   VARCHAR(32) NOT NULL DEFAULT '',
  bill_type       VARCHAR(32) NOT NULL DEFAULT 'student',
  enrollment_id   BIGINT NOT NULL DEFAULT 0,
  bill_amount     NUMERIC(12,2) NOT NULL DEFAULT 0,
  paid_amount     NUMERIC(12,2) NOT NULL DEFAULT 0,
  billing_month   VARCHAR(16) NOT NULL DEFAULT '',
  due_date        DATE NULL,
  payment_status  VARCHAR(32) NOT NULL DEFAULT 'pending',
  plan_name       VARCHAR(128) NOT NULL DEFAULT '',
  display_name    VARCHAR(191) NOT NULL DEFAULT '',
  transaction_id  VARCHAR(128) NOT NULL DEFAULT '',
  payment_method  VARCHAR(32) NOT NULL DEFAULT '',
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS app_student_enrollments (
  id            BIGSERIAL PRIMARY KEY,
  user_id       BIGINT NOT NULL,
  facility_id   BIGINT NOT NULL,
  student_id    BIGINT NOT NULL DEFAULT 0,
  student_name  VARCHAR(191) NOT NULL DEFAULT '',
  level         VARCHAR(64) NOT NULL DEFAULT '',
  status        VARCHAR(32) NOT NULL DEFAULT 'active',
  plan_id       BIGINT NOT NULL DEFAULT 0,
  plan_name     VARCHAR(128) NOT NULL DEFAULT '',
  sport_id      BIGINT NOT NULL DEFAULT 0,
  sport_name    VARCHAR(128) NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS app_student_attendance (
  id             BIGSERIAL PRIMARY KEY,
  enrollment_id  BIGINT NOT NULL,
  attend_date    DATE NOT NULL,
  status         VARCHAR(32) NOT NULL DEFAULT 'present',
  leave_reason   TEXT NOT NULL DEFAULT '',
  UNIQUE (enrollment_id, attend_date)
);

CREATE TABLE IF NOT EXISTS app_member_plans (
  id                 BIGSERIAL PRIMARY KEY,
  user_id            BIGINT NOT NULL,
  membership_id      BIGINT NOT NULL DEFAULT 0,
  member_id          BIGINT NOT NULL DEFAULT 0,
  member_name        VARCHAR(191) NOT NULL DEFAULT '',
  plan_id            BIGINT NOT NULL DEFAULT 0,
  facility_id        BIGINT NOT NULL,
  sport_id           BIGINT NOT NULL DEFAULT 0,
  sport_name         VARCHAR(128) NOT NULL DEFAULT '',
  plan_name          VARCHAR(128) NOT NULL DEFAULT '',
  sub_facility_name  VARCHAR(128) NOT NULL DEFAULT '',
  slot_time          VARCHAR(64) NOT NULL DEFAULT '',
  indiv_price        NUMERIC(12,2) NOT NULL DEFAULT 0,
  grp_price          NUMERIC(12,2) NOT NULL DEFAULT 0,
  subscription_type  VARCHAR(64) NOT NULL DEFAULT 'individual',
  max_member_count   INT NOT NULL DEFAULT 1,
  current_member_count INT NOT NULL DEFAULT 1,
  team_name          VARCHAR(128) NOT NULL DEFAULT '',
  subscription_fee   NUMERIC(12,2) NOT NULL DEFAULT 0,
  is_primary         BOOLEAN NOT NULL DEFAULT TRUE,
  whatsapp_number    VARCHAR(32) NOT NULL DEFAULT '',
  coming_today       BOOLEAN NOT NULL DEFAULT FALSE,
  status_label       VARCHAR(64) NOT NULL DEFAULT 'active'
);

CREATE TABLE IF NOT EXISTS app_live_tournaments (
  id               BIGSERIAL PRIMARY KEY,
  tournament_name  VARCHAR(191) NOT NULL,
  tournament_date  DATE NOT NULL DEFAULT CURRENT_DATE,
  facility_id      BIGINT NOT NULL DEFAULT 1,
  sport_name       VARCHAR(128) NOT NULL DEFAULT 'Badminton',
  categories       TEXT[] NOT NULL DEFAULT '{Open}',
  open_match_count INT NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS app_live_matches (
  id            BIGSERIAL PRIMARY KEY,
  tour_id       BIGINT NOT NULL REFERENCES app_live_tournaments(id) ON DELETE CASCADE,
  category      VARCHAR(64) NOT NULL DEFAULT 'Open',
  gender        VARCHAR(32) NOT NULL DEFAULT '',
  player1       VARCHAR(191) NOT NULL DEFAULT '',
  player2       VARCHAR(191) NOT NULL DEFAULT '',
  status        VARCHAR(32) NOT NULL DEFAULT 'live',
  winner        VARCHAR(191) NOT NULL DEFAULT '',
  court         VARCHAR(64) NOT NULL DEFAULT '',
  score1        INT NOT NULL DEFAULT 0,
  score2        INT NOT NULL DEFAULT 0,
  version       BIGINT NOT NULL DEFAULT 1,
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
`)
	return err
}
