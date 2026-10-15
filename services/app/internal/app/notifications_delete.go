package app

import (
	"context"
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
		res, err := s.DB.ExecContext(ctx, `
UPDATE app_notifications SET deleted_at=NOW()
WHERE user_id=$1 AND deleted_at IS NULL`, userID)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		return int(n), nil
	}
	if len(ids) == 0 {
		return 0, fmt.Errorf("ids_required")
	}
	n := 0
	for _, id := range ids {
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
