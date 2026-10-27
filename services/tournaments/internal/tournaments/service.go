package tournaments

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/facinect/tournaments/internal/config"
)

type Service struct {
	DB  *sql.DB
	Cfg config.Config
}

type Tournament struct {
	ID                   int64                  `json:"id"`
	FacilityID           int64                  `json:"facility_id"`
	SportID              *int64                 `json:"sport_id,omitempty"`
	Name                 string                 `json:"name"`
	StartDate            string                 `json:"start_date,omitempty"`
	EndDate              string                 `json:"end_date,omitempty"`
	VenueAddress         string                 `json:"venue_address,omitempty"`
	LocationURL          string                 `json:"location_url,omitempty"`
	ContactNumbers       string                 `json:"contact_numbers,omitempty"`
	Rules                string                 `json:"rules,omitempty"`
	GroupURL             string                 `json:"group_url,omitempty"`
	Slug                 string                 `json:"slug,omitempty"`
	Status               string                 `json:"status"`
	AllowFullPayment     bool                   `json:"allow_full_payment"`
	AllowAdvancePayment  bool                   `json:"allow_advance_payment"`
	AllowSpotPayment     bool                   `json:"allow_spot_payment"`
	AdvanceAmount        float64                `json:"advance_amount"`
	// EntriesOpen is 1/0 to match PHP + StrollX int.tryParse.
	EntriesOpen          int                    `json:"entries_open"`
	Categories           []Category             `json:"categories,omitempty"`
	PaymentOptions       map[string]interface{} `json:"payment_options,omitempty"`
	CreatedAt            string                 `json:"created_at,omitempty"`
	UpdatedAt            string                 `json:"updated_at,omitempty"`
}

