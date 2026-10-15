package app

import (
	"context"
	"fmt"
	"time"
)

// Hold TTL matches PHP OnholdCleanup / sp_check_onhold_status (~6 minutes).
const slotHoldTTL = 6 * time.Minute

type slotKey struct {
	court string
	date  string
	time  string
	sport int64
}

func parseSlotKeys(slots []any, defaultSport int64) []slotKey {
	out := make([]slotKey, 0, len(slots))
	for _, sl := range slots {
		m, _ := sl.(map[string]any)
		if m == nil {
			continue
		}
		court := firstNonEmpty(fmt.Sprint(m["court"]), fmt.Sprint(m["courtName"]), fmt.Sprint(m["subFacilityName"]))
		date := firstNonEmpty(fmt.Sprint(m["date"]), fmt.Sprint(m["slotDate"]), fmt.Sprint(m["slot_date"]))
		tr := firstNonEmpty(fmt.Sprint(m["time"]), fmt.Sprint(m["timeRange"]), fmt.Sprint(m["time_range"]))
		sport := defaultSport
		if v, err := parseInt64(m["sportId"]); err == nil && v > 0 {
			sport = v
		}
		if court == "" || date == "" || tr == "" {
			continue
		}
		out = append(out, slotKey{court: court, date: date, time: tr, sport: sport})
	}
	return out
}

func parseInt64(v any) (int64, error) {
	switch t := v.(type) {
	case int64:
		return t, nil
	case int:
		return int64(t), nil
	case float64:
		return int64(t), nil
	default:
		var n int64
		_, err := fmt.Sscan(fmt.Sprint(v), &n)
		return n, err
	}
}

// releaseExpiredHolds turns stale onhold rows back to available (facility-wide).
func (s *Service) releaseExpiredHolds(ctx context.Context, facilityID int64) {
	if facilityID <= 0 {
		return
	}
	_, _ = s.DB.ExecContext(ctx, `
UPDATE app_slots
SET status='available', held_by_user_id=NULL, onhold_update_time=NULL
WHERE facility_id=$1
  AND LOWER(status)='onhold'
  AND (onhold_update_time IS NULL OR onhold_update_time < NOW() - INTERVAL '6 minutes')`,
		facilityID)
}

// holdSlotsForUser reserves slots for checkout (PHP sp_set_slot_onhold1 parity).
// Already held by this user is OK (refresh timer). Others' hold / booked → error.
func (s *Service) holdSlotsForUser(ctx context.Context, facilityID, sportID, userID int64, slots []any) error {
	if facilityID <= 0 || userID <= 0 {
		return fmt.Errorf("Invalid hold request.")
	}
	keys := parseSlotKeys(slots, sportID)
	if len(keys) == 0 {
		return fmt.Errorf("No valid slots to hold.")
	}
	s.releaseExpiredHolds(ctx, facilityID)

	for _, k := range keys {
		sport := k.sport
		if sport <= 0 {
			sport = sportID
		}
		res, err := s.DB.ExecContext(ctx, `
UPDATE app_slots
SET status='onhold', held_by_user_id=$6, onhold_update_time=NOW()
WHERE facility_id=$1 AND sport_id=$2
  AND court_name=$3 AND slot_date=$4::date AND time_range=$5
  AND (
    LOWER(status)='available'
    OR (LOWER(status)='onhold' AND held_by_user_id=$6)
    OR (LOWER(status)='onhold' AND (
          onhold_update_time IS NULL
          OR onhold_update_time < NOW() - INTERVAL '6 minutes'
        ))
  )`,
			facilityID, sport, k.court, k.date, k.time, userID)
		if err != nil {
			return fmt.Errorf("Slot hold failed: %v", err)
		}
		n, _ := res.RowsAffected()
		if n > 0 {
			continue
		}
		// Already ours?
		var st string
		var held int64
		qerr := s.DB.QueryRowContext(ctx, `
SELECT LOWER(status), COALESCE(held_by_user_id,0)
FROM app_slots
WHERE facility_id=$1 AND sport_id=$2
  AND court_name=$3 AND slot_date=$4::date AND time_range=$5
LIMIT 1`, facilityID, sport, k.court, k.date, k.time).Scan(&st, &held)
		if qerr == nil && st == "onhold" && held == userID {
			_, _ = s.DB.ExecContext(ctx, `
UPDATE app_slots SET onhold_update_time=NOW()
WHERE facility_id=$1 AND sport_id=$2
  AND court_name=$3 AND slot_date=$4::date AND time_range=$5`,
				facilityID, sport, k.court, k.date, k.time)
			continue
		}
		if qerr == nil && st == "booked" {
			return fmt.Errorf("Slot is already booked (%s %s).", k.court, k.time)
		}
		return fmt.Errorf("Slot is already held by another user or unavailable (%s %s).", k.court, k.time)
	}
	return nil
}

// markSlotsBookedFromKeys sets booked and clears hold metadata.
func (s *Service) markSlotsBookedFromKeys(ctx context.Context, facilityID, sportID int64, slots []any) {
	for _, k := range parseSlotKeys(slots, sportID) {
		sport := k.sport
		if sport <= 0 {
			sport = sportID
		}
		_, _ = s.DB.ExecContext(ctx, `
UPDATE app_slots
SET status='booked', held_by_user_id=NULL, onhold_update_time=NULL
WHERE facility_id=$1 AND ($2=0 OR sport_id=$2)
  AND court_name=$3 AND slot_date=$4::date AND time_range=$5`,
			facilityID, sport, k.court, k.date, k.time)
	}
}

// releaseUserHolds frees onhold rows owned by this user (checkout abandon).
func (s *Service) releaseUserHolds(ctx context.Context, facilityID, sportID, userID int64, slots []any) {
	if userID <= 0 {
		return
	}
	keys := parseSlotKeys(slots, sportID)
	if len(keys) == 0 && facilityID > 0 {
		_, _ = s.DB.ExecContext(ctx, `
UPDATE app_slots
SET status='available', held_by_user_id=NULL, onhold_update_time=NULL
WHERE facility_id=$1 AND held_by_user_id=$2 AND LOWER(status)='onhold'`,
			facilityID, userID)
		return
	}
	for _, k := range keys {
		sport := k.sport
		if sport <= 0 {
			sport = sportID
		}
		_, _ = s.DB.ExecContext(ctx, `
UPDATE app_slots
SET status='available', held_by_user_id=NULL, onhold_update_time=NULL
WHERE facility_id=$1 AND ($2=0 OR sport_id=$2)
  AND court_name=$3 AND slot_date=$4::date AND time_range=$5
  AND held_by_user_id=$6 AND LOWER(status)='onhold'`,
			facilityID, sport, k.court, k.date, k.time, userID)
	}
}
