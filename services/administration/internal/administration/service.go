package administration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/facinect/administration/internal/config"
)

type Service struct {
	DB  *sql.DB
	Cfg config.Config
}

// --- Catalog ---

func PagesCatalog() []map[string]string {
	return []map[string]string{
		{"key": "dashboard", "label": "Dashboard"},
		{"key": "bookings", "label": "Slots Setup"},
		{"key": "view_bookings", "label": "View Bookings"},
		{"key": "students", "label": "Students"},
		{"key": "members", "label": "Members"},
		{"key": "payments", "label": "Payments"},
		{"key": "tournaments", "label": "Tournaments"},
		{"key": "enquiry", "label": "Enquiry"},
		{"key": "offers", "label": "Offers"},
		{"key": "app_setup", "label": "App Setup"},
		{"key": "administration", "label": "Administration"},
	}
}

func RoleDefaults(role string) []string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "admin":
		return []string{
			"dashboard", "bookings", "view_bookings", "students", "members",
			"payments", "tournaments", "enquiry", "offers", "app_setup", "administration",
		}
	case "sub_admin":
		return []string{
			"dashboard", "bookings", "view_bookings", "students", "members",
			"tournaments", "enquiry", "app_setup",
		}
	case "headcoach":
		return []string{"students", "tournaments"}
	case "coach":
		return []string{"students"}
	case "tournament_admin":
		return []string{"tournaments"}
	default:
		return []string{"dashboard"}
	}
}

func resolvePageAccess(role string, raw sql.NullString) []string {
	if raw.Valid && strings.TrimSpace(raw.String) != "" && raw.String != "null" {
		var keys []string
		if err := json.Unmarshal([]byte(raw.String), &keys); err == nil && len(keys) > 0 {
			return keys
		}
	}
	return RoleDefaults(role)
}

// --- Profile ---

type Profile struct {
	FacilityID  int64  `json:"facility_id"`
	DisplayName string `json:"display_name"`
	Location    string `json:"location,omitempty"`
	City        string `json:"city,omitempty"`
	Address     string `json:"address,omitempty"`
	Phone       string `json:"phone,omitempty"`
	WhatsApp    string `json:"whatsapp,omitempty"`
	Email       string `json:"email,omitempty"`
	LogoURL     string `json:"logo_url,omitempty"`
	MapURL      string `json:"map_url,omitempty"`
	Notes       string `json:"notes,omitempty"`
	OpenTime    string `json:"open_time,omitempty"`
	CloseTime   string `json:"close_time,omitempty"`
	Slug        string `json:"slug,omitempty"`
	Status      string `json:"status,omitempty"`
}

func (s *Service) GetProfile(ctx context.Context, facilityID int64) (*Profile, error) {
	if facilityID <= 0 {
		return nil, fmt.Errorf("invalid_facility")
	}
	var p Profile
	var loc, city, addr, phone, wa, email, logo, mapURL, notes, openT, closeT sql.NullString
	var lat, lng sql.NullFloat64
	// Coalesce profile → app_facilities so Facility tab fills logo/location/city
	// even when facility_profiles rows were partially wiped by an empty save.
	err := s.DB.QueryRowContext(ctx, `
SELECT f.id,
       COALESCE(NULLIF(p.display_name,''), NULLIF(af.name,''), f.name),
       COALESCE(NULLIF(p.location,''), NULLIF(af.location,''), ''),
       COALESCE(NULLIF(p.city,''), NULLIF(af.city_name,''), ''),
       COALESCE(p.address,''), COALESCE(p.phone,''), COALESCE(p.whatsapp,''), COALESCE(p.email,''),
       COALESCE(NULLIF(p.logo_url,''), NULLIF(af.logo_url,''), ''),
       COALESCE(p.map_url,''), COALESCE(p.notes,''),
       COALESCE(p.open_time,''), COALESCE(p.close_time,''), f.slug, f.status,
       af.latitude, af.longitude
FROM facilities f
LEFT JOIN facility_profiles p ON p.facility_id = f.id
LEFT JOIN app_facilities af ON af.id = f.id
WHERE f.id=$1`, facilityID).Scan(
		&p.FacilityID, &p.DisplayName, &loc, &city, &addr, &phone, &wa, &email,
		&logo, &mapURL, &notes, &openT, &closeT, &p.Slug, &p.Status, &lat, &lng,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("facility_not_found")
	}
	if err != nil {
		return nil, err
	}
	p.Location, p.City, p.Address = loc.String, city.String, addr.String
	p.Phone, p.WhatsApp, p.Email = phone.String, wa.String, email.String
	p.LogoURL, p.MapURL, p.Notes = logo.String, mapURL.String, notes.String
	p.OpenTime, p.CloseTime = openT.String, closeT.String
	if strings.TrimSpace(p.MapURL) == "" && lat.Valid && lng.Valid && (lat.Float64 != 0 || lng.Float64 != 0) {
		p.MapURL = fmt.Sprintf("https://www.google.com/maps?q=%g,%g", lat.Float64, lng.Float64)
	}
	return &p, nil
}

