-- User-level soft-delete for Proximity inbox; purge after 90 days.

ALTER TABLE app_notifications
  ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ NULL;

CREATE INDEX IF NOT EXISTS idx_app_notif_user_active
  ON app_notifications(user_id, created_at DESC)
  WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_app_notif_deleted_at
  ON app_notifications(deleted_at)
  WHERE deleted_at IS NOT NULL;
