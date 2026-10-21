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
CREATE TABLE IF NOT EXISTS platform_onboarding_requests (
  id               BIGSERIAL PRIMARY KEY,
  kind             VARCHAR(32) NOT NULL,
  email            VARCHAR(191) NULL,
  whatsapp_number  VARCHAR(64) NULL,
  payload          JSONB NOT NULL DEFAULT '{}'::jsonb,
  status           VARCHAR(32) NOT NULL DEFAULT 'approved',
  created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_platform_onboarding_kind
  ON platform_onboarding_requests(kind, created_at DESC);

CREATE TABLE IF NOT EXISTS platform_alert_contacts (
  id          BIGSERIAL PRIMARY KEY,
  purpose     VARCHAR(64) NOT NULL DEFAULT 'onboarding_pending',
  whatsapp    VARCHAR(32) NOT NULL,
  active      BOOLEAN NOT NULL DEFAULT TRUE,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (purpose, whatsapp)
);

CREATE TABLE IF NOT EXISTS platform_onboarding_drafts (
  id               BIGSERIAL PRIMARY KEY,
  email            VARCHAR(191) NOT NULL,
  whatsapp_number  VARCHAR(64) NULL,
  draft            JSONB NOT NULL DEFAULT '{}'::jsonb,
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (email)
);

CREATE TABLE IF NOT EXISTS platform_news (
  id               BIGSERIAL PRIMARY KEY,
  title            VARCHAR(255) NOT NULL,
  body             TEXT NULL,
  audience         VARCHAR(64) NOT NULL DEFAULT 'all',
  facility_id      BIGINT NULL,
  published_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_platform_news_audience
  ON platform_news(audience, published_at DESC);

CREATE TABLE IF NOT EXISTS platform_news_inbox (
  id               BIGSERIAL PRIMARY KEY,
  news_id          BIGINT NOT NULL REFERENCES platform_news(id) ON DELETE CASCADE,
  email            VARCHAR(191) NULL,
  user_key         VARCHAR(191) NULL,
  saved_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_platform_news_inbox_user
  ON platform_news_inbox(user_key, saved_at DESC);

CREATE TABLE IF NOT EXISTS platform_organisation_profiles (
  id               BIGSERIAL PRIMARY KEY,
  email            VARCHAR(191) NOT NULL UNIQUE,
  name             VARCHAR(191) NULL,
  profile          JSONB NOT NULL DEFAULT '{}'::jsonb,
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS platform_privacy (
  id               BIGSERIAL PRIMARY KEY,
  facility_id      BIGINT NULL,
  email            VARCHAR(191) NULL,
  action           VARCHAR(64) NOT NULL,
  payload          JSONB NOT NULL DEFAULT '{}'::jsonb,
  accepted         BOOLEAN NOT NULL DEFAULT false,
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_platform_privacy_facility
  ON platform_privacy(facility_id, action);

CREATE TABLE IF NOT EXISTS platform_controls_version (
  facility_id      BIGINT PRIMARY KEY,
  version          BIGINT NOT NULL DEFAULT 1,
  pages_enabled    JSONB NOT NULL DEFAULT '[]'::jsonb,
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS platform_billing_orders (
  id                 BIGSERIAL PRIMARY KEY,
  facility_id        BIGINT NULL,
  organisation_id    BIGINT NULL,
  plan_code          VARCHAR(64) NOT NULL,
  provider           VARCHAR(32) NOT NULL DEFAULT 'stub',
  external_order_id  VARCHAR(128) NOT NULL,
  amount_paise       BIGINT NOT NULL DEFAULT 0,
  currency           VARCHAR(8) NOT NULL DEFAULT 'INR',
  status             VARCHAR(32) NOT NULL DEFAULT 'created',
  payment_id         VARCHAR(128) NULL,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  paid_at            TIMESTAMPTZ NULL
);
CREATE INDEX IF NOT EXISTS idx_platform_billing_orders_ext
  ON platform_billing_orders(external_order_id);
CREATE INDEX IF NOT EXISTS idx_platform_billing_orders_fid
  ON platform_billing_orders(facility_id, created_at DESC);

CREATE TABLE IF NOT EXISTS platform_billing_entitlements (
  facility_id        BIGINT PRIMARY KEY,
  plan_code          VARCHAR(64) NOT NULL DEFAULT 'standard',
  standard_active    BOOLEAN NOT NULL DEFAULT true,
  paid_active        BOOLEAN NOT NULL DEFAULT true,
  source             VARCHAR(64) NOT NULL DEFAULT 'gateway',
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
`)
	return err
}
