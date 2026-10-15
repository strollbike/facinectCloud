package booking

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
)

type CollectPaymentInput struct {
	PayMode string   `json:"pay_mode"`
	Amount  *float64 `json:"amount"`
}

type RecurrenceBlockInput struct {
	CourtID    *int64   `json:"court_id"`
	Days       []string `json:"days"`     // concrete yyyy-MM-dd (optional if weekdays set)
	Weekdays   []int    `json:"weekdays"` // 1=Mon … 7=Sun — permanent weekly rule
	WeeksAhead int      `json:"weeks_ahead"`
	Start      string   `json:"start"`
	End        string   `json:"end"`
	Reason     string   `json:"reason"`
}

// BlockListItem is a single blocked slot (detail view).
type BlockListItem struct {
	ID         int64  `json:"id"`
	FacilityID int64  `json:"facility_id"`
	CourtID    int64  `json:"court_id"`
	CourtName  string `json:"court_name"`
	SlotDate   string `json:"slot_date"`
	StartTime  string `json:"start_time"`
	EndTime    string `json:"end_time"`
	Status     string `json:"status"`
	Notes      string `json:"notes"`
}

// BlockRuleItem is one Active-blocks row (plan / recurrence / one-off) — not every slot.
type BlockRuleItem struct {
	ID          int64  `json:"id"`
	FacilityID  int64  `json:"facility_id"`
	CourtID     *int64 `json:"court_id,omitempty"`
	CourtName   string `json:"court_name"`
	StartTime   string `json:"start_time"`
	EndTime     string `json:"end_time"`
	Reason      string `json:"reason"`
	Notes       string `json:"notes"`
	Category    string `json:"category"`
	Weekdays    []int  `json:"weekdays,omitempty"`
	Days        string `json:"days"` // CSV weekdays for UI (1=Mon…7=Sun)
	Date        string `json:"date,omitempty"`
	IsRecursive int    `json:"isRecursive"`
	Kind        string `json:"kind"` // recurring | one_off
	PlanRef     string `json:"plan_ref,omitempty"`
}

type CreateCouponsInput struct {
	Codes    []string `json:"codes"`
	Count    int      `json:"count"`
	Discount float64  `json:"discount"`
}

type Coupon struct {
	ID         int64   `json:"id"`
	FacilityID int64   `json:"facility_id"`
	Code       string  `json:"code"`
	Discount   float64 `json:"discount"`
	Status     string  `json:"status"`
}

