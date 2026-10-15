package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func (s *Service) ensureMemberPlayToday(ctx context.Context) {
	_, _ = s.DB.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS member_play_today (
  membership_id BIGINT NOT NULL,
  play_date     DATE NOT NULL,
  coming        BOOLEAN NOT NULL DEFAULT TRUE,
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (membership_id, play_date)
)`)
}

func (s *Service) comingMapForMemberships(ctx context.Context, membershipIDs []int64, playDate string) map[int64]bool {
	out := map[int64]bool{}
	if len(membershipIDs) == 0 || playDate == "" {
		return out
	}
	s.ensureMemberPlayToday(ctx)
	placeholders := make([]string, len(membershipIDs))
	args := make([]any, 0, len(membershipIDs)+1)
	args = append(args, playDate)
	for i, id := range membershipIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+2)
		args = append(args, id)
	}
	q := fmt.Sprintf(`
SELECT membership_id, coming FROM member_play_today
WHERE play_date=$1::date AND membership_id IN (%s)`, strings.Join(placeholders, ","))
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var coming bool
		if err := rows.Scan(&id, &coming); err == nil && coming {
			out[id] = true
		}
	}
	return out
}

func (s *Service) primaryMembersToggleComing(ctx context.Context, body map[string]any) (bool, string, any) {
	last10, errMsg := s.resolvePortalLast10(ctx, body)
	if errMsg != "" {
		return false, errMsg, nil
	}
	mid, _ := parseInt64Field(body, "membershipId")
	planID := planIDFromBody(body)
	if mid <= 0 {
		mid = planID
	}
	if mid <= 0 {
		return false, "membershipId is required.", nil
	}

	// Viewer must own this membership (or be primary on same plan).
	myIDs, err := s.liveMyMembershipIDs(ctx, last10, 0)
	if err != nil {
		return false, err.Error(), nil
	}
	owns := false
	for _, id := range myIDs {
		if id == mid {
			owns = true
			break
		}
	}
	if !owns {
		// Allow primary on same plan to toggle? PHP: canToggleComing = isMine only.
		return false, "Membership not found for this account.", nil
	}

	playDate := time.Now().Format("2006-01-02")
	s.ensureMemberPlayToday(ctx)
	var coming bool
	err = s.DB.QueryRowContext(ctx, `
INSERT INTO member_play_today (membership_id, play_date, coming, updated_at)
VALUES ($1,$2::date, TRUE, NOW())
ON CONFLICT (membership_id, play_date) DO UPDATE
SET coming = NOT member_play_today.coming, updated_at = NOW()
RETURNING coming`, mid, playDate).Scan(&coming)
	if err != nil {
		// Fallback app_member_plans cache
		_, _ = s.DB.ExecContext(ctx, `
UPDATE app_member_plans SET coming_today = NOT coming_today WHERE membership_id=$1 OR id=$1`, mid)
		return true, "OK", map[string]any{"membershipId": mid, "playDate": playDate}
	}
	return true, "OK", map[string]any{
		"membershipId": mid, "playDate": playDate, "comingToday": coming,
	}
}

func (s *Service) enrichLivePlanMembersComing(ctx context.Context, members []map[string]any, playDate string) int {
	ids := make([]int64, 0, len(members))
	for _, m := range members {
		id, _ := strconv.ParseInt(fmt.Sprint(m["membershipId"]), 10, 64)
		if id > 0 {
			ids = append(ids, id)
		}
	}
	cmap := s.comingMapForMemberships(ctx, ids, playDate)
	coming := 0
	for i := range members {
		id, _ := strconv.ParseInt(fmt.Sprint(members[i]["membershipId"]), 10, 64)
		on := cmap[id]
		members[i]["comingToday"] = on
		if on {
			coming++
		}
		isMine, _ := members[i]["isMine"].(bool)
		members[i]["canToggleComing"] = isMine
	}
	return coming
}