func (s *Service) UpsertProfile(ctx context.Context, facilityID int64, in Profile) (*Profile, error) {
	if facilityID <= 0 {
		return nil, fmt.Errorf("invalid_facility")
	}
	name := strings.TrimSpace(in.DisplayName)
	if name == "" {
		return nil, fmt.Errorf("display_name_required")
	}
	logoURL := strings.TrimSpace(in.LogoURL)
	mapURL := strings.TrimSpace(in.MapURL)
	// Empty logo/map must not wipe existing values (partial updates from the app).
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO facility_profiles
  (facility_id, display_name, location, city, address, phone, whatsapp, email,
   logo_url, map_url, notes, open_time, close_time, updated_at)
VALUES ($1,$2,NULLIF($3,''),NULLIF($4,''),NULLIF($5,''),NULLIF($6,''),NULLIF($7,''),NULLIF($8,''),
        NULLIF($9,''),NULLIF($10,''),NULLIF($11,''),NULLIF($12,''),NULLIF($13,''),NOW())
ON CONFLICT (facility_id) DO UPDATE SET
  display_name=EXCLUDED.display_name,
  location=COALESCE(EXCLUDED.location, facility_profiles.location),
  city=COALESCE(EXCLUDED.city, facility_profiles.city),
  address=COALESCE(EXCLUDED.address, facility_profiles.address),
  phone=COALESCE(EXCLUDED.phone, facility_profiles.phone),
  whatsapp=COALESCE(EXCLUDED.whatsapp, facility_profiles.whatsapp),
  email=COALESCE(EXCLUDED.email, facility_profiles.email),
  logo_url=COALESCE(EXCLUDED.logo_url, facility_profiles.logo_url),
  map_url=COALESCE(EXCLUDED.map_url, facility_profiles.map_url),
  notes=COALESCE(EXCLUDED.notes, facility_profiles.notes),
  open_time=COALESCE(EXCLUDED.open_time, facility_profiles.open_time),
  close_time=COALESCE(EXCLUDED.close_time, facility_profiles.close_time),
  updated_at=NOW()`,
		facilityID, name,
		strings.TrimSpace(in.Location), strings.TrimSpace(in.City), strings.TrimSpace(in.Address),
		strings.TrimSpace(in.Phone), strings.TrimSpace(in.WhatsApp), strings.TrimSpace(in.Email),
		logoURL, mapURL, strings.TrimSpace(in.Notes),
		strings.TrimSpace(in.OpenTime), strings.TrimSpace(in.CloseTime),
	)
	if err != nil {
		return nil, err
	}
	_, _ = s.DB.ExecContext(ctx, `UPDATE facilities SET name=$1, updated_at=NOW() WHERE id=$2`, name, facilityID)
	// Keep customer-facing app_facilities in sync for logo/location/city.
	_, _ = s.DB.ExecContext(ctx, `
UPDATE app_facilities SET
  name=COALESCE(NULLIF($1,''), name),
  location=COALESCE(NULLIF($2,''), location),
  city_name=COALESCE(NULLIF($3,''), city_name),
  logo_url=CASE WHEN NULLIF($4,'') IS NOT NULL THEN $4 ELSE logo_url END
WHERE id=$5`,
		name, strings.TrimSpace(in.Location), strings.TrimSpace(in.City), logoURL, facilityID,
	)
	return s.GetProfile(ctx, facilityID)
}

// --- Sports ---

type Sport struct {
	ID         int64  `json:"id"`       // catalog_sport_id when set (PHP Hostinger id), else pk
	InternalID int64  `json:"internal_id"`
	SportID    int64  `json:"sportId"` // same as id — StrollX / PHP dual-stack
	FacilityID int64  `json:"facility_id"`
	Name       string `json:"name"`
	SortOrder  int    `json:"sort_order"`
	Status     string `json:"status"`
}

type SportInput struct {
	Name           string `json:"name"`
	SortOrder      int    `json:"sort_order"`
	CatalogSportID *int64 `json:"catalog_sport_id,omitempty"`
	SportID        *int64 `json:"sportId,omitempty"`
}

func scanSport(pk, facilityID int64, name string, sortOrder int, status string, catalog sql.NullInt64) Sport {
	r := Sport{
		InternalID: pk,
		FacilityID: facilityID,
		Name:       name,
		SortOrder:  sortOrder,
		Status:     status,
	}
	if catalog.Valid && catalog.Int64 > 0 {
		r.ID = catalog.Int64
		r.SportID = catalog.Int64
	} else {
		r.ID = pk
		r.SportID = pk
	}
	return r
}

func (s *Service) ListSports(ctx context.Context, facilityID int64, status string) ([]Sport, error) {
	q := `SELECT id, facility_id, name, sort_order, status, catalog_sport_id FROM facility_sports WHERE facility_id=$1`
	args := []interface{}{facilityID}
	st := strings.ToLower(strings.TrimSpace(status))
	if st != "" && st != "all" {
		q += ` AND status=$2`
		args = append(args, st)
	}
	q += ` ORDER BY sort_order, name`
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Sport{}
	for rows.Next() {
		var pk, facilityIDRow int64
		var name, statusRow string
		var sortOrder int
		var catalog sql.NullInt64
		if err := rows.Scan(&pk, &facilityIDRow, &name, &sortOrder, &statusRow, &catalog); err != nil {
			return nil, err
		}
		out = append(out, scanSport(pk, facilityIDRow, name, sortOrder, statusRow, catalog))
	}
	return out, rows.Err()
}

func (s *Service) CreateSport(ctx context.Context, facilityID int64, in SportInput) (*Sport, error) {
	name := strings.TrimSpace(in.Name)
	if facilityID <= 0 || name == "" {
		return nil, fmt.Errorf("invalid_sport")
	}
	var catalog interface{}
	if in.CatalogSportID != nil && *in.CatalogSportID > 0 {
		catalog = *in.CatalogSportID
	} else if in.SportID != nil && *in.SportID > 0 {
		catalog = *in.SportID
	}
	var pk, facilityIDRow int64
	var nameOut, statusRow string
	var sortOrder int
	var catalogOut sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `
INSERT INTO facility_sports (facility_id, name, sort_order, status, catalog_sport_id)
VALUES ($1,$2,$3,'active',$4)
RETURNING id, facility_id, name, sort_order, status, catalog_sport_id`,
		facilityID, name, in.SortOrder, catalog,
	).Scan(&pk, &facilityIDRow, &nameOut, &sortOrder, &statusRow, &catalogOut)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, fmt.Errorf("sport_exists")
		}
		return nil, err
	}
	r := scanSport(pk, facilityIDRow, nameOut, sortOrder, statusRow, catalogOut)
	return &r, nil
}

func (s *Service) UpdateSportStatus(ctx context.Context, facilityID, sportID int64, status string) (*Sport, error) {
	st := strings.ToLower(strings.TrimSpace(status))
	if st != "active" && st != "inactive" {
		return nil, fmt.Errorf("invalid_status")
	}
	var pk, facilityIDRow int64
	var nameOut, statusRow string
	var sortOrder int
	var catalogOut sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `
UPDATE facility_sports SET status=$1, updated_at=NOW()
WHERE facility_id=$3 AND (id=$2 OR catalog_sport_id=$2)
RETURNING id, facility_id, name, sort_order, status, catalog_sport_id`, st, sportID, facilityID).Scan(
		&pk, &facilityIDRow, &nameOut, &sortOrder, &statusRow, &catalogOut,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("not_found")
	}
	if err != nil {
		return nil, err
	}
	r := scanSport(pk, facilityIDRow, nameOut, sortOrder, statusRow, catalogOut)
	return &r, nil
}

// --- Courts ---

type Court struct {
	ID           int64    `json:"id"`
	FacilityID   int64    `json:"facility_id"`
	SportID      *int64   `json:"sport_id,omitempty"`
	Name         string   `json:"name"`
	PricePerHour *float64 `json:"price_per_hour,omitempty"`
	OpenTime     string   `json:"open_time,omitempty"`
	CloseTime    string   `json:"close_time,omitempty"`
	Status       string   `json:"status"`
}

type CourtInput struct {
	SportID      *int64   `json:"sport_id"`
	Name         string   `json:"name"`
	PricePerHour *float64 `json:"price_per_hour"`
	OpenTime     string   `json:"open_time"`
	CloseTime    string   `json:"close_time"`
}

func (s *Service) ListCourts(ctx context.Context, facilityID int64, status string) ([]Court, error) {
	q := `
SELECT id, facility_id, sport_id, name, price_per_hour,
       COALESCE(open_time,''), COALESCE(close_time,''), status
FROM facility_courts WHERE facility_id=$1`
	args := []interface{}{facilityID}
	st := strings.ToLower(strings.TrimSpace(status))
	if st != "" && st != "all" {
		q += ` AND status=$2`
		args = append(args, st)
	}
	q += ` ORDER BY name`
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Court{}
	for rows.Next() {
		c, err := scanCourt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Service) CreateCourt(ctx context.Context, facilityID int64, in CourtInput) (*Court, error) {
	name := strings.TrimSpace(in.Name)
	if facilityID <= 0 || name == "" {
		return nil, fmt.Errorf("invalid_court")
	}
	var sport, price interface{}
	if in.SportID != nil && *in.SportID > 0 {
		sport = *in.SportID
	}
	if in.PricePerHour != nil {
		price = *in.PricePerHour
	}
	row := s.DB.QueryRowContext(ctx, `
INSERT INTO facility_courts
  (facility_id, sport_id, name, price_per_hour, open_time, close_time, status)
VALUES ($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),'active')
RETURNING id, facility_id, sport_id, name, price_per_hour,
          COALESCE(open_time,''), COALESCE(close_time,''), status`,
		facilityID, sport, name, price,
		strings.TrimSpace(in.OpenTime), strings.TrimSpace(in.CloseTime),
	)
	c, err := scanCourt(row)
	return &c, err
}

// SportCourtSyncIn is one sport + its courts from the Facility edit form.
type SportCourtSyncIn struct {
	SportID   int64             `json:"sportId"`
	SportName string            `json:"sportName"`
	Subs      []SportCourtSubIn `json:"subs"`
}

type SportCourtSubIn struct {
	CourtID     int64    `json:"courtId"`
	Name        string   `json:"subFacilityName"`
	NameAlt     string   `json:"name"`
	DayType     string   `json:"dayType"`
	OpeningTime string   `json:"openingTime"`
	ClosingTime string   `json:"closingTime"`
	Price       float64  `json:"price"`
	IsActive    *bool    `json:"isActive"`
}

func (sub SportCourtSubIn) courtName() string {
	n := strings.TrimSpace(sub.Name)
	if n == "" {
		n = strings.TrimSpace(sub.NameAlt)
	}
	return n
}

// SyncSportsCourts replaces active sports/courts for a facility from the admin form.
// Also mirrors into booking_courts and fills the rolling 30-day slot window.
func (s *Service) SyncSportsCourts(ctx context.Context, facilityID int64, sports []SportCourtSyncIn) error {
	if facilityID <= 0 {
		return fmt.Errorf("invalid_facility")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	keepSportPKs := map[int64]struct{}{}
	keepCourtIDs := map[int64]struct{}{}
	// Track names already claimed in this save so duplicate "Court 1" inserts a 2nd row.
	claimedNames := map[string]struct{}{}
	type bookingMirror struct {
		Name    string
		SportID int64
		Open    string
		Close   string
		Active  bool
	}
	var mirrors []bookingMirror
	minOpenH, maxCloseH := 24, 0

	for i, sp := range sports {
		name := strings.TrimSpace(sp.SportName)
		if name == "" {
			continue
		}
		catalogID := sp.SportID
		var pk int64
		err := tx.QueryRowContext(ctx, `
