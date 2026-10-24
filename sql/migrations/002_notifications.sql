-- Notifications tables (shared fac_identity DB for N1; can split later)
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