type UserStatusInput struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func (s *Service) CleanupSlots(ctx context.Context, facilityID int64) (int, error) {
	if facilityID <= 0 {
		return 0, fmt.Errorf("invalid_facility")
	}
	// Free inventory only — keep booked + blocked history.
	// Past days + today's slots whose end_time has already passed.
	res, err := s.DB.ExecContext(ctx, `
DELETE FROM booking_slots s
WHERE s.facility_id=$1
  AND s.status = 'available'
  AND (
    s.slot_date < CURRENT_DATE
    OR (s.slot_date = CURRENT_DATE AND s.end_time <= CURRENT_TIME)
  )
  AND NOT EXISTS (SELECT 1 FROM booking_bookings b WHERE b.slot_id = s.id)`, facilityID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	_, _ = s.DB.ExecContext(ctx, `
DELETE FROM app_slots
WHERE facility_id=$1
  AND slot_date < CURRENT_DATE
  AND LOWER(status) = 'available'`, facilityID)
	return int(n), nil
}

func (s *Service) CreateRecurrenceBlocks(ctx context.Context, facilityID int64, in RecurrenceBlockInput) (int, error) {
	if facilityID <= 0 {
		return 0, fmt.Errorf("invalid_facility")
	}
	start := normalizeTime(in.Start)
	end := normalizeTime(in.End)
	if start == "" || end == "" {
		return 0, fmt.Errorf("start_end_required")
	}
	reason := strings.TrimSpace(in.Reason)

	weekdays := normalizeWeekdays(in.Weekdays)
	days := make([]string, 0, len(in.Days))
	for _, d := range in.Days {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		days = append(days, d)
	}

	// Permanent weekly rule — re-applied on every future slot generate.
	if len(weekdays) > 0 {
		var court interface{}
		if in.CourtID != nil && *in.CourtID > 0 {
			court = *in.CourtID
		}
		_, err := s.DB.ExecContext(ctx, `
INSERT INTO booking_recurring_blocks (facility_id, court_id, weekdays, start_time, end_time, reason, active)
VALUES ($1,$2,$3::int[],$4::time,$5::time,NULLIF($6,''),TRUE)`,
			facilityID, court, pgIntArrayLiteral(weekdays), start, end, reason)
		if err != nil {
			return 0, err
		}
		weeks := in.WeeksAhead
		if weeks <= 0 {
			weeks = 5 // ~rolling 30-day Free inventory
		}
		if weeks > 5 {
			weeks = 5
		}
		days = mergeUniqueDates(days, expandWeekdaysToDates(weekdays, weeks))
	}

	// Only materialize blocks inside today → today+29 (same as Generate).
	_, winTo := MonthWindow(time.Now())
	winToStr := winTo.Format("2006-01-02")
	todayStr := time.Now().Format("2006-01-02")
	filtered := days[:0]
	for _, d := range days {
		if d >= todayStr && d <= winToStr {
			filtered = append(filtered, d)
		}
	}
	days = filtered

	if len(days) == 0 {
		return 0, fmt.Errorf("days_required")
	}

	blocked := 0
	for _, day := range days {
		if err := validateDate(day); err != nil {
			return blocked, err
		}
		n, err := s.blockSlotsForDate(ctx, facilityID, in.CourtID, day, start, end, reason)
		if err != nil {
			return blocked, err
		}
		blocked += n
	}
	return blocked, nil
}

func (s *Service) blockSlotsForDate(
	ctx context.Context,
	facilityID int64,
	courtID *int64,
	day, start, end, reason string,
) (int, error) {
	var court interface{}
	if courtID != nil && *courtID > 0 {
		court = *courtID
	}
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO booking_blocks (facility_id, court_id, block_date, start_time, end_time, reason)
VALUES ($1,$2,$3::date,$4::time,$5::time,NULLIF($6,''))`,
		facilityID, court, day, start, end, reason)
	if err != nil {
		return 0, err
	}

	return s.updateAvailableToBlocked(ctx, facilityID, courtID, day, start, end, reason)
}

// ApplyBlocksForDate reapplies one-off + permanent weekly rules after slot generate.
func (s *Service) ApplyBlocksForDate(ctx context.Context, facilityID int64, day time.Time) (int, error) {
	if facilityID <= 0 {
		return 0, fmt.Errorf("invalid_facility")
	}
	date := day.Format("2006-01-02")
	dartWd := dartWeekday(day)
	blocked := 0

	// One-off / expanded rows for this date.
	rows, err := s.DB.QueryContext(ctx, `
SELECT court_id, start_time::text, end_time::text, COALESCE(reason,'')
FROM booking_blocks
WHERE facility_id=$1 AND block_date=$2::date`, facilityID, date)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var court sql.NullInt64
		var start, end, reason string
		if err := rows.Scan(&court, &start, &end, &reason); err != nil {
			return blocked, err
		}
		var cid *int64
		if court.Valid && court.Int64 > 0 {
			v := court.Int64
			cid = &v
		}
		n, err := s.updateAvailableToBlocked(ctx, facilityID, cid, date, normalizeTime(start), normalizeTime(end), reason)
		if err != nil {
			return blocked, err
		}
		blocked += n
	}
	if err := rows.Err(); err != nil {
		return blocked, err
	}

	// Permanent weekly rules matching this weekday (1=Mon … 7=Sun).
	rrows, err := s.DB.QueryContext(ctx, `
SELECT court_id, start_time::text, end_time::text, COALESCE(reason,'')
FROM booking_recurring_blocks
WHERE facility_id=$1 AND active=TRUE AND $2 = ANY(weekdays)`, facilityID, dartWd)
	if err != nil {
		return blocked, err
	}
	defer rrows.Close()
	for rrows.Next() {
		var court sql.NullInt64
		var start, end, reason string
		if err := rrows.Scan(&court, &start, &end, &reason); err != nil {
			return blocked, err
		}
		var cid *int64
		if court.Valid && court.Int64 > 0 {
			v := court.Int64
			cid = &v
		}
		n, err := s.updateAvailableToBlocked(ctx, facilityID, cid, date, normalizeTime(start), normalizeTime(end), reason)
		if err != nil {
			return blocked, err
		}
		blocked += n
	}
	return blocked, rrows.Err()
}

func normalizeWeekdays(in []int) []int {
	seen := map[int]bool{}
	var out []int
	for _, w := range in {
		if w == 0 {
			w = 7 // JS Sunday
		}
		if w < 1 || w > 7 || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	sort.Ints(out)
	return out
}

func dartWeekday(t time.Time) int {
	// Go: Sun=0 … Sat=6 → Dart: Mon=1 … Sun=7
	w := int(t.Weekday())
	if w == 0 {
		return 7
	}
	return w
}

func expandWeekdaysToDates(weekdays []int, weeksAhead int) []string {
	want := map[int]bool{}
	for _, w := range weekdays {
		want[w] = true
	}
	now := time.Now()
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	var out []string
	for i := 0; i < weeksAhead*7; i++ {
		d := from.AddDate(0, 0, i)
		if want[dartWeekday(d)] {
			out = append(out, d.Format("2006-01-02"))
		}
	}
	return out
}

func mergeUniqueDates(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range [][]string{a, b} {
		for _, d := range list {
			d = strings.TrimSpace(d)
			if d == "" || seen[d] {
				continue
			}
			seen[d] = true
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

// pgIntArrayLiteral builds '{1,2,3}' for INT[] insert without lib/pq.
func pgIntArrayLiteral(vals []int) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = strconv.Itoa(v)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func (s *Service) updateAvailableToBlocked(
	ctx context.Context,
	facilityID int64,
	courtID *int64,
	day, start, end, reason string,
) (int, error) {
	purpose := strings.TrimSpace(reason)
	if purpose == "" {
		purpose = "Blocked"
	}
	// Always stamp purpose on the slot (Coaching / Membership / Maintenance / …).
	q := `
UPDATE booking_slots SET status='blocked', notes=$1, updated_at=NOW()
WHERE facility_id=$2 AND slot_date=$3::date AND status='available'
  AND start_time >= $4::time AND end_time <= $5::time`
	args := []interface{}{purpose, facilityID, day, start, end}
	if courtID != nil && *courtID > 0 {
		q += ` AND court_id=$6`
		args = append(args, *courtID)
	}
	res, err := s.DB.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// ListFacilityBlocks returns blocked slots from today through toDate (detail / debug).
func (s *Service) ListFacilityBlocks(ctx context.Context, facilityID int64, from, to string) ([]BlockListItem, error) {
	if facilityID <= 0 {
		return nil, fmt.Errorf("invalid_facility")
	}
	if err := validateDate(from); err != nil {
		return nil, err
	}
	if err := validateDate(to); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT s.id, s.facility_id, s.court_id, c.name, s.slot_date::text, s.start_time::text, s.end_time::text,
       s.status, COALESCE(s.notes,'')
FROM booking_slots s
JOIN booking_courts c ON c.id = s.court_id
WHERE s.facility_id=$1
  AND s.slot_date >= $2::date AND s.slot_date <= $3::date
  AND s.status='blocked'
ORDER BY s.slot_date, c.name, s.start_time`, facilityID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BlockListItem
	for rows.Next() {
		var b BlockListItem
		if err := rows.Scan(&b.ID, &b.FacilityID, &b.CourtID, &b.CourtName, &b.SlotDate, &b.StartTime, &b.EndTime, &b.Status, &b.Notes); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ListFacilityBlockRules returns Active-blocks rows: recurring plans/rules + one-off dates.
// Does NOT expand to every blocked slot (avoids 1000+ rows for membership).
func (s *Service) ListFacilityBlockRules(ctx context.Context, facilityID int64) ([]BlockRuleItem, error) {
	if facilityID <= 0 {
		return nil, fmt.Errorf("invalid_facility")
	}
	var out []BlockRuleItem

	rrows, err := s.DB.QueryContext(ctx, `
SELECT r.id, r.facility_id, r.court_id, COALESCE(c.name,''), r.weekdays::text,
       r.start_time::text, r.end_time::text, COALESCE(r.reason,'')
FROM booking_recurring_blocks r
LEFT JOIN booking_courts c ON c.id = r.court_id
WHERE r.facility_id=$1 AND r.active=TRUE
ORDER BY r.start_time, r.id`, facilityID)
	if err != nil {
		return nil, err
	}
	defer rrows.Close()
	for rrows.Next() {
		var item BlockRuleItem
		var court sql.NullInt64
		var wdText string
		if err := rrows.Scan(&item.ID, &item.FacilityID, &court, &item.CourtName, &wdText,
			&item.StartTime, &item.EndTime, &item.Reason); err != nil {
			return nil, err
		}
		if court.Valid && court.Int64 > 0 {
			v := court.Int64
			item.CourtID = &v
		}
		if item.CourtName == "" {
			item.CourtName = "All Sub Facilities"
		}
		item.Weekdays = parsePgIntArray(wdText)
		item.Days = weekdaysCSV(item.Weekdays)
		item.IsRecursive = 1
		item.Kind = "recurring"
		item.Notes = item.Reason
		item.Category, item.PlanRef = splitPurposePlan(item.Reason)
		out = append(out, item)
	}
	if err := rrows.Err(); err != nil {
		return nil, err
	}

	// One-off blocks: future booking_blocks that are single-date (not covered by listing every recurrence expansion).
	// Show distinct (date, start, end, reason, court) for dates from today; skip rows that match an active recurring rule time+reason.
	brows, err := s.DB.QueryContext(ctx, `
SELECT DISTINCT ON (b.block_date, b.start_time, b.end_time, COALESCE(b.reason,''), COALESCE(b.court_id,0))
  b.id, b.facility_id, b.court_id, COALESCE(c.name,''), b.block_date::text,
  b.start_time::text, b.end_time::text, COALESCE(b.reason,'')
FROM booking_blocks b
LEFT JOIN booking_courts c ON c.id = b.court_id
WHERE b.facility_id=$1 AND b.block_date >= CURRENT_DATE
  AND NOT EXISTS (
    SELECT 1 FROM booking_recurring_blocks r
    WHERE r.facility_id=b.facility_id AND r.active=TRUE
      AND r.start_time=b.start_time AND r.end_time=b.end_time
      AND COALESCE(r.reason,'') = COALESCE(b.reason,'')
      AND (r.court_id IS NULL OR b.court_id IS NULL OR r.court_id = b.court_id)
  )
ORDER BY b.block_date, b.start_time, b.end_time, COALESCE(b.reason,''), COALESCE(b.court_id,0), b.id`, facilityID)
	if err != nil {
		return out, err
	}
	defer brows.Close()
	for brows.Next() {
		var item BlockRuleItem
		var court sql.NullInt64
		if err := brows.Scan(&item.ID, &item.FacilityID, &court, &item.CourtName, &item.Date,
			&item.StartTime, &item.EndTime, &item.Reason); err != nil {
			return out, err
		}
		if court.Valid && court.Int64 > 0 {
			v := court.Int64
			item.CourtID = &v
		}
		if item.CourtName == "" {
			item.CourtName = "All Sub Facilities"
		}
		item.IsRecursive = 0
		item.Kind = "one_off"
		item.Notes = item.Reason
		item.Category, item.PlanRef = splitPurposePlan(item.Reason)
		out = append(out, item)
	}
	return out, brows.Err()
}

func splitPurposePlan(reason string) (category, planRef string) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "Blocked", ""
	}
	if i := strings.Index(reason, ":"); i > 0 {
		cat := strings.TrimSpace(reason[:i])
		ref := strings.TrimSpace(reason[i+1:])
		return cat, ref
	}
	return reason, ""
}

func parsePgIntArray(s string) []int {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "{")
	s = strings.TrimSuffix(s, "}")
	if s == "" {
		return nil
	}
	var out []int
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err == nil {
			out = append(out, n)
		}
	}
	return out
}