SELECT id FROM facility_sports
WHERE facility_id=$1 AND (
  ($2 > 0 AND catalog_sport_id=$2) OR LOWER(name)=LOWER($3)
)
ORDER BY CASE WHEN $2 > 0 AND catalog_sport_id=$2 THEN 0 ELSE 1 END
LIMIT 1`, facilityID, catalogID, name).Scan(&pk)
		if err == sql.ErrNoRows {
			var catalog interface{}
			if catalogID > 0 {
				catalog = catalogID
			}
			err = tx.QueryRowContext(ctx, `
INSERT INTO facility_sports (facility_id, name, sort_order, status, catalog_sport_id)
VALUES ($1,$2,$3,'active',$4)
RETURNING id`, facilityID, name, i, catalog).Scan(&pk)
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			_, err = tx.ExecContext(ctx, `
UPDATE facility_sports
SET name=$2, sort_order=$3, status='active',
    catalog_sport_id=COALESCE(NULLIF($4,0), catalog_sport_id),
    updated_at=NOW()
WHERE id=$1`, pk, name, i, catalogID)
			if err != nil {
				return err
			}
		}
		keepSportPKs[pk] = struct{}{}

		for _, sub := range sp.Subs {
			cname := sub.courtName()
			if cname == "" {
				continue
			}
			openT := normalizeClock(sub.OpeningTime)
			closeT := normalizeClock(sub.ClosingTime)
			active := true
			if sub.IsActive != nil {
				active = *sub.IsActive
			}
			st := "active"
			if !active {
				st = "inactive"
			}
			var cid int64
			matched := false
			// 1) Prefer stable courtId from the form (rename-safe).
			if sub.CourtID > 0 {
				err := tx.QueryRowContext(ctx, `
