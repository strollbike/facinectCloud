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
CREATE TABLE IF NOT EXISTS notification_templates (
  id            BIGSERIAL PRIMARY KEY,
  facility_id   BIGINT NULL,
  template_key  VARCHAR(64) NOT NULL,
  channel       VARCHAR(16) NOT NULL DEFAULT 'whatsapp'
                CHECK (channel IN ('whatsapp','push','both')),
  wa_template   VARCHAR(128) NULL,
  push_title    VARCHAR(191) NULL,
  push_body     TEXT NULL,
  status        VARCHAR(16) NOT NULL DEFAULT 'active'
                CHECK (status IN ('active','disabled')),
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (facility_id, template_key, channel)
);

CREATE TABLE IF NOT EXISTS notification_jobs (
  id            BIGSERIAL PRIMARY KEY,
  facility_id   BIGINT NULL,
  channel       VARCHAR(16) NOT NULL
                CHECK (channel IN ('whatsapp','push','both')),
  template_key  VARCHAR(64) NULL,
  to_whatsapp   VARCHAR(32) NULL,
  to_user_id    BIGINT NULL,
  payload_json  JSONB NOT NULL DEFAULT '{}'::jsonb,
  status        VARCHAR(16) NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending','processing','sent','failed','dry_run')),
  provider_ref  VARCHAR(191) NULL,
  error_text    TEXT NULL,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_notification_jobs_facility ON notification_jobs(facility_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_notification_jobs_status ON notification_jobs(status, created_at DESC);

CREATE TABLE IF NOT EXISTS device_tokens (
  id            BIGSERIAL PRIMARY KEY,
  user_id       BIGINT NOT NULL,
  token         TEXT NOT NULL,
  platform      VARCHAR(16) NOT NULL DEFAULT 'android'
                CHECK (platform IN ('android','ios','web')),
  status        VARCHAR(16) NOT NULL DEFAULT 'active'
                CHECK (status IN ('active','disabled')),
  app           VARCHAR(16) NOT NULL DEFAULT 'proximity'
                CHECK (app IN ('proximity','strollx')),
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (user_id, token)
);
CREATE INDEX IF NOT EXISTS idx_device_tokens_user ON device_tokens(user_id);
ALTER TABLE device_tokens ADD COLUMN IF NOT EXISTS app VARCHAR(16) NOT NULL DEFAULT 'proximity';

CREATE TABLE IF NOT EXISTS facility_whatsapp_config (
  facility_id            BIGINT PRIMARY KEY,
  meta_phone_number_id   VARCHAR(64) NULL,
  whatsapp_api_token     VARCHAR(512) NULL,
  waba_id                VARCHAR(64) NULL,
  whatsapp_enabled       BOOLEAN NOT NULL DEFAULT TRUE,
  created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_facility_wa_phone
  ON facility_whatsapp_config(meta_phone_number_id)
  WHERE meta_phone_number_id IS NOT NULL AND meta_phone_number_id <> '';

CREATE TABLE IF NOT EXISTS notification_log (
  id              BIGSERIAL PRIMARY KEY,
  job_id          BIGINT NULL,
  facility_id     BIGINT NULL,
  user_id         BIGINT NULL,
  channel         VARCHAR(16) NOT NULL DEFAULT 'push',
  template_key    VARCHAR(64) NULL,
  title           VARCHAR(191) NULL,
  body            TEXT NULL,
  to_whatsapp     VARCHAR(32) NULL,
  status          VARCHAR(16) NOT NULL DEFAULT 'sent',
  meta_json       JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  seen_at         TIMESTAMPTZ NULL
);
CREATE INDEX IF NOT EXISTS idx_notification_log_facility
  ON notification_log(facility_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_notification_log_user
  ON notification_log(user_id, created_at DESC);
`)
	return err
}
