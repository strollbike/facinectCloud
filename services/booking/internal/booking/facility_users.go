package booking

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// last10WhatsApp returns the last 10 digits for match (91XXXXXXXXXX ≡ XXXXXXXXXX).
func last10WhatsApp(wa string) string {
	d := normalizeWhatsApp(wa)
	if len(d) >= 10 {
		return d[len(d)-10:]
	}
	return d
}

// ensureFacilityAppUser upserts app_users (global WhatsApp OTP users) and a
// facility-scoped booking_facility_users row (PHP paid_users_{facility} parity).
// Same WhatsApp (any 91/0 prefix) never creates a duplicate — updates name instead.
func (s *Service) ensureFacilityAppUser(ctx context.Context, facilityID int64, whatsapp, fullName string) (int64, error) {
	wa := normalizeWhatsApp(whatsapp)
	if facilityID <= 0 || len(wa) < 10 {
		return 0, fmt.Errorf("invalid_customer")
	}
	name := strings.TrimSpace(fullName)
	last10 := last10WhatsApp(wa)

	// Prefer existing row matched by last-10 digits (avoids 91… vs 10-digit dupes).
	var existingID int64
	var existingName string
	err := s.DB.QueryRowContext(ctx, `
SELECT id, COALESCE(full_name,'')
FROM app_users
WHERE status='active'
  AND RIGHT(regexp_replace(whatsapp_no, '\D', '', 'g'), 10) = $1
ORDER BY id ASC
LIMIT 1`, last10).Scan(&existingID, &existingName)

	var userID int64
	if err == nil && existingID > 0 {
		// Update existing — never insert a second row for the same number.
		newName := name
		if newName == "" || strings.EqualFold(newName, "Customer") {
			newName = existingName
		}
		if newName == "" {
			newName = "Customer"
		}
		// Keep stored WhatsApp format; only refresh name + facility access.
		_, err = s.DB.ExecContext(ctx, `
UPDATE app_users SET
  full_name = $2,
  accessed_facility_ids = CASE
    WHEN $3 = ANY(accessed_facility_ids) THEN accessed_facility_ids
    ELSE array_append(accessed_facility_ids, $3)
  END,
  status = 'active',
  updated_at = NOW()
WHERE id = $1`, existingID, newName, facilityID)
		if err != nil {
			return 0, fmt.Errorf("app_users_update: %w", err)
		}
		userID = existingID
	} else if err != nil && err != sql.ErrNoRows {
		return 0, fmt.Errorf("app_users_lookup: %w", err)
	} else {
		if name == "" {
			name = "Customer"
		}
		err = s.DB.QueryRowContext(ctx, `
INSERT INTO app_users (full_name, whatsapp_no, phone, accessed_facility_ids, status)
VALUES ($1, $2, $2, ARRAY[$3]::bigint[], 'active')
ON CONFLICT (whatsapp_no) DO UPDATE SET
  full_name = CASE
    WHEN EXCLUDED.full_name <> '' AND EXCLUDED.full_name <> 'Customer'
      THEN EXCLUDED.full_name
    ELSE app_users.full_name
  END,
  phone = CASE WHEN app_users.phone = '' THEN EXCLUDED.phone ELSE app_users.phone END,
  accessed_facility_ids = CASE
    WHEN $3 = ANY(app_users.accessed_facility_ids) THEN app_users.accessed_facility_ids
    ELSE array_append(app_users.accessed_facility_ids, $3)
  END,
  status = 'active',
  updated_at = NOW()
RETURNING id`, name, wa, facilityID).Scan(&userID)
		if err != nil {
			return 0, fmt.Errorf("app_users: %w", err)
		}
	}

	displayName := name
	if displayName == "" || strings.EqualFold(displayName, "Customer") {
		_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(full_name,'') FROM app_users WHERE id=$1`, userID).Scan(&displayName)
	}
	if displayName == "" {
		displayName = "Customer"
	}

	_, err = s.DB.ExecContext(ctx, `
INSERT INTO booking_facility_users (facility_id, user_id, user_name, whatsapp_no, status, updated_at)
VALUES ($1, $2, $3, $4, 'active', NOW())
ON CONFLICT (facility_id, user_id) DO UPDATE SET
  user_name = CASE
    WHEN EXCLUDED.user_name <> '' AND EXCLUDED.user_name <> 'Customer'
      THEN EXCLUDED.user_name
    ELSE booking_facility_users.user_name
  END,
  whatsapp_no = EXCLUDED.whatsapp_no,
  status = 'active',
  updated_at = NOW()`,
		facilityID, userID, displayName, wa)
	if err != nil {
		return userID, fmt.Errorf("facility_users: %w", err)
	}
	return userID, nil
}

// LookupFacilityCustomer finds an existing customer by WhatsApp (last-10 match).
// Used by StrollX payment-link UI to autofill name — never creates a row.
func (s *Service) LookupFacilityCustomer(ctx context.Context, facilityID int64, whatsapp string) (map[string]any, error) {
	wa := normalizeWhatsApp(whatsapp)
	last10 := last10WhatsApp(wa)
	if len(last10) < 10 {
		return nil, fmt.Errorf("invalid_whatsapp")
	}

	var userID int64
	var name, storedWA string
	// Prefer facility-scoped name, else global app_users.
	err := s.DB.QueryRowContext(ctx, `
SELECT u.id, COALESCE(NULLIF(bfu.user_name,''), NULLIF(u.full_name,''), ''), u.whatsapp_no
FROM app_users u
LEFT JOIN booking_facility_users bfu
  ON bfu.user_id = u.id AND bfu.facility_id = $1
WHERE u.status = 'active'
  AND RIGHT(regexp_replace(u.whatsapp_no, '\D', '', 'g'), 10) = $2
ORDER BY CASE WHEN bfu.user_id IS NOT NULL THEN 0 ELSE 1 END, u.id ASC
LIMIT 1`, facilityID, last10).Scan(&userID, &name, &storedWA)
	if err == sql.ErrNoRows {
		return map[string]any{
			"exists": false, "user_id": 0, "full_name": "", "whatsapp": wa,
		}, nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"exists":     true,
		"user_id":    userID,
		"full_name":  name,
		"whatsapp":   storedWA,
		"facility_id": facilityID,
	}, nil
}