SELECT id FROM facility_courts
WHERE id=$1 AND facility_id=$2 AND sport_id=$3
LIMIT 1`, sub.CourtID, facilityID, pk).Scan(&cid)
				if err == nil {
					matched = true
				} else if err != sql.ErrNoRows {
					return err
				}
			}
			// 2) Else match by name only if that name is not already claimed this save.
			nameKey := fmt.Sprintf("%d|%s", pk, strings.ToLower(cname))
			if !matched {
				if _, taken := claimedNames[nameKey]; !taken {
					err := tx.QueryRowContext(ctx, `
SELECT id FROM facility_courts
WHERE facility_id=$1 AND sport_id=$2 AND LOWER(name)=LOWER($3)
LIMIT 1`, facilityID, pk, cname).Scan(&cid)
					if err == nil {
						matched = true
					} else if err != sql.ErrNoRows {
						return err
					}
				}
			}
			if matched {
				_, err = tx.ExecContext(ctx, `
UPDATE facility_courts
SET name=$2, price_per_hour=$3, open_time=NULLIF($4,''), close_time=NULLIF($5,''),
    status=$6, updated_at=NOW()
WHERE id=$1`, cid, cname, sub.Price, openT, closeT, st)
				if err != nil {
					return err
				}
			} else {
				err = tx.QueryRowContext(ctx, `
INSERT INTO facility_courts
  (facility_id, sport_id, name, price_per_hour, open_time, close_time, status)
VALUES ($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),$7)
RETURNING id`, facilityID, pk, cname, sub.Price, openT, closeT, st).Scan(&cid)
				if err != nil {
					return err
				}
			}
			claimedNames[nameKey] = struct{}{}
			keepCourtIDs[cid] = struct{}{}
			// Booking UI filters by catalog sportId (Hostinger / sports picker), not internal pk.
			bookSportID := catalogID
			if bookSportID <= 0 {
				bookSportID = pk
			}
			mirrors = append(mirrors, bookingMirror{
				Name: cname, SportID: bookSportID, Open: openT, Close: closeT, Active: active,
			})
			if oh, ok := parseHour(openT); ok && oh < minOpenH {
				minOpenH = oh
			}
			if closeT != "" {
				if ch := parseCloseHour(closeT); ch > maxCloseH {
					maxCloseH = ch
				}
			}
		}
	}

	rows, err := tx.QueryContext(ctx, `
