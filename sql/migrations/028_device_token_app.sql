-- Separate Proximity and StrollX FCM tokens that share a numeric user id.
ALTER TABLE device_tokens ADD COLUMN IF NOT EXISTS app VARCHAR(16) NOT NULL DEFAULT 'proximity';