type UpsertInput struct {
	Name                string          `json:"name"`
	SportID             *int64          `json:"sport_id"`
	StartDate           string          `json:"start_date"`
	EndDate             string          `json:"end_date"`
	VenueAddress        string          `json:"venue_address"`
	LocationURL         string          `json:"location_url"`
	ContactNumbers      string          `json:"contact_numbers"`
	Rules               string          `json:"rules"`
	GroupURL            string          `json:"group_url"`
	Slug                string          `json:"slug"`
	Status              string          `json:"status"`
	AllowFullPayment    *int            `json:"allow_full_payment"`
	AllowAdvancePayment *int            `json:"allow_advance_payment"`
	AllowSpotPayment    *int            `json:"allow_spot_payment"`
	AdvanceAmount       *float64        `json:"advance_amount"`
	// Categories nil = leave unchanged; non-nil (including empty) = replace.
	Categories []CategoryInput `json:"categories"`
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func (s *Service) List(ctx context.Context, facilityID int64, status string) ([]Tournament, error) {
	if facilityID <= 0 {
		return nil, fmt.Errorf("invalid_facility")
	}
	q := `
SELECT id, facility_id, sport_id, name,
       COALESCE(start_date::text,''), COALESCE(end_date::text,''),
       COALESCE(venue_address,''), COALESCE(location_url,''),
       COALESCE(contact_numbers,''), COALESCE(rules,''), COALESCE(group_url,''), COALESCE(slug,''),
       status,
       allow_full_payment, allow_advance_payment, allow_spot_payment, advance_amount::float8,
       CASE WHEN entries_open THEN 1 ELSE 0 END,
       created_at::text, updated_at::text
FROM tournaments WHERE facility_id=$1`
	args := []interface{}{facilityID}
	st := strings.TrimSpace(strings.ToLower(status))
	if st != "" && st != "all" {
		q += ` AND status=$2`
		args = append(args, st)
	}
	q += ` ORDER BY COALESCE(start_date, DATE '1970-01-01') DESC, id DESC`

	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Tournament{}
	for rows.Next() {
		t, err := scanTournament(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (s *Service) CountActive(ctx context.Context, facilityID int64) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `
SELECT COUNT(*) FROM tournaments
WHERE facility_id=$1 AND status IN ('draft','upcoming','live')`, facilityID).Scan(&n)
	return n, err
}

func (s *Service) Get(ctx context.Context, id int64) (*Tournament, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT id, facility_id, sport_id, name,
       COALESCE(start_date::text,''), COALESCE(end_date::text,''),
       COALESCE(venue_address,''), COALESCE(location_url,''),
       COALESCE(contact_numbers,''), COALESCE(rules,''), COALESCE(group_url,''), COALESCE(slug,''),
       status,
       allow_full_payment, allow_advance_payment, allow_spot_payment, advance_amount::float8,
       CASE WHEN entries_open THEN 1 ELSE 0 END,
       created_at::text, updated_at::text
FROM tournaments WHERE id=$1`, id)
	t, err := scanTournament(row)
	if err != nil {
		return nil, err
	}
	if err := s.attachCategories(ctx, t); err != nil {
		return nil, err
	}
	return t, nil
}

// GetLite returns tournament row without categories (status updates).
func (s *Service) GetLite(ctx context.Context, id int64) (*Tournament, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT id, facility_id, sport_id, name,
       COALESCE(start_date::text,''), COALESCE(end_date::text,''),
       COALESCE(venue_address,''), COALESCE(location_url,''),
       COALESCE(contact_numbers,''), COALESCE(rules,''), COALESCE(group_url,''), COALESCE(slug,''),
       status,
       allow_full_payment, allow_advance_payment, allow_spot_payment, advance_amount::float8,
       CASE WHEN entries_open THEN 1 ELSE 0 END,
       created_at::text, updated_at::text
FROM tournaments WHERE id=$1`, id)
	return scanTournament(row)
}

func (s *Service) Create(ctx context.Context, facilityID int64, in UpsertInput) (*Tournament, error) {
	name := strings.TrimSpace(in.Name)
	if facilityID <= 0 || name == "" {
		return nil, fmt.Errorf("invalid_tournament")
	}
	if err := validateOptionalDates(in.StartDate, in.EndDate); err != nil {
		return nil, err
	}
	status := normalizeStatus(in.Status, "draft")
	slug := strings.TrimSpace(in.Slug)
	if slug == "" {
		slug = slugify(name)
	} else {
		slug = slugify(slug)
	}

	var sport, start, end interface{}
	if in.SportID != nil {
		sport = *in.SportID
	}
	if strings.TrimSpace(in.StartDate) != "" {
		start = in.StartDate
	}
	if strings.TrimSpace(in.EndDate) != "" {
		end = in.EndDate
	}

	fullPay := true
	advPay := true
	spotPay := false
	advAmt := 0.0
	if in.AllowFullPayment != nil {
		fullPay = *in.AllowFullPayment != 0
	}
	if in.AllowAdvancePayment != nil {
		advPay = *in.AllowAdvancePayment != 0
	}
	if in.AllowSpotPayment != nil {
		spotPay = *in.AllowSpotPayment != 0
	}
	if in.AdvanceAmount != nil {
		advAmt = *in.AdvanceAmount
	}

	var id int64
	err := s.DB.QueryRowContext(ctx, `
INSERT INTO tournaments
  (facility_id, sport_id, name, start_date, end_date, venue_address, location_url,
   contact_numbers, rules, group_url, slug, status,
   allow_full_payment, allow_advance_payment, allow_spot_payment, advance_amount)
VALUES ($1,$2,$3,$4::date,$5::date,NULLIF($6,''),NULLIF($7,''),NULLIF($8,''),NULLIF($9,''),NULLIF($10,''),NULLIF($11,''),$12,
        $13,$14,$15,$16)
RETURNING id`,
		facilityID, sport, name, start, end,
		strings.TrimSpace(in.VenueAddress),
		strings.TrimSpace(in.LocationURL),
		strings.TrimSpace(in.ContactNumbers),
		strings.TrimSpace(in.Rules),
		strings.TrimSpace(in.GroupURL),
		slug, status,
		fullPay, advPay, spotPay, advAmt,
	).Scan(&id)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "idx_tournaments_facility_slug") {
			return nil, fmt.Errorf("slug_taken")
		}
		return nil, err
	}
	if in.Categories != nil {
		if err := s.replaceCategories(ctx, id, in.Categories); err != nil {
			return nil, err
		}
	}
	return s.Get(ctx, id)
}