SELECT id FROM facility_sports WHERE facility_id=$1 AND status='active'`, facilityID)
	if err != nil {
		return err
	}
	var dropSports []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if _, ok := keepSportPKs[id]; !ok {
			dropSports = append(dropSports, id)
		}
	}
	rows.Close()
	for _, id := range dropSports {
		_, _ = tx.ExecContext(ctx, `
UPDATE facility_sports SET status='inactive', updated_at=NOW() WHERE id=$1`, id)
	}

	rows2, err := tx.QueryContext(ctx, `
SELECT id FROM facility_courts WHERE facility_id=$1 AND status='active'`, facilityID)
	if err != nil {
		return err
	}
	var dropCourts []int64
	for rows2.Next() {
		var id int64
		if err := rows2.Scan(&id); err != nil {
			rows2.Close()
			return err
		}
		if _, ok := keepCourtIDs[id]; !ok {
			dropCourts = append(dropCourts, id)
		}
	}
	rows2.Close()
	for _, id := range dropCourts {
		_, _ = tx.ExecContext(ctx, `
UPDATE facility_courts SET status='inactive', updated_at=NOW() WHERE id=$1`, id)
	}

	// Mirror into booking_courts (Slots Setup / generate use this table).
	keepBooking := map[int64]struct{}{}
	bookingNameUsed := map[string]struct{}{}
	for _, m := range mirrors {
		bst := "active"
		if !m.Active {
			bst = "disabled"
		}
		bname := m.Name
		baseKey := strings.ToLower(bname)
		if _, taken := bookingNameUsed[baseKey]; taken {
			// Avoid collapsing two facility courts with the same display name.
			bname = fmt.Sprintf("%s (%d)", m.Name, len(bookingNameUsed)+1)
		}
		var bid int64
		err := tx.QueryRowContext(ctx, `
SELECT id FROM booking_courts
WHERE facility_id=$1 AND LOWER(name)=LOWER($2)
ORDER BY id ASC LIMIT 1`, facilityID, bname).Scan(&bid)
		if err == sql.ErrNoRows {
			err = tx.QueryRowContext(ctx, `
INSERT INTO booking_courts (facility_id, name, sport_id, status)
VALUES ($1,$2,$3,$4) RETURNING id`, facilityID, bname, m.SportID, bst).Scan(&bid)
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			_, err = tx.ExecContext(ctx, `
UPDATE booking_courts
SET name=$2, sport_id=$3, status=$4, updated_at=NOW()
WHERE id=$1`, bid, bname, m.SportID, bst)
			if err != nil {
				return err
			}
		}
		bookingNameUsed[strings.ToLower(bname)] = struct{}{}
		keepBooking[bid] = struct{}{}
	}
	bRows, err := tx.QueryContext(ctx, `
SELECT id FROM booking_courts WHERE facility_id=$1 AND status='active'`, facilityID)
	if err != nil {
		return err
	}
	var dropBooking []int64
	for bRows.Next() {
		var id int64
		if err := bRows.Scan(&id); err != nil {
			bRows.Close()
			return err
		}
		if _, ok := keepBooking[id]; !ok {
			dropBooking = append(dropBooking, id)
		}
	}
	bRows.Close()
	for _, id := range dropBooking {
		_, _ = tx.ExecContext(ctx, `
UPDATE booking_courts SET status='disabled', updated_at=NOW() WHERE id=$1`, id)
	}

	openH, closeH := 6, 22
	if minOpenH < 24 {
		openH = minOpenH
	}
	if maxCloseH > 0 {
		closeH = maxCloseH
	}
	if openH >= closeH {
		openH, closeH = 6, 22
	}
	_, _ = tx.ExecContext(ctx, `
INSERT INTO booking_facility_schedule (facility_id, open_hour, close_hour, slot_minutes, updated_at)
VALUES ($1,$2,$3,60,NOW())
ON CONFLICT (facility_id) DO UPDATE SET
  open_hour=EXCLUDED.open_hour, close_hour=EXCLUDED.close_hour, updated_at=NOW()`,
		facilityID, openH, closeH)

	if err := tx.Commit(); err != nil {
		return err
	}

	// Drop Free slots outside the new window, then fill rolling 30 days.
	_, _ = s.DB.ExecContext(ctx, `
