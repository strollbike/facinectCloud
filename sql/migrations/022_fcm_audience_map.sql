-- Role → login user map for FCM audience push (parity with PHP app_fcm_audience_map).
-- audience: user | member | student | management
-- role_id: app_users.id / members.id / students.id / identity users.id

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
