package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func (s *Service) ensureNotificationSoftDelete(ctx context.Context) {
	_, _ = s.DB.ExecContext(ctx, `
ALTER TABLE app_notifications ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ NULL`)
	_, _ = s.DB.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS idx_app_notif_user_active
  ON app_notifications(user_id, created_at DESC) WHERE deleted_at IS NULL`)
	_, _ = s.DB.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS idx_app_notif_deleted_at
  ON app_notifications(deleted_at) WHERE deleted_at IS NOT NULL`)
}

func notificationIDsFromBody(body map[string]any) []int64 {
	seen := map[int64]struct{}{}
	var out []int64
	add := func(id int64) {
		if id <= 0 {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if raw, ok := body["ids"].([]any); ok {
		for _, v := range raw {
			n, _ := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(v)), 10, 64)
			add(n)
		}
	}
	if raw, ok := body["items"].([]any); ok {
		for _, row := range raw {
			m, _ := row.(map[string]any)
			if m == nil {
				continue
			}
			n, _ := strconv.ParseInt(fmt.Sprint(m["id"]), 10, 64)
			add(n)
		}
	}
	if v, ok := body["id"]; ok {
		n, _ := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(v)), 10, 64)
		add(n)
	}
	return out
}

func (s *Service) softDeleteNotifications(ctx context.Context, userID int64, ids []int64, deleteAll bool) (int, error) {
	if userID <= 0 {
		return 0, fmt.Errorf("invalid_user")
	}
	if deleteAll {
		rows, err := s.DB.QueryContext(ctx, `
SELECT id FROM app_notifications
WHERE user_id=$1 AND deleted_at IS NULL`, userID)
		if err != nil {
			return 0, err
		}
		defer rows.Close()
		var all []int64
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil && id > 0 {
				all = append(all, id)
			}
		}
		ids = all
	}
	if len(ids) == 0 {
		if deleteAll {
			return 0, nil
		}
		return 0, fmt.Errorf("ids_required")
	}
	n := 0
	for _, id := range ids {
		if blocked, _ := s.billingReminderDeleteBlocked(ctx, userID, id); blocked {
			continue
		}
		res, err := s.DB.ExecContext(ctx, `
UPDATE app_notifications SET deleted_at=NOW()
WHERE user_id=$1 AND id=$2 AND deleted_at IS NULL`, userID, id)
		if err != nil {
			return n, err
		}
		c, _ := res.RowsAffected()
		n += int(c)
	}
	return n, nil
}

// billingReminderDeleteBlocked: unpaid payment reminders must stay until paid.
func (s *Service) billingReminderDeleteBlocked(ctx context.Context, userID, notifID int64) (bool, error) {
	var ntype string
	var meta []byte
	var fid sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `
SELECT ntype, COALESCE(meta_json,'{}'::jsonb), facility_id
FROM app_notifications WHERE id=$1 AND user_id=$2 AND deleted_at IS NULL`, notifID, userID).
		Scan(&ntype, &meta, &fid)
	if err != nil {
		return false, err
	}
	if !strings.EqualFold(strings.TrimSpace(ntype), "billing_reminder") {
		return false, nil
	}
	paid := s.notificationBillingIsPaid(ctx, userID, fid, meta)
	if paid {
		_, _ = s.DB.ExecContext(ctx, `
UPDATE app_notifications
SET meta_json = COALESCE(meta_json,'{}'::jsonb) || '{"isPaid":true,"requiresPayment":false}'::jsonb
WHERE id=$1`, notifID)
		return false, nil
	}
	return true, nil
}

// notificationBillingIsPaid: true when the user has no pending dues for the reminder facility.
func (s *Service) notificationBillingIsPaid(ctx context.Context, userID int64, fid sql.NullInt64, meta []byte) bool {
	var metaMap map[string]any
	_ = json.Unmarshal(meta, &metaMap)
	if metaMap != nil {
		if metaMap["isPaid"] == true || fmt.Sprint(metaMap["isPaid"]) == "true" {
			return true
		}
		if req := strings.ToLower(strings.TrimSpace(fmt.Sprint(metaMap["requiresPayment"]))); req == "false" {
			return true
		}
	}
	facilityID := int64(0)
	if fid.Valid {
		facilityID = fid.Int64
	}
	if facilityID <= 0 && metaMap != nil {
		facilityID, _ = strconv.ParseInt(fmt.Sprint(metaMap["facilityId"]), 10, 64)
	}
	if userID <= 0 {
		return false
	}
	body := map[string]any{
		"userId": userID,
		"status": "pending",
	}
	if facilityID > 0 {
		body["facilityId"] = facilityID
	}
	if metaMap != nil {
		aud := strings.ToLower(strings.TrimSpace(fmt.Sprint(metaMap["audience"])))
		if aud == "member" || aud == "student" {
			body["type"] = aud
		}
	}
	ok, _, data := s.billingMyBills(ctx, body)
	if !ok {
		// Fail closed: treat as unpaid so reminder cannot be dismissed.
		return false
	}
	m, _ := data.(map[string]any)
	if m == nil {
		return true
	}
	pending, _ := strconv.Atoi(fmt.Sprint(m["pendingCount"]))
	return pending <= 0
}

// PurgeSoftDeletedNotifications hard-deletes soft-deleted rows older than retentionDays (default 90).
func (s *Service) PurgeSoftDeletedNotifications(ctx context.Context, retentionDays int) (int, error) {
	if retentionDays <= 0 {
		retentionDays = 90
	}
	s.ensureNotificationSoftDelete(ctx)
	res, err := s.DB.ExecContext(ctx, `
DELETE FROM app_notifications
WHERE deleted_at IS NOT NULL
  AND deleted_at < NOW() - make_interval(days => $1)`, retentionDays)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