func weekdaysCSV(wds []int) string {
	parts := make([]string, 0, len(wds))
	for _, w := range wds {
		parts = append(parts, strconv.Itoa(w))
	}
	return strings.Join(parts, ",")
}

// DeleteBlockRule removes a recurring rule (or one-off block row) and frees matching slots
// everywhere: all courts (when rule is facility-wide), booking_slots, booking_blocks, app_slots.
func (s *Service) DeleteBlockRule(ctx context.Context, facilityID, ruleID int64, kind string) (int, error) {
	if facilityID <= 0 || ruleID <= 0 {
		return 0, fmt.Errorf("invalid_id")
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	unblocked := 0

	if kind == "" || kind == "recurring" {
		var court sql.NullInt64
		var start, end, reason string
		err := s.DB.QueryRowContext(ctx, `
SELECT court_id, start_time::text, end_time::text, COALESCE(reason,'')
FROM booking_recurring_blocks WHERE id=$1 AND facility_id=$2 AND active=TRUE`,
			ruleID, facilityID).Scan(&court, &start, &end, &reason)
		if err == nil {
			_, _ = s.DB.ExecContext(ctx, `
UPDATE booking_recurring_blocks SET active=FALSE WHERE id=$1 AND facility_id=$2`, ruleID, facilityID)
			start = normalizeTime(start)
			end = normalizeTime(end)
			var cid *int64
			if court.Valid && court.Int64 > 0 {
				v := court.Int64
				cid = &v
			}
			n, err := s.freePlanBlockedEverywhere(ctx, facilityID, start, end, reason, cid, "" /* all future dates */)
			if err != nil {
				return unblocked, err
			}
			return n, nil
		}
		if err != sql.ErrNoRows {
			return 0, err
		}
	}

	// One-off booking_blocks row
	var court sql.NullInt64
	var date, start, end, reason string
	err := s.DB.QueryRowContext(ctx, `
SELECT court_id, block_date::text, start_time::text, end_time::text, COALESCE(reason,'')
FROM booking_blocks WHERE id=$1 AND facility_id=$2`, ruleID, facilityID).
		Scan(&court, &date, &start, &end, &reason)
	if err == sql.ErrNoRows {
		return 0, fmt.Errorf("not_found")
	}
	if err != nil {
		return 0, err
	}
	start = normalizeTime(start)
	end = normalizeTime(end)
	var cid *int64
	if court.Valid && court.Int64 > 0 {
		v := court.Int64
		cid = &v
	}
	n, err := s.freePlanBlockedEverywhere(ctx, facilityID, start, end, reason, cid, date)
	if err != nil {
		return 0, err
	}
	unblocked += n
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM booking_blocks WHERE id=$1 AND facility_id=$2`, ruleID, facilityID)
	return unblocked, nil
}

// freePlanBlockedEverywhere frees blocked slots for a plan across courts/tables.
// If onlyDate is set, only that day; otherwise all dates from today forward.
func (s *Service) freePlanBlockedEverywhere(
	ctx context.Context,
	facilityID int64,
	start, end, reason string,
	courtID *int64,
	onlyDate string,
) (int, error) {
	q := `
UPDATE booking_slots SET status='available', notes=NULL, updated_at=NOW()
WHERE facility_id=$1 AND status='blocked'
  AND start_time >= $2::time AND end_time <= $3::time`
	args := []interface{}{facilityID, start, end}
	if onlyDate != "" {
		q += fmt.Sprintf(` AND slot_date=$%d::date`, len(args)+1)
		args = append(args, onlyDate)
	} else {
		q += ` AND slot_date >= CURRENT_DATE`
	}
	if reason != "" {
		// Exact match or "Purpose: plan" style / contains plan name.
		q += fmt.Sprintf(` AND (notes=$%d OR notes ILIKE $%d OR notes ILIKE $%d)`,
			len(args)+1, len(args)+2, len(args)+3)
		args = append(args, reason, reason+"%", "%"+reason+"%")
	}
	if courtID != nil {
		q += fmt.Sprintf(` AND court_id=$%d`, len(args)+1)
		args = append(args, *courtID)
	}
	res, err := s.DB.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()

	bq := `
DELETE FROM booking_blocks
WHERE facility_id=$1
  AND start_time=$2::time AND end_time=$3::time`
	bargs := []interface{}{facilityID, start, end}
	if onlyDate != "" {
		bq += fmt.Sprintf(` AND block_date=$%d::date`, len(bargs)+1)
		bargs = append(bargs, onlyDate)
	} else {
		bq += ` AND block_date >= CURRENT_DATE`
	}
	if reason != "" {
		bq += fmt.Sprintf(` AND (COALESCE(reason,'')=$%d OR reason ILIKE $%d OR reason ILIKE $%d)`,
			len(bargs)+1, len(bargs)+2, len(bargs)+3)
		bargs = append(bargs, reason, reason+"%", "%"+reason+"%")
	}
	if courtID != nil {
		bq += fmt.Sprintf(` AND (court_id IS NULL OR court_id=$%d)`, len(bargs)+1)
		bargs = append(bargs, *courtID)
	}
	_, _ = s.DB.ExecContext(ctx, bq, bargs...)

	// Mirror free status into Proximity app_slots for matching times.
	aq := `
UPDATE app_slots SET status='available'
WHERE facility_id=$1
  AND LOWER(status)='blocked'`
	aargs := []interface{}{facilityID}
	if onlyDate != "" {
		aq += fmt.Sprintf(` AND slot_date=$%d::date`, len(aargs)+1)
		aargs = append(aargs, onlyDate)
	} else {
		aq += ` AND slot_date >= CURRENT_DATE`
	}
	// time_range stored like "06:00-07:00" — match by prefix of normalized times when possible
	startShort := strings.TrimSuffix(start, ":00")
	if len(start) >= 5 {
		startShort = start[:5]
	}
	endShort := strings.TrimSuffix(end, ":00")
	if len(end) >= 5 {
		endShort = end[:5]
	}
	aq += fmt.Sprintf(` AND (time_range=$%d OR time_range ILIKE $%d)`, len(aargs)+1, len(aargs)+2)
	aargs = append(aargs, startShort+"-"+endShort, startShort+"%")
	_, _ = s.DB.ExecContext(ctx, aq, aargs...)

	return int(n), nil
}

type blockRuleMatch struct {
	courtID *int64
	start   string
	end     string
	reason  string
	date    string // one_off only
	kind    string
}

func (s *Service) loadBlockRuleMatch(ctx context.Context, facilityID, ruleID int64, kind string) (*blockRuleMatch, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" || kind == "recurring" {
		var court sql.NullInt64
		var start, end, reason string
		err := s.DB.QueryRowContext(ctx, `
SELECT court_id, start_time::text, end_time::text, COALESCE(reason,'')
FROM booking_recurring_blocks WHERE id=$1 AND facility_id=$2 AND active=TRUE`,
			ruleID, facilityID).Scan(&court, &start, &end, &reason)
		if err == nil {
			m := &blockRuleMatch{
				start:  normalizeTime(start),
				end:    normalizeTime(end),
				reason: reason,
				kind:   "recurring",
			}
			if court.Valid && court.Int64 > 0 {
				v := court.Int64
				m.courtID = &v
			}
			return m, nil
		}
		if err != sql.ErrNoRows {
			return nil, err
		}
		if kind == "recurring" {
			return nil, fmt.Errorf("not_found")
		}
	}

	var court sql.NullInt64
	var date, start, end, reason string
	err := s.DB.QueryRowContext(ctx, `
SELECT court_id, block_date::text, start_time::text, end_time::text, COALESCE(reason,'')
FROM booking_blocks WHERE id=$1 AND facility_id=$2`, ruleID, facilityID).
		Scan(&court, &date, &start, &end, &reason)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("not_found")
	}
	if err != nil {
		return nil, err
	}
	m := &blockRuleMatch{
		start:  normalizeTime(start),
		end:    normalizeTime(end),
		reason: reason,
		date:   date,
		kind:   "one_off",
	}
	if court.Valid && court.Int64 > 0 {
		v := court.Int64
		m.courtID = &v
	}
	return m, nil
}

func (m *blockRuleMatch) appendSlotFilters(q string, args []interface{}) (string, []interface{}) {
	if m.reason != "" {
		q += fmt.Sprintf(` AND notes=$%d`, len(args)+1)
		args = append(args, m.reason)
	}
	if m.courtID != nil {
		q += fmt.Sprintf(` AND court_id=$%d`, len(args)+1)
		args = append(args, *m.courtID)
	}
	return q, args
}

// ListBlockRuleDates returns distinct blocked slot dates for an Active-blocks plan/rule.
func (s *Service) ListBlockRuleDates(ctx context.Context, facilityID, ruleID int64, kind string) ([]string, error) {
	m, err := s.loadBlockRuleMatch(ctx, facilityID, ruleID, kind)
	if err != nil {
		return nil, err
	}
	if m.kind == "one_off" && m.date != "" {
		return []string{m.date}, nil
	}
	q := `
SELECT DISTINCT s.slot_date::text
FROM booking_slots s
WHERE s.facility_id=$1 AND s.status='blocked'
  AND s.start_time >= $2::time AND s.end_time <= $3::time
  AND s.slot_date >= CURRENT_DATE`
	args := []interface{}{facilityID, m.start, m.end}
	q, args = m.appendSlotFilters(q, args)
	q += ` ORDER BY 1`
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		if d != "" {
			out = append(out, d)
		}
	}
	return out, rows.Err()
}

// FreeBlocksByReason removes recurring/one-off rules and frees blocked slots whose
// notes/reason match the membership (or other) plan label — e.g. "Membership: Gold".
func (s *Service) FreeBlocksByReason(ctx context.Context, facilityID int64, reason string) (int, error) {
	reason = strings.TrimSpace(reason)
	if facilityID <= 0 || reason == "" {
		return 0, fmt.Errorf("invalid_reason")
	}
	unblocked := 0

	// Remove matching recurring rules so they disappear from Active Blocks / Manage Plans.
	_, _ = s.DB.ExecContext(ctx, `
DELETE FROM booking_recurring_blocks
WHERE facility_id=$1
  AND (COALESCE(reason,'') = $2 OR reason ILIKE $3 OR reason ILIKE $4)`,
		facilityID, reason, reason+"%", "%"+reason+"%")

	q := `
UPDATE booking_slots SET status='available', notes=NULL, updated_at=NOW()
WHERE facility_id=$1 AND status='blocked'
  AND slot_date >= CURRENT_DATE
  AND (COALESCE(notes,'') = $2 OR notes ILIKE $3 OR notes ILIKE $4)`
	res, err := s.DB.ExecContext(ctx, q, facilityID, reason, reason+"%", "%"+reason+"%")
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	unblocked += int(n)

	_, _ = s.DB.ExecContext(ctx, `
DELETE FROM booking_blocks
WHERE facility_id=$1 AND block_date >= CURRENT_DATE
  AND (COALESCE(reason,'') = $2 OR reason ILIKE $3 OR reason ILIKE $4)`,
		facilityID, reason, reason+"%", "%"+reason+"%")

	return unblocked, nil
}

// FreeBlockRuleRange frees blocked slots for a plan only between fromDate and toDate (inclusive).
// Does not deactivate the recurring rule — only opens that date window.
func (s *Service) FreeBlockRuleRange(ctx context.Context, facilityID, ruleID int64, kind, fromDate, toDate string) (int, error) {
	if facilityID <= 0 || ruleID <= 0 {
		return 0, fmt.Errorf("invalid_id")
	}
	fromDate = strings.TrimSpace(fromDate)
	toDate = strings.TrimSpace(toDate)
	if err := validateDate(fromDate); err != nil {
		return 0, fmt.Errorf("invalid_from")
	}
	if err := validateDate(toDate); err != nil {
		return 0, fmt.Errorf("invalid_to")
	}
	if toDate < fromDate {
		return 0, fmt.Errorf("invalid_date_range")
	}

	m, err := s.loadBlockRuleMatch(ctx, facilityID, ruleID, kind)
	if err != nil {
		return 0, err
	}

	q := `
UPDATE booking_slots SET status='available', notes=NULL, updated_at=NOW()
WHERE facility_id=$1 AND status='blocked'
  AND start_time >= $2::time AND end_time <= $3::time
  AND slot_date >= $4::date AND slot_date <= $5::date`
	args := []interface{}{facilityID, m.start, m.end, fromDate, toDate}
	q, args = m.appendSlotFilters(q, args)
	res, err := s.DB.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()

	// Remove matching booking_blocks rows in the same window (keep recurring rule active).
	bq := `
DELETE FROM booking_blocks
WHERE facility_id=$1
  AND start_time=$2::time AND end_time=$3::time
  AND block_date >= $4::date AND block_date <= $5::date
  AND COALESCE(reason,'')=$6`
	bargs := []interface{}{facilityID, m.start, m.end, fromDate, toDate, m.reason}
	if m.courtID != nil {
		bq += ` AND (court_id IS NULL OR court_id=$7)`
		bargs = append(bargs, *m.courtID)
	}
	_, _ = s.DB.ExecContext(ctx, bq, bargs...)

	// One-off rule row: if its date falls in range, remove the rule entry too.
	if m.kind == "one_off" && m.date >= fromDate && m.date <= toDate {
		_, _ = s.DB.ExecContext(ctx,
			`DELETE FROM booking_blocks WHERE id=$1 AND facility_id=$2`, ruleID, facilityID)
	}

	return int(n), nil
}

func (s *Service) CollectPayment(ctx context.Context, bookingID int64, in CollectPaymentInput) (*Booking, error) {
	mode := strings.TrimSpace(in.PayMode)
	if mode == "" {
		return nil, fmt.Errorf("pay_mode_required")
	}
	var amount interface{}
	if in.Amount != nil {
		amount = *in.Amount
	}
	res, err := s.DB.ExecContext(ctx, `
UPDATE booking_bookings
SET payment_status='paid', pay_mode=$2, amount_paid=$3, updated_at=NOW()
WHERE id=$1 AND status='confirmed'`, bookingID, mode, amount)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("not_found")
	}
	return s.GetBooking(ctx, bookingID)
}

func (s *Service) CreateCoupons(ctx context.Context, facilityID int64, in CreateCouponsInput) ([]Coupon, error) {
	codes := append([]string{}, in.Codes...)
	if len(codes) == 0 {
		count := in.Count
		if count <= 0 {
			count = 1
		}
		if count > 100 {
			count = 100
		}
		for i := 0; i < count; i++ {
			c, err := stubBookingCoupon()
			if err != nil {
				return nil, err
			}
			codes = append(codes, c)
		}
	}
	out := make([]Coupon, 0, len(codes))
	for _, code := range codes {
		code = strings.TrimSpace(strings.ToUpper(code))
		if code == "" {
			continue
		}
		var id int64
		err := s.DB.QueryRowContext(ctx, `
INSERT INTO booking_coupons (facility_id, code, discount, status)
VALUES ($1,$2,$3,'active')
ON CONFLICT (facility_id, code) DO UPDATE SET discount=EXCLUDED.discount, status='active'
RETURNING id`, facilityID, code, in.Discount).Scan(&id)
		if err != nil {
			return out, err
		}
		out = append(out, Coupon{
			ID: id, FacilityID: facilityID, Code: code, Discount: in.Discount, Status: "active",
		})
	}
	return out, nil
}

func stubBookingCoupon() (string, error) {
	const chars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 6)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			return "", err
		}
		b[i] = chars[n.Int64()]
	}
	return "SAVE" + string(b), nil
}

func (s *Service) UpdateUserStatus(ctx context.Context, facilityID, userID int64, in UserStatusInput) error {
	st := strings.ToLower(strings.TrimSpace(in.Status))
	if st != "active" && st != "blocked" {
		return fmt.Errorf("invalid_status")
	}
	if facilityID <= 0 || userID <= 0 {
		return fmt.Errorf("invalid_user")
	}
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO booking_user_status (facility_id, user_id, status, reason, updated_at)
VALUES ($1,$2,$3,NULLIF($4,''),NOW())
ON CONFLICT (facility_id, user_id) DO UPDATE
SET status=EXCLUDED.status, reason=EXCLUDED.reason, updated_at=NOW()`,
		facilityID, userID, st, strings.TrimSpace(in.Reason))
	return err
}

func normalizeTime(t string) string {
	t = strings.TrimSpace(t)
	if t == "" {
		return ""
	}
	if len(t) == 5 {
		return t + ":00"
	}
	if len(t) >= 8 {
		return t[:8]
	}
	return t
}