DELETE FROM booking_slots
WHERE facility_id=$1
  AND status='available'
  AND (
    start_time < make_time($2,0,0)
    OR end_time > make_time($3,0,0)
    OR court_id NOT IN (
      SELECT id FROM booking_courts WHERE facility_id=$1 AND status='active'
    )
  )`, facilityID, openH, closeH)
	_, _ = s.generateBookingSlotsMonth(ctx, facilityID, openH, closeH, 60)
	// Proximity checkout reads app_price_list — keep it aligned with facility courts.
	s.syncAppPriceList(ctx, facilityID)
	return nil
}

// syncAppPriceList mirrors active facility_courts prices into app_price_list
// (WEEKDAY/WEEKEND) so proximity Razorpay checkout can charge.
func (s *Service) syncAppPriceList(ctx context.Context, facilityID int64) {
	if facilityID <= 0 {
		return
	}
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM app_price_list WHERE facility_id=$1`, facilityID)
	_, _ = s.DB.ExecContext(ctx, `
INSERT INTO app_price_list (facility_id, sport_id, court_name, day_type, price, advance_amount)
SELECT fc.facility_id,
       COALESCE(NULLIF(fs.catalog_sport_id,0), fs.id, 1),
       fc.name,
       d.day_type,
       COALESCE(NULLIF(fc.price_per_hour,0), 500),
       1
FROM facility_courts fc
JOIN facility_sports fs ON fs.id = fc.sport_id
CROSS JOIN (VALUES ('WEEKDAY'), ('WEEKEND')) AS d(day_type)
WHERE fc.facility_id=$1 AND LOWER(COALESCE(fc.status,'active'))='active'`, facilityID)
	_, _ = s.DB.ExecContext(ctx, `
INSERT INTO facility_sport_payments
  (facility_id, sport_id, allow_full_payment, allow_advance_payment, allow_spot_payment, advance_amount)
SELECT DISTINCT fc.facility_id,
       COALESCE(NULLIF(fs.catalog_sport_id,0), fs.id, 1),
       TRUE, TRUE, TRUE, 1
FROM facility_courts fc
JOIN facility_sports fs ON fs.id = fc.sport_id
WHERE fc.facility_id=$1 AND LOWER(COALESCE(fc.status,'active'))='active'
ON CONFLICT (facility_id, sport_id) DO UPDATE SET
  allow_full_payment=TRUE,
  allow_advance_payment=TRUE,
  allow_spot_payment=TRUE,
  advance_amount=1,
  updated_at=NOW()`, facilityID)
}

func normalizeClock(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	// Accept "6:00", "06:00", "06:00:00".
	parts := strings.Split(v, ":")
	if len(parts) < 2 {
		return v
	}
	h := strings.TrimSpace(parts[0])
	m := strings.TrimSpace(parts[1])
	if len(h) == 1 {
		h = "0" + h
	}
	if len(m) == 1 {
		m = "0" + m
	}
	if len(m) > 2 {
		m = m[:2]
	}
	return h + ":" + m
}

func parseHour(v string) (int, bool) {
	v = normalizeClock(v)
	if v == "" {
		return 0, false
	}
	var h, m int
	if _, err := fmt.Sscanf(v, "%d:%d", &h, &m); err != nil {
		return 0, false
	}
	if h < 0 || h > 24 {
		return 0, false
	}
	// Closing "23:30" → hour 24 window uses ceil for generate end exclusive.
	if m > 0 && h < 24 {
		// For close times, callers that want exclusive end use +1 separately.
		return h, true
	}
	return h, true
}

func parseCloseHour(v string) int {
	v = normalizeClock(v)
	var h, m int
	if _, err := fmt.Sscanf(v, "%d:%d", &h, &m); err != nil {
		return 22
	}
	if m > 0 && h < 24 {
		return h + 1
	}
	if h <= 0 {
		return 22
	}
	return h
}