func (s *Service) Update(ctx context.Context, facilityID, id int64, in UpsertInput) (*Tournament, error) {
	cur, err := s.GetLite(ctx, id)
	if err != nil {
		return nil, err
	}
	if cur.FacilityID != facilityID {
		return nil, fmt.Errorf("facility_mismatch")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = cur.Name
	}
	if err := validateOptionalDates(in.StartDate, in.EndDate); err != nil {
		return nil, err
	}
	status := normalizeStatus(in.Status, cur.Status)
	slug := strings.TrimSpace(in.Slug)
	if slug == "" {
		slug = cur.Slug
	} else {
		slug = slugify(slug)
	}

	sport := interface{}(nil)
	if in.SportID != nil {
		sport = *in.SportID
	} else if cur.SportID != nil {
		sport = *cur.SportID
	}
	start := nullableDate(in.StartDate, cur.StartDate)
	end := nullableDate(in.EndDate, cur.EndDate)

	venue := pickStr(in.VenueAddress, cur.VenueAddress)
	loc := pickStr(in.LocationURL, cur.LocationURL)
	contacts := pickStr(in.ContactNumbers, cur.ContactNumbers)
	rules := pickStr(in.Rules, cur.Rules)
	groupURL := pickStr(in.GroupURL, cur.GroupURL)

	fullPay := cur.AllowFullPayment
	advPay := cur.AllowAdvancePayment
	spotPay := cur.AllowSpotPayment
	advAmt := cur.AdvanceAmount
	if in.AllowFullPayment != nil {
		fullPay = *in.AllowFullPayment != 0
	}
	if in.AllowAdvancePayment != nil {
		advPay = *in.AllowAdvancePayment != 0
	}
	if in.AllowSpotPayment != nil {
		spotPay = *in.AllowSpotPayment != 0
	}
	if in.AdvanceAmount != nil {
		advAmt = *in.AdvanceAmount
	}

	_, err = s.DB.ExecContext(ctx, `
UPDATE tournaments SET
  sport_id=$1, name=$2, start_date=$3::date, end_date=$4::date,
  venue_address=NULLIF($5,''), location_url=NULLIF($6,''),
  contact_numbers=NULLIF($7,''), rules=NULLIF($8,''), group_url=NULLIF($9,''), slug=NULLIF($10,''),
  status=$11,
  allow_full_payment=$12, allow_advance_payment=$13, allow_spot_payment=$14, advance_amount=$15,
  updated_at=NOW()
WHERE id=$16 AND facility_id=$17`,
		sport, name, start, end, venue, loc, contacts, rules, groupURL, slug, status,
		fullPay, advPay, spotPay, advAmt, id, facilityID,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "idx_tournaments_facility_slug") {
			return nil, fmt.Errorf("slug_taken")
		}
		return nil, err
	}
	if in.Categories != nil {
		if err := s.replaceCategories(ctx, id, in.Categories); err != nil {
			return nil, err
		}
	}
	return s.Get(ctx, id)
}

func (s *Service) UpdateStatus(ctx context.Context, facilityID, id int64, status string) (*Tournament, error) {
	st := normalizeStatus(status, "")
	if st == "" {
		return nil, fmt.Errorf("invalid_status")
	}
	res, err := s.DB.ExecContext(ctx, `
UPDATE tournaments SET status=$1, updated_at=NOW()
WHERE id=$2 AND facility_id=$3`, st, id, facilityID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("not_found")
	}
	return s.Get(ctx, id)
}

type scannable interface {
	Scan(dest ...interface{}) error
}

func scanTournament(row scannable) (*Tournament, error) {
	var t Tournament
	var sport sql.NullInt64
	err := row.Scan(
		&t.ID, &t.FacilityID, &sport, &t.Name,
		&t.StartDate, &t.EndDate, &t.VenueAddress, &t.LocationURL,
		&t.ContactNumbers, &t.Rules, &t.GroupURL, &t.Slug, &t.Status,
		&t.AllowFullPayment, &t.AllowAdvancePayment, &t.AllowSpotPayment, &t.AdvanceAmount,
		&t.EntriesOpen,
		&t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if sport.Valid {
		v := sport.Int64
		t.SportID = &v
	}
	return &t, nil
}

func normalizeStatus(status, fallback string) string {
	st := strings.ToLower(strings.TrimSpace(status))
	switch st {
	case "draft", "upcoming", "live", "completed", "cancelled":
		return st
	case "":
		return strings.ToLower(strings.TrimSpace(fallback))
	default:
		return ""
	}
}

func validateOptionalDates(start, end string) error {
	if strings.TrimSpace(start) != "" {
		if err := validateDate(start); err != nil {
			return err
		}
	}
	if strings.TrimSpace(end) != "" {
		if err := validateDate(end); err != nil {
			return err
		}
	}
	return nil
}

func validateDate(date string) error {
	date = strings.TrimSpace(date)
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return fmt.Errorf("invalid_date")
	}
	return nil
}

func nullableDate(in, fallback string) interface{} {
	in = strings.TrimSpace(in)
	if in != "" {
		return in
	}
	fallback = strings.TrimSpace(fallback)
	if fallback != "" {
		return fallback
	}
	return nil
}

func pickStr(in, fallback string) string {
	if strings.TrimSpace(in) != "" {
		return strings.TrimSpace(in)
	}
	return fallback
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else if r == ' ' || r == '-' || r == '_' {
			b.WriteByte('-')
		}
	}
	out := nonSlug.ReplaceAllString(b.String(), "-")
	out = strings.Trim(out, "-")
	if out == "" {
		return "tournament"
	}
	if len(out) > 80 {
		out = out[:80]
		out = strings.Trim(out, "-")
	}
	return out
}
