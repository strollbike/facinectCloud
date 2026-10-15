package app

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const (
	fcmAudienceUser       = "user"
	fcmAudienceMember     = "member"
	fcmAudienceStudent    = "student"
	fcmAudienceManagement = "management"
)

func (s *Service) ensureFCMAudienceMap(ctx context.Context) {
	_, _ = s.DB.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS app_fcm_audience_map (
  id             BIGSERIAL PRIMARY KEY,
  audience       VARCHAR(32) NOT NULL,
  role_id        BIGINT NOT NULL,
  users_user_id  BIGINT NOT NULL,
  phone_norm     VARCHAR(20),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (audience, role_id)
)`)
	_, _ = s.DB.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_fcm_aud_user ON app_fcm_audience_map(users_user_id)`)
	_, _ = s.DB.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_fcm_aud_phone ON app_fcm_audience_map(phone_norm)`)
	_, _ = s.DB.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_fcm_aud_audience ON app_fcm_audience_map(audience, role_id)`)
}

func (s *Service) upsertFCMAudience(ctx context.Context, audience string, roleID, usersUserID int64, phoneNorm string) {
	if roleID <= 0 || usersUserID <= 0 {
		return
	}
	audience = strings.ToLower(strings.TrimSpace(audience))
	if audience == "" {
		audience = fcmAudienceUser
	}
	phoneNorm = digitsOnly(phoneNorm)
	if len(phoneNorm) > 20 {
		phoneNorm = phoneNorm[:20]
	}
	var phone any
	if phoneNorm != "" {
		phone = phoneNorm
	}
	_, _ = s.DB.ExecContext(ctx, `
INSERT INTO app_fcm_audience_map (audience, role_id, users_user_id, phone_norm, updated_at)
VALUES ($1,$2,$3,$4,NOW())
ON CONFLICT (audience, role_id) DO UPDATE SET
  users_user_id = EXCLUDED.users_user_id,
  phone_norm = COALESCE(EXCLUDED.phone_norm, app_fcm_audience_map.phone_norm),
  updated_at = NOW()`,
		audience, roleID, usersUserID, phone)
}

func (s *Service) audienceMapCount(ctx context.Context, userID int64) int {
	if userID <= 0 {
		return 0
	}
	var n int
	err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM app_fcm_audience_map WHERE users_user_id=$1`, userID,
	).Scan(&n)
	if err != nil {
		return 0
	}
	return n
}

// syncAudienceForUser maps member/student/management roles → login userId by WhatsApp last-10.
func (s *Service) syncAudienceForUser(ctx context.Context, userID int64) {
	if userID <= 0 {
		return
	}
	s.ensureFCMAudienceMap(ctx)

	var wa, phone sql.NullString
	err := s.DB.QueryRowContext(ctx,
		`SELECT whatsapp_no, phone FROM app_users WHERE id=$1 AND status='active' LIMIT 1`, userID,
	).Scan(&wa, &phone)
	if err != nil {
		return
	}
	raw := strings.TrimSpace(wa.String)
	if raw == "" {
		raw = strings.TrimSpace(phone.String)
	}
	norm := normalizeWA(raw)
	last10 := last10Digits(norm)
	if len(last10) < 10 {
		return
	}
	s.upsertFCMAudience(ctx, fcmAudienceUser, userID, userID, norm)

	// Members
	rows, err := s.DB.QueryContext(ctx, fmt.Sprintf(`
SELECT id FROM members
WHERE %s = $1 AND status='active'
LIMIT 50`, phoneLast10SQL("COALESCE(NULLIF(whatsapp,''), NULLIF(contact_phone,''))")), last10)
	if err == nil {
		for rows.Next() {
			var mid int64
			if rows.Scan(&mid) == nil && mid > 0 {
				s.upsertFCMAudience(ctx, fcmAudienceMember, mid, userID, norm)
			}
		}
		rows.Close()
	}

	// Students
	rows, err = s.DB.QueryContext(ctx, fmt.Sprintf(`
SELECT id FROM students
WHERE (%s = $1 OR %s = $1) AND status='active'
LIMIT 50`,
		phoneLast10SQL("COALESCE(NULLIF(whatsapp,''), NULLIF(contact_phone,''))"),
		phoneLast10SQL("contact_phone"),
	), last10)
	if err == nil {
		for rows.Next() {
			var sid int64
			if rows.Scan(&sid) == nil && sid > 0 {
				s.upsertFCMAudience(ctx, fcmAudienceStudent, sid, userID, norm)
			}
		}
		rows.Close()
	}

	// Facility staff (identity users) — same WhatsApp may use Proximity
	rows, err = s.DB.QueryContext(ctx, fmt.Sprintf(`
SELECT u.id
FROM users u
JOIN user_facility_memberships m ON m.user_id = u.id AND m.status='active'
WHERE u.status='active' AND %s = $1
LIMIT 50`, phoneLast10SQL("u.phone_e164")), last10)
	if err == nil {
		for rows.Next() {
			var uid int64
			if rows.Scan(&uid) == nil && uid > 0 {
				s.upsertFCMAudience(ctx, fcmAudienceManagement, uid, userID, norm)
			}
		}
		rows.Close()
	}
}

func parseSyncRoles(body map[string]any) bool {
	v, ok := body["syncRoles"]
	if !ok || v == nil {
		return true
	}
	switch t := v.(type) {
	case bool:
		return t
	default:
		s := strings.ToLower(strings.TrimSpace(fmt.Sprint(t)))
		return s != "false" && s != "0" && s != "no"
	}
}