// generateBookingSlotsMonth inserts available slots for today..today+29 (ON CONFLICT DO NOTHING).
func (s *Service) generateBookingSlotsMonth(ctx context.Context, facilityID int64, openH, closeH, mins int) (int, error) {
	if openH < 0 || closeH > 24 || openH >= closeH || mins < 15 {
		openH, closeH, mins = 6, 22, 60
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT id FROM booking_courts WHERE facility_id=$1 AND status='active' ORDER BY id`, facilityID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var courtIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		courtIDs = append(courtIDs, id)
	}
	if len(courtIDs) == 0 {
		return 0, nil
	}

	now := time.Now()
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	created := 0
	for d := 0; d < 30; d++ {
		day := from.AddDate(0, 0, d)
		date := day.Format("2006-01-02")
		for _, courtID := range courtIDs {
			for t := openH * 60; t+mins <= closeH*60; t += mins {
				start := fmt.Sprintf("%02d:%02d:00", t/60, t%60)
				endM := t + mins
				end := fmt.Sprintf("%02d:%02d:00", endM/60, endM%60)
				res, err := s.DB.ExecContext(ctx, `
INSERT INTO booking_slots (facility_id, court_id, slot_date, start_time, end_time, status)
VALUES ($1,$2,$3::date,$4::time,$5::time,'available')
ON CONFLICT (court_id, slot_date, start_time) DO NOTHING`,
					facilityID, courtID, date, start, end)
				if err != nil {
					return created, err
				}
				n, _ := res.RowsAffected()
				created += int(n)
			}
		}
	}
	return created, nil
}

func (s *Service) UpdateCourtStatus(ctx context.Context, facilityID, courtID int64, status string) (*Court, error) {
	st := strings.ToLower(strings.TrimSpace(status))
	if st != "active" && st != "inactive" {
		return nil, fmt.Errorf("invalid_status")
	}
	row := s.DB.QueryRowContext(ctx, `
UPDATE facility_courts SET status=$1, updated_at=NOW()
WHERE id=$2 AND facility_id=$3
RETURNING id, facility_id, sport_id, name, price_per_hour,
          COALESCE(open_time,''), COALESCE(close_time,''), status`, st, courtID, facilityID)
	c, err := scanCourt(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("not_found")
	}
	return &c, err
}

type courtScanner interface {
	Scan(dest ...interface{}) error
}

func scanCourt(row courtScanner) (Court, error) {
	var c Court
	var sport sql.NullInt64
	var price sql.NullFloat64
	err := row.Scan(&c.ID, &c.FacilityID, &sport, &c.Name, &price, &c.OpenTime, &c.CloseTime, &c.Status)
	if err != nil {
		return c, err
	}
	if sport.Valid {
		v := sport.Int64
		c.SportID = &v
	}
	if price.Valid {
		v := price.Float64
		c.PricePerHour = &v
	}
	return c, nil
}

// --- Payments ---

type PaymentSetting struct {
	ID                  int64   `json:"id,omitempty"`
	FacilityID          int64   `json:"facility_id"`
	SportID             int64   `json:"sport_id"` // 0 = facility-wide default
	AllowFullPayment    bool    `json:"allow_full_payment"`
	AllowAdvancePayment bool    `json:"allow_advance_payment"`
	AllowSpotPayment    bool    `json:"allow_spot_payment"`
	AdvanceAmount       float64 `json:"advance_amount"`
}

func (s *Service) ListPayments(ctx context.Context, facilityID int64) ([]PaymentSetting, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, facility_id, sport_id, allow_full_payment, allow_advance_payment,
       allow_spot_payment, advance_amount
FROM facility_sport_payments WHERE facility_id=$1 ORDER BY sport_id`, facilityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PaymentSetting{}
	for rows.Next() {
		var p PaymentSetting
		if err := rows.Scan(&p.ID, &p.FacilityID, &p.SportID, &p.AllowFullPayment, &p.AllowAdvancePayment,
			&p.AllowSpotPayment, &p.AdvanceAmount); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		out = append(out, PaymentSetting{
			FacilityID:          facilityID,
			SportID:             0,
			AllowFullPayment:    true,
			AllowAdvancePayment: true,
			AllowSpotPayment:    false,
		})
	}
	return out, rows.Err()
}

func (s *Service) UpsertPayment(ctx context.Context, facilityID int64, in PaymentSetting) (*PaymentSetting, error) {
	if facilityID <= 0 {
		return nil, fmt.Errorf("invalid_facility")
	}
	sportID := in.SportID
	if sportID < 0 {
		sportID = 0
	}
	var p PaymentSetting
	err := s.DB.QueryRowContext(ctx, `
INSERT INTO facility_sport_payments
  (facility_id, sport_id, allow_full_payment, allow_advance_payment, allow_spot_payment, advance_amount)
VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT (facility_id, sport_id) DO UPDATE SET
  allow_full_payment=EXCLUDED.allow_full_payment,
  allow_advance_payment=EXCLUDED.allow_advance_payment,
  allow_spot_payment=EXCLUDED.allow_spot_payment,
  advance_amount=EXCLUDED.advance_amount,
  updated_at=NOW()
RETURNING id, facility_id, sport_id, allow_full_payment, allow_advance_payment,
          allow_spot_payment, advance_amount`,
		facilityID, sportID, in.AllowFullPayment, in.AllowAdvancePayment, in.AllowSpotPayment, in.AdvanceAmount,
	).Scan(&p.ID, &p.FacilityID, &p.SportID, &p.AllowFullPayment, &p.AllowAdvancePayment, &p.AllowSpotPayment, &p.AdvanceAmount)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// --- Services / maintenance ---

type ServiceFlags struct {
	FacilityID       int64 `json:"facility_id"`
	StaffEnabled     bool  `json:"staff_enabled"`
	CustomerEnabled  bool  `json:"customer_enabled"`
}

func (s *Service) GetServices(ctx context.Context, facilityID int64) (*ServiceFlags, error) {
	var f ServiceFlags
	err := s.DB.QueryRowContext(ctx, `
SELECT facility_id, staff_enabled, customer_enabled FROM facility_service_flags WHERE facility_id=$1`,
		facilityID).Scan(&f.FacilityID, &f.StaffEnabled, &f.CustomerEnabled)
	if err == sql.ErrNoRows {
		return &ServiceFlags{FacilityID: facilityID, StaffEnabled: true, CustomerEnabled: true}, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func (s *Service) UpsertServices(ctx context.Context, facilityID int64, in ServiceFlags) (*ServiceFlags, error) {
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO facility_service_flags (facility_id, staff_enabled, customer_enabled, updated_at)
VALUES ($1,$2,$3,NOW())
ON CONFLICT (facility_id) DO UPDATE SET
  staff_enabled=EXCLUDED.staff_enabled,
  customer_enabled=EXCLUDED.customer_enabled,
  updated_at=NOW()`, facilityID, in.StaffEnabled, in.CustomerEnabled)
	if err != nil {
		return nil, err
	}
	return s.GetServices(ctx, facilityID)
}

// --- Staff (identity tables) ---

type StaffRow struct {
	MembershipID int64    `json:"membership_id"`
	UserID       int64    `json:"user_id"`
	FacilityID   int64    `json:"facility_id"`
	Email        string   `json:"email"`
	Name         string   `json:"name,omitempty"`
	Role         string   `json:"role"`
	PageAccess   []string `json:"page_access"`
	Status       string   `json:"status"`
}

type StaffInput struct {
	Email      string   `json:"email"`
	Name       string   `json:"name"`
	Role       string   `json:"role"`
	PageAccess []string `json:"page_access"`
}

func (s *Service) ListStaff(ctx context.Context, facilityID int64) ([]StaffRow, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT m.id, u.id, m.facility_id, u.email, COALESCE(u.full_name,''), m.role, m.page_access::text, m.status
FROM user_facility_memberships m
JOIN users u ON u.id = m.user_id
WHERE m.facility_id=$1
ORDER BY u.email`, facilityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StaffRow{}
	for rows.Next() {
		var r StaffRow
		var pageRaw sql.NullString
		if err := rows.Scan(&r.MembershipID, &r.UserID, &r.FacilityID, &r.Email, &r.Name, &r.Role, &pageRaw, &r.Status); err != nil {
			return nil, err
		}
		r.PageAccess = resolvePageAccess(r.Role, pageRaw)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) UpsertStaff(ctx context.Context, facilityID int64, in StaffInput) (*StaffRow, error) {
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if facilityID <= 0 || email == "" {
		return nil, fmt.Errorf("invalid_staff")
	}
	role := strings.ToLower(strings.TrimSpace(in.Role))
	if role == "" {
		role = "coach"
	}
	allowed := map[string]bool{
		"admin": true, "sub_admin": true, "headcoach": true, "coach": true, "tournament_admin": true,
	}
	if !allowed[role] {
		return nil, fmt.Errorf("invalid_role")
	}

	var userID int64
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM users WHERE lower(email)=$1`, email).Scan(&userID)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("user_not_found")
	}
	if err != nil {
		return nil, err
	}
	if name := strings.TrimSpace(in.Name); name != "" {
		_, _ = s.DB.ExecContext(ctx, `UPDATE users SET full_name=$1, updated_at=NOW() WHERE id=$2`, name, userID)
	}

	var pageJSON interface{}
	if len(in.PageAccess) > 0 {
		b, err := json.Marshal(in.PageAccess)
		if err != nil {
			return nil, err
		}
		pageJSON = string(b)
	}

	var membershipID int64
	err = s.DB.QueryRowContext(ctx, `
INSERT INTO user_facility_memberships (user_id, facility_id, role, page_access, status)
VALUES ($1,$2,$3,$4::jsonb,'active')
ON CONFLICT (user_id, facility_id) DO UPDATE SET
  role=EXCLUDED.role,
  page_access=EXCLUDED.page_access,
  status='active',
  updated_at=NOW()
RETURNING id`, userID, facilityID, role, pageJSON).Scan(&membershipID)
	if err != nil {
		return nil, err
	}
	return s.getStaff(ctx, membershipID)
}

func (s *Service) UpdateStaffStatus(ctx context.Context, facilityID, membershipID int64, status string) (*StaffRow, error) {
	st := strings.ToLower(strings.TrimSpace(status))
	if st != "active" && st != "disabled" {
		return nil, fmt.Errorf("invalid_status")
	}
	res, err := s.DB.ExecContext(ctx, `
UPDATE user_facility_memberships SET status=$1, updated_at=NOW()
WHERE id=$2 AND facility_id=$3`, st, membershipID, facilityID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("not_found")
	}
	return s.getStaff(ctx, membershipID)
}

func (s *Service) getStaff(ctx context.Context, membershipID int64) (*StaffRow, error) {
	var r StaffRow
	var pageRaw sql.NullString
	err := s.DB.QueryRowContext(ctx, `
SELECT m.id, u.id, m.facility_id, u.email, COALESCE(u.full_name,''), m.role, m.page_access::text, m.status
FROM user_facility_memberships m
JOIN users u ON u.id = m.user_id
WHERE m.id=$1`, membershipID).Scan(
		&r.MembershipID, &r.UserID, &r.FacilityID, &r.Email, &r.Name, &r.Role, &pageRaw, &r.Status,
	)
	if err != nil {
		return nil, err
	}
	r.PageAccess = resolvePageAccess(r.Role, pageRaw)
	return &r, nil
}

// --- Summary ---

type Summary struct {
	SportsActive   int  `json:"sports_active"`
	CourtsActive   int  `json:"courts_active"`
	StaffActive    int  `json:"staff_active"`
	StaffEnabled   bool `json:"staff_enabled"`
	CustomerEnabled bool `json:"customer_enabled"`
}

func (s *Service) Summary(ctx context.Context, facilityID int64) (*Summary, error) {
	var out Summary
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM facility_sports WHERE facility_id=$1 AND status='active'`, facilityID).Scan(&out.SportsActive)
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM facility_courts WHERE facility_id=$1 AND status='active'`, facilityID).Scan(&out.CourtsActive)
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_facility_memberships WHERE facility_id=$1 AND status='active'`, facilityID).Scan(&out.StaffActive)
	flags, err := s.GetServices(ctx, facilityID)
	if err != nil {
		return nil, err
	}
	out.StaffEnabled = flags.StaffEnabled
	out.CustomerEnabled = flags.CustomerEnabled
	return &out, nil
}
