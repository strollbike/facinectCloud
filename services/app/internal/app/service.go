package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/facinect/app/internal/config"
)

type Service struct {
	DB  *sql.DB
	Cfg config.Config
}

func strField(body map[string]any, key string) string {
	v, ok := body[key]
	if !ok || v == nil {
		return ""
	}
	s := strings.TrimSpace(fmt.Sprint(v))
	if s == "<nil>" {
		return ""
	}
	return s
}

func digitsOnly(s string) string {
	re := regexp.MustCompile(`\D+`)
	return re.ReplaceAllString(s, "")
}

func normalizeWA(raw string) string {
	d := digitsOnly(raw)
	if len(d) == 10 {
		d = "91" + d
	}
	if len(d) == 11 && strings.HasPrefix(d, "0") {
		d = "91" + d[1:]
	}
	return d
}

func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371.0
	toRad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat := toRad(lat2 - lat1)
	dLon := toRad(lon2 - lon1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(toRad(lat1))*math.Cos(toRad(lat2))*math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return R * c
}

func (s *Service) parseIDs(raw any) []int64 {
	out := []int64{}
	switch v := raw.(type) {
	case []any:
		for _, x := range v {
			n, _ := strconv.ParseInt(fmt.Sprint(x), 10, 64)
			if n > 0 {
				out = append(out, n)
			}
		}
	case []int64:
		return v
	}
	return out
}

func (s *Service) userRow(ctx context.Context, id int64, wa string) (map[string]any, error) {
	var (
		uid                int64
		name, whatsapp, ph string
		email              string
		accessed           []int64
	)
	q := `SELECT id, full_name, whatsapp_no, phone, email, COALESCE(accessed_facility_ids, '{}')::text
FROM app_users WHERE status='active' AND `
	var row *sql.Row
	if id > 0 {
		row = s.DB.QueryRowContext(ctx, q+`id=$1`, id)
	} else {
		row = s.DB.QueryRowContext(ctx, q+`whatsapp_no=$1`, normalizeWA(wa))
	}
	var arrStr string
	err := row.Scan(&uid, &name, &whatsapp, &ph, &email, &arrStr)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	accessed = parsePgIntArray(arrStr)
	if ph == "<nil>" {
		ph = ""
	}
	if email == "<nil>" {
		email = ""
	}
	return map[string]any{
		"exists":              true,
		"created":             false,
		"userId":              uid,
		"fullName":            name,
		"whatsappNo":          whatsapp,
		"phone":               ph,
		"email":               email,
		"accessedFacilityIds": accessed,
	}, nil
}

func parsePgIntArray(s string) []int64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "{}" {
		return []int64{}
	}
	s = strings.TrimPrefix(s, "{")
	s = strings.TrimSuffix(s, "}")
	parts := strings.Split(s, ",")
	out := make([]int64, 0, len(parts))
	for _, p := range parts {
		n, _ := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
		if n > 0 {
			out = append(out, n)
		}
	}
	return out
}

func formatPgIntArray(ids []int64) string {
	if len(ids) == 0 {
		return "{}"
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func (s *Service) UsersLookup(ctx context.Context, body map[string]any) (bool, string, any) {
	action := strings.ToLower(strings.TrimSpace(fmt.Sprint(body["action"])))
	switch action {
	case "lookup":
		wa := normalizeWA(strField(body, "whatsappNo"))
		u, err := s.userRow(ctx, 0, wa)
		if err != nil {
			return false, err.Error(), nil
		}
		if u == nil {
			return true, "Not found", map[string]any{
				"exists": false, "created": false, "userId": 0,
				"fullName": "", "whatsappNo": wa, "phone": "", "email": "",
				"accessedFacilityIds": []int64{},
			}
		}
		return true, "OK", u
	case "register":
		wa := normalizeWA(strField(body, "whatsappNo"))
		name := strField(body, "fullName")
		phone := strField(body, "phone")
		if phone == "" {
			phone = strField(body, "altPhone")
		}
		email := strField(body, "email")
		if wa == "" || name == "" {
			return false, "Name and WhatsApp required", nil
		}
		if existing, _ := s.userRow(ctx, 0, wa); existing != nil {
			existing["created"] = false
			return true, "Already registered", existing
		}
		var id int64
		err := s.DB.QueryRowContext(ctx, `
INSERT INTO app_users (full_name, whatsapp_no, phone, email)
VALUES ($1,$2,$3,$4) RETURNING id`, name, wa, phone, email).Scan(&id)
		if err != nil {
			return false, err.Error(), nil
		}
		u, _ := s.userRow(ctx, id, "")
		if u != nil {
			u["created"] = true
		}
		return true, "Registered", u
	case "add_facility_access":
		uid, _ := strconv.ParseInt(fmt.Sprint(body["userId"]), 10, 64)
		fid, _ := strconv.ParseInt(fmt.Sprint(body["facilityId"]), 10, 64)
		u, err := s.userRow(ctx, uid, "")
		if err != nil || u == nil {
			return false, "User not found", nil
		}
		ids := s.parseIDs(u["accessedFacilityIds"])
		found := false
		for _, id := range ids {
			if id == fid {
				found = true
				break
			}
		}
		if !found && fid > 0 {
			ids = append(ids, fid)
			_, err = s.DB.ExecContext(ctx, `
UPDATE app_users SET accessed_facility_ids=$1::bigint[], updated_at=NOW() WHERE id=$2`,
				formatPgIntArray(ids), uid)
			if err != nil {
				return false, err.Error(), nil
			}
		}
		return true, "OK", map[string]any{"accessedFacilityIds": ids}
	case "delete_account":
		wa := normalizeWA(fmt.Sprint(body["whatsappNo"]))
		uid, _ := strconv.ParseInt(fmt.Sprint(body["userId"]), 10, 64)
		res, err := s.DB.ExecContext(ctx, `
UPDATE app_users SET status='deleted', updated_at=NOW()
WHERE ($1 > 0 AND id=$1) OR whatsapp_no=$2`, uid, wa)
		if err != nil {
			return false, err.Error(), nil
		}
		n, _ := res.RowsAffected()
		return n > 0, "Deleted", map[string]any{"deleted": n > 0}
	default:
		return false, "unknown_action", nil
	}
}

func (s *Service) FacilitiesNearby(ctx context.Context, body map[string]any) (bool, string, any) {
	action := strings.ToLower(strings.TrimSpace(fmt.Sprint(body["action"])))
	switch action {
	case "sports":
		rows, err := s.DB.QueryContext(ctx, `SELECT id, name FROM app_sports WHERE status='active' ORDER BY name`)
		if err != nil {
			return false, err.Error(), nil
		}
		defer rows.Close()
		list := []map[string]any{}
		for rows.Next() {
			var id int64
			var name string
			_ = rows.Scan(&id, &name)
			list = append(list, map[string]any{"sportId": id, "sportName": name})
		}
		return true, "OK", map[string]any{"sports": list}
	case "cities":
		return s.citiesPayload(ctx)
	case "all_cities":
		// City-wide facilities sorted by distance (Proximity city browse).
		return s.facilitiesCityDistance(ctx, body)
	case "facility_sports":
		fid, _ := strconv.ParseInt(fmt.Sprint(body["facilityId"]), 10, 64)
		rows, err := s.DB.QueryContext(ctx, `
SELECT s.id, s.name FROM app_facility_sports fs
JOIN app_sports s ON s.id=fs.sport_id
WHERE fs.facility_id=$1 ORDER BY s.name`, fid)
		if err != nil {
			return false, err.Error(), nil
		}
		defer rows.Close()
		list := []map[string]any{}
		for rows.Next() {
			var id int64
			var name string
			_ = rows.Scan(&id, &name)
			list = append(list, map[string]any{"sportId": id, "sportName": name})
		}
		return true, "OK", map[string]any{"sports": list}
	case "nearby", "search":
		lat, _ := strconv.ParseFloat(fmt.Sprint(body["lat"]), 64)
		lon, _ := strconv.ParseFloat(fmt.Sprint(body["lon"]), 64)
		radius, _ := strconv.ParseFloat(fmt.Sprint(body["radiusKm"]), 64)
		if radius <= 0 {
			radius = 25
		}
		sportID, _ := strconv.ParseInt(strField(body, "sportId"), 10, 64)
		q := strField(body, "q")
		if q == "" {
			q = strField(body, "city")
		}
		sportsByFac := s.allFacilitySportsMap(ctx)
		rows, err := s.DB.QueryContext(ctx, `
SELECT f.id, f.name, f.location, f.city_name, f.logo_url, f.latitude, f.longitude,
       COALESCE(f.geohash,''), COALESCE(f.geohash5,''),
       f.customer_ok, f.maintenance
FROM app_facilities f WHERE f.status='active'`)
		if err != nil {
			return false, err.Error(), nil
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var id int64
			var name, loc, city, logo, ghash, ghash5 string
			var flat, flon float64
			var custOK, maint bool
			_ = rows.Scan(&id, &name, &loc, &city, &logo, &flat, &flon, &ghash, &ghash5, &custOK, &maint)
			dist := 0.0
			missingCoords := flat == 0 && flon == 0
			if !missingCoords && (lat != 0 || lon != 0) {
				dist = haversineKm(lat, lon, flat, flon)
				if dist > radius {
					continue
				}
			} else if missingCoords {
				// Still list facilities without map pins (admin onboarded, coords unset).
				dist = 9999
			}
			if q != "" {
				ql := strings.ToLower(q)
				if !strings.Contains(strings.ToLower(name), ql) &&
					!strings.Contains(strings.ToLower(city), ql) &&
					!strings.Contains(strings.ToLower(loc), ql) {
					continue
				}
			}
			sports := sportsByFac[id]
			if sports == nil {
				sports = []map[string]any{}
			}
			if sportID > 0 {
				ok := false
				for _, sp := range sports {
					if sp["sportId"] == sportID {
						ok = true
						break
					}
				}
				if !ok {
					continue
				}
			}
			out = append(out, map[string]any{
				"facilityId":                id,
				"facilityName":              name,
				"location":                  loc,
				"cityName":                  city,
				"facilityLogoUrl":           logo,
				"latitude":                  flat,
				"longitude":                 flon,
				"geohash":                   ghash,
				"geohash5":                  ghash5,
				"distanceKm":                math.Round(dist*1000) / 1000, // metre precision
				"sports":                    sports,
				"customer_services_enabled": custOK,
				"maintenance":               maint,
			})
		}
		sort.SliceStable(out, func(i, j int) bool {
			di, _ := out[i]["distanceKm"].(float64)
			dj, _ := out[j]["distanceKm"].(float64)
			return di < dj
		})
		return true, "OK", map[string]any{"facilities": out, "geoReady": true}
	default:
		return false, "unknown_action", nil
	}
}

// facilitiesCityDistance: city-wide facilities ordered by km from lat/lon (no radius cut).
// If city filter matches nothing (empty/mismatched city_name), fall back to nearest
// facilities from the city center so IP/city browse is never empty when data exists.
func (s *Service) facilitiesCityDistance(ctx context.Context, body map[string]any) (bool, string, any) {
	lat, _ := strconv.ParseFloat(fmt.Sprint(body["lat"]), 64)
	lon, _ := strconv.ParseFloat(fmt.Sprint(body["lon"]), 64)
	sportID, _ := strconv.ParseInt(strField(body, "sportId"), 10, 64)
	cityQ := strings.ToLower(strings.TrimSpace(strField(body, "city")))
	limit, _ := strconv.Atoi(fmt.Sprint(body["limit"]))
	if limit <= 0 {
		limit = 120
	}
	sportsByFac := s.allFacilitySportsMap(ctx)
	rows, err := s.DB.QueryContext(ctx, `
SELECT f.id, f.name, f.location, f.city_name, f.logo_url, f.latitude, f.longitude,
       COALESCE(f.geohash,''), COALESCE(f.geohash5,''),
       f.customer_ok, f.maintenance
FROM app_facilities f WHERE f.status='active'`)
	if err != nil {
		return false, err.Error(), nil
	}
	defer rows.Close()
	matched := []map[string]any{}
	all := []map[string]any{}
	for rows.Next() {
		var id int64
		var name, loc, city, logo, ghash, ghash5 string
		var flat, flon float64
		var custOK, maint bool
		_ = rows.Scan(&id, &name, &loc, &city, &logo, &flat, &flon, &ghash, &ghash5, &custOK, &maint)
		sports := sportsByFac[id]
		if sports == nil {
			sports = []map[string]any{}
		}
		if sportID > 0 {
			ok := false
			for _, sp := range sports {
				if sp["sportId"] == sportID {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}
		dist := 0.0
		if lat != 0 || lon != 0 {
			dist = haversineKm(lat, lon, flat, flon)
		}
		row := map[string]any{
			"facilityId":                id,
			"facilityName":              name,
			"location":                  loc,
			"cityName":                  city,
			"facilityLogoUrl":           logo,
			"latitude":                  flat,
			"longitude":                 flon,
			"geohash":                   ghash,
			"geohash5":                  ghash5,
			"distanceKm":                math.Round(dist*1000) / 1000, // metre precision
			"sports":                    sports,
			"customer_services_enabled": custOK,
			"maintenance":               maint,
		}
		all = append(all, row)
		if cityQ == "" || cityMatchesQuery(cityQ, city, loc, name) {
			matched = append(matched, row)
		}
	}
	out := matched
	scope := "city"
	if cityQ != "" && len(matched) == 0 {
		// city_name often blank in DB — show nearest from city center instead.
		out = all
		scope = "city_fallback_distance"
	}
	sort.SliceStable(out, func(i, j int) bool {
		di, _ := out[i]["distanceKm"].(float64)
		dj, _ := out[j]["distanceKm"].(float64)
		return di < dj
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return true, "OK", map[string]any{
		"facilities": out,
		"count":      len(out),
		"geoReady":   true,
		"city":       strField(body, "city"),
		"scope":      scope,
	}
}

func cityMatchesQuery(cityQ, cityName, location, facilityName string) bool {
	if cityQ == "" {
		return true
	}
	cl := strings.ToLower(strings.TrimSpace(cityName))
	ll := strings.ToLower(location)
	nl := strings.ToLower(facilityName)
	if cl == cityQ ||
		strings.Contains(cl, cityQ) ||
		(cl != "" && strings.Contains(cityQ, cl)) {
		return true
	}
	if strings.Contains(ll, cityQ) || strings.Contains(nl, cityQ) {
		return true
	}
	return false
}

func (s *Service) facilitySports(ctx context.Context, fid int64) []map[string]any {
	rows, err := s.DB.QueryContext(ctx, `
SELECT s.id, s.name FROM app_facility_sports fs
JOIN app_sports s ON s.id=fs.sport_id WHERE fs.facility_id=$1`, fid)
	if err != nil {
		return nil
	}
	defer rows.Close()
	list := []map[string]any{}
	for rows.Next() {
		var id int64
		var name string
		_ = rows.Scan(&id, &name)
		list = append(list, map[string]any{"sportId": id, "sportName": name})
	}
	return list
}

// allFacilitySportsMap loads sports for every facility in one query (avoids N+1).
func (s *Service) allFacilitySportsMap(ctx context.Context) map[int64][]map[string]any {
	out := map[int64][]map[string]any{}
	rows, err := s.DB.QueryContext(ctx, `
SELECT fs.facility_id, s.id, s.name
FROM app_facility_sports fs
JOIN app_sports s ON s.id = fs.sport_id
ORDER BY fs.facility_id, s.name`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var fid, sid int64
		var name string
		if err := rows.Scan(&fid, &sid, &name); err != nil {
			continue
		}
		out[fid] = append(out[fid], map[string]any{"sportId": sid, "sportName": name})
	}
	return out
}

func (s *Service) citiesPayload(ctx context.Context) (bool, string, any) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, name, state_name, latitude, longitude FROM app_cities ORDER BY name`)
	if err != nil {
		return false, err.Error(), nil
	}
	defer rows.Close()

	// One areas query — group by city (avoids N+1 per city).
	areasByCity := map[int64][]map[string]any{}
	if arows, aerr := s.DB.QueryContext(ctx, `
SELECT city_id, id, name, latitude, longitude FROM app_areas ORDER BY city_id, name`); aerr == nil {
		for arows.Next() {
			var cid, aid int64
			var aname string
			var alat, alon float64
			if err := arows.Scan(&cid, &aid, &aname, &alat, &alon); err != nil {
				continue
			}
			areasByCity[cid] = append(areasByCity[cid], map[string]any{
				"areaId": aid, "areaName": aname, "name": aname,
				"latitude": alat, "longitude": alon, "aliases": []string{},
			})
		}
		arows.Close()
	}

	cities := []map[string]any{}
	for rows.Next() {
		var id int64
		var name, state string
		var lat, lon float64
		_ = rows.Scan(&id, &name, &state, &lat, &lon)
		areas := areasByCity[id]
		if areas == nil {
			areas = []map[string]any{}
		}
		cities = append(cities, map[string]any{
			"cityId": id, "cityName": name, "stateName": state,
			"latitude": lat, "longitude": lon, "areas": areas,
		})
	}
	return true, "OK", map[string]any{"cities": cities, "geoCitiesReady": true}
}

func (s *Service) GetSlots(ctx context.Context, facilityID, sportID, userID int64, date, whatsappNo string) (bool, string, any) {
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	if facilityID <= 0 || sportID <= 0 {
		return false, "facilityId and sportId are required.", nil
	}
	// Resolve OTP login user from WhatsApp when userId omitted.
	if userID <= 0 {
		userID = s.resolveUserIDFromWhatsApp(ctx, whatsappNo)
	}
	s.releaseExpiredHolds(ctx, facilityID)

	// Prefer staff booking_slots (StrollX) so inventory matches admin.
	list, fromBooking := s.slotsFromBookingInventory(ctx, facilityID, sportID, date)
	if !fromBooking {
		s.ensureSlotsForDay(ctx, facilityID, sportID, date)
		list = s.slotsFromAppTable(ctx, facilityID, sportID, date)
	} else {
		// Overlay consumer OTP holds (app_slots onhold) onto staff inventory.
		s.overlayAppHolds(ctx, facilityID, sportID, date, list)
	}
	_ = userID // held_by already on each row; client filters selectable with session userId
	return true, "OK", list
}

func (s *Service) resolveUserIDFromWhatsApp(ctx context.Context, whatsappNo string) int64 {
	wa := normalizeWA(whatsappNo)
	if wa == "" {
		return 0
	}
	u, err := s.userRow(ctx, 0, wa)
	if err != nil || u == nil {
		return 0
	}
	id, _ := strconv.ParseInt(fmt.Sprint(u["userId"]), 10, 64)
	return id
}

// resolveCheckoutUserID prefers body userId; else WhatsApp OTP login user (app_users).
func (s *Service) resolveCheckoutUserID(ctx context.Context, body map[string]any, uid int64) int64 {
	if uid > 0 {
		return uid
	}
	wa := s.resolvePortalWhatsApp(ctx, body)
	return s.resolveUserIDFromWhatsApp(ctx, wa)
}

func (s *Service) slotsFromAppTable(ctx context.Context, facilityID, sportID int64, date string) []map[string]any {
	rows, err := s.DB.QueryContext(ctx, `
SELECT facility_id, sport_id, court_name, slot_date::text, time_range, status, held_by_user_id
FROM app_slots WHERE facility_id=$1 AND sport_id=$2 AND slot_date=$3::date
ORDER BY court_name, time_range`, facilityID, sportID, date)
	if err != nil {
		return nil
	}
	defer rows.Close()
	list := []map[string]any{}
	for rows.Next() {
		var fid, sid int64
		var court, d, tr, st string
		var held sql.NullInt64
		_ = rows.Scan(&fid, &sid, &court, &d, &tr, &st, &held)
		if pastFreeSlot(d, st, timeRangeEnd(tr), time.Now().In(bookingTZ())) {
			continue
		}
		item := map[string]any{
			"facilityId": fid, "sportId": sid, "courtName": court,
			"date": d, "timeRange": tr, "status": strings.ToLower(st),
			"held_by_userId": 0, "heldByUserId": 0,
		}
		if held.Valid {
			item["held_by_userId"] = held.Int64
			item["heldByUserId"] = held.Int64
		}
		list = append(list, item)
	}
	return list
}

// slotsFromBookingInventory mirrors StrollX booking_slots for the consumer app.
func (s *Service) slotsFromBookingInventory(ctx context.Context, facilityID, sportID int64, date string) ([]map[string]any, bool) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT s.facility_id,
       COALESCE(NULLIF(c.sport_id,0), $3) AS sport_id,
       c.name,
       s.slot_date::text,
       to_char(s.start_time, 'HH24:MI') || '-' || to_char(s.end_time, 'HH24:MI'),
       LOWER(s.status)
FROM booking_slots s
JOIN booking_courts c ON c.id = s.court_id
WHERE s.facility_id=$1 AND s.slot_date=$2::date
  AND ($3=0 OR c.sport_id IS NULL OR c.sport_id=0 OR c.sport_id=$3)
ORDER BY c.name, s.start_time`, facilityID, date, sportID)
	if err != nil {
		return nil, false
	}
	defer rows.Close()
	list := []map[string]any{}
	saw := false
	now := time.Now().In(bookingTZ())
	for rows.Next() {
		var fid, sid int64
		var court, d, tr, st string
		if rows.Scan(&fid, &sid, &court, &d, &tr, &st) != nil {
			continue
		}
		saw = true
		st = strings.ToLower(strings.TrimSpace(st))
		// Consumer UI uses onhold; staff payment-hold uses held.
		if st == "held" {
			st = "onhold"
		}
		if pastFreeSlot(d, st, timeRangeEnd(tr), now) {
			continue
		}
		list = append(list, map[string]any{
			"facilityId": fid, "sportId": sid, "courtName": court,
			"date": d, "timeRange": tr, "status": st,
			"held_by_userId": 0, "heldByUserId": 0,
		})
	}
	return list, saw
}

func timeRangeEnd(tr string) string {
	parts := strings.Split(tr, "-")
	if len(parts) < 2 {
		return ""
	}
	return strings.TrimSpace(parts[len(parts)-1])
}

// freeSlotHideLead removes today's free slots this long before they end.
const freeSlotHideLead = 15 * time.Minute

// pastFreeSlot is true for free inventory that has ended, or that ends within 15 minutes.
// Booked and blocked stay.
func pastFreeSlot(date, status, end string, now time.Time) bool {
	st := strings.ToLower(strings.TrimSpace(status))
	if st != "available" && st != "free" {
		return false
	}
	d := strings.TrimSpace(date)
	if len(d) >= 10 {
		d = d[:10]
	}
	today := now.Format("2006-01-02")
	if d == "" || d > today {
		return false
	}
	if d < today {
		return true
	}
	e := strings.TrimSpace(end)
	if len(e) == 5 {
		e += ":00"
	}
	if len(e) >= 8 {
		e = e[:8]
	}
	if e == "" {
		return false
	}
	endAt, err := time.ParseInLocation("2006-01-02 15:04:05", d+" "+e, now.Location())
	if err != nil {
		return false
	}
	return !endAt.After(now.Add(freeSlotHideLead))
}

func (s *Service) overlayAppHolds(ctx context.Context, facilityID, sportID int64, date string, list []map[string]any) {
	if len(list) == 0 {
		return
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT court_name, time_range, COALESCE(held_by_user_id,0)
FROM app_slots
WHERE facility_id=$1 AND sport_id=$2 AND slot_date=$3::date
  AND LOWER(status)='onhold'`, facilityID, sportID, date)
	if err != nil {
		return
	}
	defer rows.Close()
	holds := map[string]int64{}
	for rows.Next() {
		var court, tr string
		var held int64
		if rows.Scan(&court, &tr, &held) != nil {
			continue
		}
		holds[strings.ToLower(court)+"|"+tr] = held
	}
	for _, item := range list {
		court := strings.ToLower(fmt.Sprint(item["courtName"]))
		tr := fmt.Sprint(item["timeRange"])
		held, ok := holds[court+"|"+tr]
		if !ok {
			continue
		}
		st := strings.ToLower(fmt.Sprint(item["status"]))
		if st == "available" || st == "onhold" {
			item["status"] = "onhold"
			item["held_by_userId"] = held
			item["heldByUserId"] = held
		}
	}
}

func (s *Service) MarkBooked(ctx context.Context, body map[string]any) (bool, string, any) {
	fid, _ := strconv.ParseInt(fmt.Sprint(body["facilityId"]), 10, 64)
	sid, _ := strconv.ParseInt(fmt.Sprint(body["sportId"]), 10, 64)
	uid, _ := strconv.ParseInt(fmt.Sprint(body["userId"]), 10, 64)
	notes := strings.TrimSpace(fmt.Sprint(body["notes"]))
	price, _ := strconv.ParseFloat(fmt.Sprint(body["totalPrice"]), 64)
	slotsRaw, _ := body["slotsToBook"].([]any)
	if slotsRaw == nil {
		slotsRaw, _ = body["slots"].([]any)
	}
	slotsJSON, _ := json.Marshal(slotsRaw)
	var bid int64
	err := s.DB.QueryRowContext(ctx, `
INSERT INTO app_bookings (facility_id, sport_id, user_id, notes, amount, slots_json, payment_mode, formatted_time, raw_timestamp)
VALUES ($1,$2,$3,$4,$5,$6::jsonb,'spot',$7,NOW()) RETURNING id`,
		fid, sid, uid, notes, price, string(slotsJSON), fmt.Sprint(slotsRaw)).Scan(&bid)
	if err != nil {
		return false, err.Error(), nil
	}
	for _, sl := range slotsRaw {
		m, _ := sl.(map[string]any)
		if m == nil {
			continue
		}
		court := firstNonEmpty(fmt.Sprint(m["court"]), fmt.Sprint(m["courtName"]), fmt.Sprint(m["subFacilityName"]))
		date := firstNonEmpty(fmt.Sprint(m["date"]), fmt.Sprint(m["slotDate"]), fmt.Sprint(m["slot_date"]))
		tr := firstNonEmpty(fmt.Sprint(m["time"]), fmt.Sprint(m["timeRange"]), fmt.Sprint(m["time_range"]))
		sport := sid
		if v, _ := strconv.ParseInt(fmt.Sprint(m["sportId"]), 10, 64); v > 0 {
			sport = v
		}
		if court == "" || date == "" || tr == "" {
			continue
		}
		_, _ = s.DB.ExecContext(ctx, `
UPDATE app_slots
SET status='booked', held_by_user_id=NULL, onhold_update_time=NULL
WHERE facility_id=$1 AND ($2=0 OR sport_id=$2)
  AND court_name=$3 AND slot_date=$4::date AND time_range=$5`,
			fid, sport, court, date, tr)
	}
	s.mirrorAppBookingToLedger(ctx, fid, sid, uid, bid, price, "spot", slotsRaw)
	_, _, _ = s.UsersLookup(ctx, map[string]any{"action": "add_facility_access", "userId": uid, "facilityId": fid})
	return true, "Booked", map[string]any{"bookingId": bid}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v != "" && v != "<nil>" {
			return v
		}
	}
	return ""
}

func (s *Service) BookingCheckout(ctx context.Context, body map[string]any) (bool, string, any, int) {
	action := strings.ToLower(strings.TrimSpace(fmt.Sprint(body["action"])))
	fid, _ := strconv.ParseInt(fmt.Sprint(body["facilityId"]), 10, 64)
	sportID, _ := strconv.ParseInt(fmt.Sprint(body["sportId"]), 10, 64)
	uid, _ := strconv.ParseInt(fmt.Sprint(body["userId"]), 10, 64)
	// All checkout mutations bind to WhatsApp OTP login user (app_users).
	uid = s.resolveCheckoutUserID(ctx, body, uid)

	var blocked bool
	_ = s.DB.QueryRowContext(ctx, `SELECT booking_blocked FROM app_facilities WHERE id=$1`, fid).Scan(&blocked)
	if blocked && (action == "initiate_payment" || action == "confirm_booking" || action == "payment_options" || action == "set_slots_onhold" || action == "release_hold") {
		return false, "Bookings temporarily unavailable", map[string]any{"booking_blocked": true}, 402
	}

	switch action {
	case "set_slots_onhold":
		if uid <= 0 {
			return false, "Sign in with WhatsApp OTP to hold slots.", nil, 401
		}
		slots, _ := body["slots"].([]any)
		if err := s.holdSlotsForUser(ctx, fid, sportID, uid, slots); err != nil {
			return false, err.Error(), nil, 409
		}
		return true, "Slots held.", map[string]any{
			"facilityId": fid, "userId": uid, "holdMinutes": int(slotHoldTTL.Minutes()),
		}, 200
	case "release_hold":
		if uid <= 0 {
			return false, "Sign in with WhatsApp OTP to release holds.", nil, 401
		}
		slots, _ := body["slots"].([]any)
		s.releaseUserHolds(ctx, fid, sportID, uid, slots)
		return true, "Hold released.", map[string]any{"facilityId": fid, "userId": uid}, 200
	case "payment_options":
		rows, err := s.DB.QueryContext(ctx, `
SELECT court_name, day_type, price, advance_amount FROM app_price_list
WHERE facility_id=$1 AND sport_id=$2`, fid, sportID)
		if err != nil {
			return false, err.Error(), nil, 400
		}
		defer rows.Close()
		plist := []map[string]any{}
		adv := 1.0
		for rows.Next() {
			var court, day string
			var price, advance float64
			_ = rows.Scan(&court, &day, &price, &advance)
			if advance > 0 {
				adv = advance
			}
			plist = append(plist, map[string]any{
				"subFacilityName": court, "dayType": day, "price": price, "advance_amount": advance,
			})
		}
		if len(plist) == 0 {
			plist = append(plist, map[string]any{
				"subFacilityName": "Court 1", "dayType": "WEEKDAY", "price": 500, "advance_amount": 1,
			})
		}
		for i := range plist {
			if dt, ok := plist[i]["dayType"].(string); ok {
				plist[i]["dayType"] = normalizePriceDayType(dt)
			}
		}
		allowFull, allowAdv, allowSpot, sportAdv := s.loadSportPaymentFlags(ctx, fid, sportID)
		if sportAdv > 0 {
			adv = sportAdv
		}
		af, aa, aspot := 1, 1, 1
		if !allowFull {
			af = 0
		}
		if !allowAdv {
			aa = 0
		}
		if !allowSpot {
			aspot = 0
		}
		return true, "OK", map[string]any{
			"allow_full_payment": af, "allow_advance_payment": aa, "allow_spot_payment": aspot,
			"advance_amount": adv, "Booking_advance_amount": adv,
			"payment_source": "app", "priceList": plist,
		}, 200
	case "quote":
		slots, _ := body["slots"].([]any)
		mode := fmt.Sprint(body["paymentMode"])
		coupon := strField(body, "couponCode")
		q, qerr := s.buildCheckoutQuote(ctx, fid, sportID, uid, slots, mode, coupon)
		if qerr != nil {
			return false, qerr.Error(), nil, 400
		}
		return true, "OK", quoteToMap(q), 200
	case "validate_coupon":
		code := strField(body, "couponCode")
		if code == "" {
			return false, "facilityId and couponCode are required.", nil, 400
		}
		disc, isReg, isUser, err := s.resolveCouponDiscount(ctx, fid, uid, code)
		if err != nil {
			return false, "Invalid coupon", nil, 400
		}
		ctype := s.couponCType(ctx, fid, code)
		source := "PROMO"
		offerType := "PROMO"
		if isUser {
			ctype = "flat"
			source = "COUPON"
			offerType = "COUPON"
			if strings.HasPrefix(strings.ToUpper(code), "REF-") {
				offerType = "COUPON"
			} else {
				// Admin CreateCoupons MEMBER (booking_coupons / app_user_coupons).
				source = "MEMBER"
				offerType = "MEMBER"
			}
		}
		if isReg {
			ctype = "reg"
			source = "REG_OFFER"
			offerType = "REG_OFFER"
		}
		if strings.EqualFold(ctype, "percent") || strings.EqualFold(ctype, "percentage") {
			offerType = "PERCENT"
		}
		return true, "Coupon applied.", map[string]any{
			"discount": disc, "type": ctype, "offerType": offerType,
			"source": source, "couponCode": strings.ToUpper(code),
		}, 200
	case "get_available_coupons":
		list := []map[string]any{}
		seen := map[string]bool{}
		rows, err := s.DB.QueryContext(ctx, `
SELECT code, value, ctype, label, description FROM app_coupons
WHERE facility_id=$1 AND status='active' AND code NOT ILIKE 'REF-%'`, fid)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var code, ctype, label, desc string
				var value float64
				if rows.Scan(&code, &value, &ctype, &label, &desc) != nil {
					continue
				}
				up := strings.ToUpper(strings.TrimSpace(code))
				seen[up] = true
				offerType := "PROMO"
				if strings.EqualFold(ctype, "percent") || strings.EqualFold(ctype, "percentage") {
					offerType = "PERCENT"
				}
				list = append(list, map[string]any{
					"code": code, "value": value, "type": offerType,
					"ctype": ctype, "label": label, "description": desc, "source": "PROMO",
				})
			}
		}
		// User refund / MEMBER coupons (app_user_coupons dual-write from CreateCoupons).
		if uid > 0 {
			urow, uerr := s.DB.QueryContext(ctx, `
SELECT coupon_code, amount, COALESCE(expiry_date::text,'') FROM app_user_coupons
WHERE user_id=$1 AND facility_id=$2
  AND (expiry_date IS NULL OR expiry_date >= CURRENT_DATE)`, uid, fid)
			if uerr == nil {
				defer urow.Close()
				for urow.Next() {
					var code, exp string
					var amt float64
					if urow.Scan(&code, &amt, &exp) != nil {
						continue
					}
					up := strings.ToUpper(strings.TrimSpace(code))
					if seen[up] {
						continue
					}
					seen[up] = true
					label, source, offerType := "Refund coupon", "COUPON", "COUPON"
					desc := "Cancellation refund"
					if !strings.HasPrefix(up, "REF-") {
						label, source, offerType = "Member coupon", "MEMBER", "MEMBER"
						desc = "Thank-you discount"
					}
					list = append(list, map[string]any{
						"code": code, "value": amt, "type": offerType,
						"label": label, "description": desc,
						"source": source, "expiryDate": exp,
					})
				}
			}
			// Fallback: MEMBER only in booking_coupons (failed dual-write / older rows).
			brow, berr := s.DB.QueryContext(ctx, `
SELECT code, discount, COALESCE(valid_until::text,'') FROM booking_coupons
WHERE facility_id=$1 AND user_id=$2 AND status='active'
  AND (valid_until IS NULL OR valid_until >= CURRENT_DATE)`, fid, uid)
			if berr == nil {
				defer brow.Close()
				for brow.Next() {
					var code, exp string
					var amt float64
					if brow.Scan(&code, &amt, &exp) != nil {
						continue
					}
					up := strings.ToUpper(strings.TrimSpace(code))
					if seen[up] {
						continue
					}
					seen[up] = true
					list = append(list, map[string]any{
						"code": code, "value": amt, "type": "MEMBER",
						"label": "Member coupon", "description": "Thank-you discount",
						"source": "MEMBER", "expiryDate": exp,
					})
				}
			}
		}
		return true, "OK", map[string]any{"coupons": list, "count": len(list)}, 200
	case "initiate_payment":
		if uid <= 0 {
			return false, "Sign in with WhatsApp OTP to pay.", nil, 401
		}
		mode := strings.ToLower(strings.TrimSpace(fmt.Sprint(body["paymentMode"])))
		slots, _ := body["slots"].([]any)
		coupon := strField(body, "couponCode")
		q, qerr := s.buildCheckoutQuote(ctx, fid, sportID, uid, slots, mode, coupon)
		if qerr != nil {
			return false, qerr.Error(), nil, 400
		}
		if !q.ModeAllowed {
			return false, "Selected payment mode is not allowed.", nil, 400
		}
		amount := q.Payable
		if amount <= 0 {
			return false, "Nothing to charge online for this mode. Use confirm_booking for spot/free.", nil, 400
		}
		if v, err := strconv.ParseFloat(fmt.Sprint(body["amount"]), 64); err == nil && v > 0 {
			if absFloat(v-amount) <= 1 {
				amount = v
			}
		}
		if err := s.holdSlotsForUser(ctx, fid, sportID, uid, slots); err != nil {
			return false, err.Error(), nil, 409
		}
		orderID, keyID, amt, oerr := s.createPaymentOrder(ctx, amount, fmt.Sprintf("book_%d_%d", fid, uid), "BOOKING", map[string]any{
			"userId": uid, "facilityId": fid, "sportId": sportID, "mode": q.PaymentMode, "couponCode": q.CouponCode,
		})
		if oerr != nil && orderID == "" {
			s.releaseUserHolds(ctx, fid, sportID, uid, slots)
			return false, oerr.Error(), nil, 400
		}
		paise := int64(amt*100 + 0.5)
		if paise < 100 && amt > 0 {
			paise = 100
		}
		out := quoteToMap(q)
		out["orderId"] = orderID
		out["amount"] = amt
		out["keyId"] = keyID
		out["currency"] = "INR"
		out["amountInr"] = amt
		out["amountPaise"] = paise
		out["holdMinutes"] = int(slotHoldTTL.Minutes())
		return true, "OK", out, 200
	case "confirm_booking":
		if uid <= 0 {
			return false, "Sign in with WhatsApp OTP to confirm booking.", nil, 401
		}
		slots, _ := body["slots"].([]any)
		method := strings.TrimSpace(fmt.Sprint(body["paymentMethod"]))
		mode := normalizePayMode(fmt.Sprint(body["paymentMode"]), method)
		txn := strings.TrimSpace(fmt.Sprint(body["razorpayPaymentId"]))
		coupon := strField(body, "couponCode")
		if txn == "" {
			return false, "razorpayPaymentId is required.", nil, 400
		}
		q, qerr := s.buildCheckoutQuote(ctx, fid, sportID, uid, slots, mode, coupon)
		if qerr != nil {
			return false, qerr.Error(), nil, 400
		}
		if !q.ModeAllowed {
			return false, "Selected payment mode is not allowed.", nil, 400
		}
		mode = q.PaymentMode
		paid := q.Payable
		orderID := strings.TrimSpace(fmt.Sprint(body["razorpayOrderId"]))
		signature := strings.TrimSpace(fmt.Sprint(body["razorpaySignature"]))
		methodLower := strings.ToLower(method)
		if methodLower == "online" || (paid > 0 && !strings.HasPrefix(strings.ToUpper(txn), "SPOT_") && !strings.HasPrefix(strings.ToUpper(txn), "FREE_")) {
			secret := strings.TrimSpace(s.Cfg.RazorpayKeySecret)
			if secret != "" && !strings.Contains(s.Cfg.RazorpayKeyID, "test_local") {
				if orderID == "" || signature == "" {
					return false, "razorpayOrderId and razorpaySignature are required for online payment.", nil, 400
				}
				if !verifyRazorpaySignature(orderID, txn, signature, secret) {
					return false, "Invalid payment signature.", nil, 400
				}
			}
		}
		// Idempotent: same transaction already confirmed.
		var existingID int64
		_ = s.DB.QueryRowContext(ctx, `
SELECT id FROM app_bookings WHERE transaction_id=$1 AND user_id=$2 LIMIT 1`, txn, uid).Scan(&existingID)
		if existingID > 0 {
			ok, _, access := s.UsersLookup(ctx, map[string]any{"action": "add_facility_access", "userId": uid, "facilityId": fid})
			accessed := []int64{}
			if ok {
				if m, ok2 := access.(map[string]any); ok2 {
					accessed = s.parseIDs(m["accessedFacilityIds"])
				}
			}
			return true, "Already confirmed.", map[string]any{
				"bookingId": existingID, "transactionId": txn, "facilityId": fid,
				"accessedFacilityIds": accessed, "finalAmount": paid, "totalBill": q.TotalBill,
				"balanceDue": q.BalanceDue, "paymentMode": mode, "discount": q.Discount,
			}, 200
		}
		if err := s.holdSlotsForUser(ctx, fid, sportID, uid, slots); err != nil {
			return false, err.Error(), nil, 409
		}
		slotsJSON, _ := json.Marshal(slots)
		meta := bookingMetaFromSlots(slots)
		slotStart := meta.Start
		if slotStart.IsZero() {
			slotStart = time.Now()
		}
		ftime := meta.FormattedTime
		if ftime == "" {
			ftime = meta.TimesJoined
		}
		notes := "Booking via App"
		if q.IsRegOffer {
			notes = "Reg Offer Applied: " + q.CouponCode
		} else if q.CouponCode != "" {
			notes = "Coupon: " + q.CouponCode
		}
		if method == "" {
			if mode == "spot" {
				method = "Spot"
			} else if mode == "free" {
				method = "Coupon"
			} else {
				method = "Online"
			}
		}
		var bid int64
		err := s.DB.QueryRowContext(ctx, `
INSERT INTO app_bookings (
  facility_id, sport_id, user_id, payment_mode, payment_method, transaction_id,
  amount, notes, slots_json, formatted_time, formatted_date, court_names, raw_timestamp
)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10,$11,$12,$13) RETURNING id`,
			fid, sportID, uid, mode, method, txn, paid, notes, string(slotsJSON),
			ftime, meta.FormattedDate, meta.Courts, slotStart).Scan(&bid)
		if err != nil {
			return false, err.Error(), nil, 400
		}
		s.markSlotsBookedFromKeys(ctx, fid, sportID, slots)
		s.mirrorAppBookingToLedger(ctx, fid, sportID, uid, bid, paid, mode, slots)
		s.consumeCheckoutCoupon(ctx, fid, uid, q, bid)
		ok, _, access := s.UsersLookup(ctx, map[string]any{"action": "add_facility_access", "userId": uid, "facilityId": fid})
		accessed := []int64{}
		if ok {
			if m, ok2 := access.(map[string]any); ok2 {
				accessed = s.parseIDs(m["accessedFacilityIds"])
			}
		}
		// Server payable drives WA template (PHP notifyBookingConfirmation).
		s.notifyBookingConfirmation(ctx, uid, fid, bid, slots, mode, txn, paid, q.Discount, q.TotalBill, q.BalanceDue)
		return true, "Confirmed", map[string]any{
			"bookingId": bid, "transactionId": txn, "facilityId": fid,
			"accessedFacilityIds": accessed,
			"finalAmount":         paid, "totalBill": q.TotalBill,
			"balanceDue": q.BalanceDue, "paymentMode": mode, "discount": q.Discount,
		}, 200
	default:
		return false, "unknown_action", nil, 400
	}
}

func (s *Service) MyBookings(ctx context.Context, body map[string]any) (bool, string, any) {
	action := strings.ToLower(strings.TrimSpace(fmt.Sprint(body["action"])))
	uid, _ := strconv.ParseInt(fmt.Sprint(body["userId"]), 10, 64)
	switch action {
	case "facilities":
		waHint := fmt.Sprint(body["whatsappNo"])
		u, _ := s.userRow(ctx, uid, waHint)
		ids := []int64{}
		if u != nil {
			ids = s.parseIDs(u["accessedFacilityIds"])
		}
		// Include facilities that only have StrollX payment holds for this WhatsApp.
		pendingHolds := s.pendingPaymentHoldsForUser(ctx, uid, 0, waHint)
		holdCountByFid := map[int64]int{}
		holdNextByFid := map[int64]int64{}
		seenFid := map[int64]bool{}
		for _, id := range ids {
			seenFid[id] = true
		}
		for _, h := range pendingHolds {
			fid, _ := strconv.ParseInt(fmt.Sprint(h["facilityId"]), 10, 64)
			if fid <= 0 {
				continue
			}
			holdCountByFid[fid]++
			if ts, _ := strconv.ParseInt(fmt.Sprint(h["raw_timestamp"]), 10, 64); ts > 0 {
				if cur, ok := holdNextByFid[fid]; !ok || ts < cur {
					holdNextByFid[fid] = ts
				}
			}
			if !seenFid[fid] {
				seenFid[fid] = true
				ids = append(ids, fid)
			}
		}
		now := time.Now().In(bookingTZ())
		facilities := []map[string]any{}
		upcomingFacilities := []map[string]any{}
		historyFacilities := []map[string]any{}
		otherFacilities := []map[string]any{}
		for _, fid := range ids {
			var name string
			var maint bool
			_ = s.DB.QueryRowContext(ctx, `SELECT name, maintenance FROM app_facilities WHERE id=$1`, fid).Scan(&name, &maint)
			rows, err := s.DB.QueryContext(ctx, `
SELECT status, raw_timestamp, slots_json FROM app_bookings
WHERE user_id=$1 AND facility_id=$2`, uid, fid)
			upc, hist := 0, 0
			var nextTs, lastTs int64
			if err == nil {
				for rows.Next() {
					var status string
					var ts time.Time
					var slotsJSON []byte
					if rows.Scan(&status, &ts, &slotsJSON) != nil {
						continue
					}
					start := bookingMetaFromSlotsJSON(slotsJSON).Start
					if start.IsZero() {
						start = ts
					}
					if isUpcomingBooking(status, start, now) {
						upc++
						unix := start.Unix()
						if nextTs == 0 || unix < nextTs {
							nextTs = unix
						}
					} else {
						hist++
						unix := start.Unix()
						if unix > lastTs {
							lastTs = unix
						}
					}
				}
				rows.Close()
			}
			if hc := holdCountByFid[fid]; hc > 0 {
				upc += hc
				if hts := holdNextByFid[fid]; hts > 0 && (nextTs == 0 || hts < nextTs) {
					nextTs = hts
				}
			}
			group := "other"
			if upc > 0 {
				group = "upcoming"
			} else if hist > 0 {
				group = "history"
			}
			item := map[string]any{
				"facilityId": fid, "facilityName": name,
				"upcomingCount": upc, "historyCount": hist,
				"nextBookingAt": nil, "lastBookingAt": nil,
				"nextTimestamp": nextTs, "lastTimestamp": lastTs,
				"group": group, "maintenance": maint,
				"customer_services_enabled": !maint,
			}
			if nextTs > 0 {
				item["nextBookingAt"] = utcISO(time.Unix(nextTs, 0))
			}
			if lastTs > 0 {
				item["lastBookingAt"] = utcISO(time.Unix(lastTs, 0))
			}
			facilities = append(facilities, item)
			switch group {
			case "upcoming":
				upcomingFacilities = append(upcomingFacilities, item)
			case "history":
				historyFacilities = append(historyFacilities, item)
			default:
				otherFacilities = append(otherFacilities, item)
			}
		}
		sort.SliceStable(upcomingFacilities, func(i, j int) bool {
			ti, _ := upcomingFacilities[i]["nextTimestamp"].(int64)
			tj, _ := upcomingFacilities[j]["nextTimestamp"].(int64)
			if ti == 0 {
				return false
			}
			if tj == 0 {
				return true
			}
			return ti < tj
		})
		sort.SliceStable(historyFacilities, func(i, j int) bool {
			ti, _ := historyFacilities[i]["lastTimestamp"].(int64)
			tj, _ := historyFacilities[j]["lastTimestamp"].(int64)
			return ti > tj
		})
		return true, "Facilities retrieved.", map[string]any{
			"accessedFacilityIds": ids,
			"facilities":          facilities,
			"upcomingFacilities":  upcomingFacilities,
			"historyFacilities":   historyFacilities,
			"otherFacilities":     otherFacilities,
			"count":               len(facilities),
		}
	case "list":
		fid, _ := strconv.ParseInt(fmt.Sprint(body["facilityId"]), 10, 64)
		rows, err := s.DB.QueryContext(ctx, `
SELECT b.id, b.facility_id, COALESCE(f.name,''), b.sport_id, b.status,
       b.formatted_time, b.formatted_date, b.raw_timestamp, b.court_names, b.slots_json
FROM app_bookings b
LEFT JOIN app_facilities f ON f.id=b.facility_id
WHERE b.user_id=$1 AND ($2=0 OR b.facility_id=$2)
ORDER BY b.id DESC`, uid, fid)
		if err != nil {
			return false, err.Error(), nil
		}
		defer rows.Close()
		upcoming, history := []map[string]any{}, []map[string]any{}
		upcomingGroups := map[string][]map[string]any{}
		now := time.Now().In(bookingTZ())
		for rows.Next() {
			var id, facilityID, sportID int64
			var fname, status, ftime, fdate, courts string
			var ts time.Time
			var slotsJSON []byte
			_ = rows.Scan(&id, &facilityID, &fname, &sportID, &status, &ftime, &fdate, &ts, &courts, &slotsJSON)
			meta := bookingMetaFromSlotsJSON(slotsJSON)
			start := meta.Start
			if start.IsZero() {
				start = ts
			}
			// Prefer slot-derived display (old rows stored fmt.Sprint(slots) junk).
			if meta.Courts != "" {
				courts = meta.Courts
			}
			if meta.TimesJoined != "" {
				ftime = meta.TimesJoined
			} else if meta.FormattedTime != "" {
				ftime = meta.FormattedTime
			}
			if meta.FormattedDate != "" {
				fdate = meta.FormattedDate
			}
			sportName := s.sportName(ctx, sportID)
			item := bookingListItem(id, facilityID, sportID, fname, sportName, status, start, courts, ftime, fdate)
			if isUpcomingBooking(status, start, now) {
				upcoming = append(upcoming, item)
				upcomingGroups[sportName] = append(upcomingGroups[sportName], item)
			} else {
				history = append(history, item)
			}
		}
		// StrollX payment holds for this WhatsApp → awaiting payment in Upcoming.
		waHint := fmt.Sprint(body["whatsappNo"])
		for _, hold := range s.pendingPaymentHoldsForUser(ctx, uid, fid, waHint) {
			upcoming = append(upcoming, hold)
			sportName := strings.TrimSpace(fmt.Sprint(hold["sportName"]))
			if sportName == "" {
				sportName = "Booking"
			}
			upcomingGroups[sportName] = append(upcomingGroups[sportName], hold)
		}
		sortBookingsByStart(upcoming, true)
		sortBookingsByStart(history, false)
		coupons := []map[string]any{}
		seenC := map[string]bool{}
		crows, _ := s.DB.QueryContext(ctx, `
SELECT coupon_code, amount, COALESCE(expiry_date::text,''), facility_id FROM app_user_coupons WHERE user_id=$1`, uid)
		if crows != nil {
			defer crows.Close()
			for crows.Next() {
				var code, exp string
				var amt float64
				var cfid int64
				_ = crows.Scan(&code, &amt, &exp, &cfid)
				up := strings.ToUpper(strings.TrimSpace(code))
				seenC[fmt.Sprintf("%d:%s", cfid, up)] = true
				var cname string
				_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(name,'') FROM app_facilities WHERE id=$1`, cfid).Scan(&cname)
				source := "COUPON"
				if !strings.HasPrefix(up, "REF-") {
					source = "MEMBER"
				}
				coupons = append(coupons, map[string]any{
					"couponCode": code, "amount": amt, "expiryDate": exp,
					"facilityId": cfid, "facilityName": cname, "source": source,
				})
			}
		}
		// MEMBER rows only in booking_coupons (CreateCoupons PHP parity).
		bcrows, _ := s.DB.QueryContext(ctx, `
SELECT code, discount, COALESCE(valid_until::text,''), facility_id
FROM booking_coupons
WHERE user_id=$1 AND status='active'
  AND (valid_until IS NULL OR valid_until >= CURRENT_DATE)`, uid)
		if bcrows != nil {
			defer bcrows.Close()
			for bcrows.Next() {
				var code, exp string
				var amt float64
				var cfid int64
				_ = bcrows.Scan(&code, &amt, &exp, &cfid)
				key := fmt.Sprintf("%d:%s", cfid, strings.ToUpper(strings.TrimSpace(code)))
				if seenC[key] {
					continue
				}
				seenC[key] = true
				var cname string
				_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(name,'') FROM app_facilities WHERE id=$1`, cfid).Scan(&cname)
				coupons = append(coupons, map[string]any{
					"couponCode": code, "amount": amt, "expiryDate": exp,
					"facilityId": cfid, "facilityName": cname, "source": "MEMBER",
				})
			}
		}
		return true, "Bookings retrieved.", map[string]any{
			"upcoming": upcoming, "history": history,
			"upcomingGroups": upcomingGroups, "coupons": coupons,
			"countUpcoming": len(upcoming), "countHistory": len(history),
			"countCoupons": len(coupons),
		}
	case "cancel":
		bid, _ := strconv.ParseInt(fmt.Sprint(body["bookingId"]), 10, 64)
		reqFid, _ := strconv.ParseInt(fmt.Sprint(body["facilityId"]), 10, 64)
		if bid <= 0 || uid <= 0 {
			return false, "facilityId, bookingId, and userId are required.", nil
		}
		var facilityID, sportID int64
		var status, fname, notes, payMode string
		var paidAmount float64
		var ts time.Time
		var slotsJSON []byte
		err := s.DB.QueryRowContext(ctx, `
SELECT b.id, b.facility_id, b.sport_id, b.status, b.raw_timestamp, b.slots_json,
       COALESCE(f.name,''), COALESCE(b.amount,0), COALESCE(b.notes,''),
       COALESCE(b.payment_mode,'')
FROM app_bookings b
LEFT JOIN app_facilities f ON f.id=b.facility_id
WHERE b.id=$1 AND b.user_id=$2`, bid, uid).
			Scan(&bid, &facilityID, &sportID, &status, &ts, &slotsJSON, &fname, &paidAmount, &notes, &payMode)
		if err != nil {
			return false, "Booking not found.", nil
		}
		priorCoupon := couponCodeFromNotes(notes)
		if reqFid > 0 && facilityID != reqFid {
			return false, "Booking not found for this facility.", nil
		}
		if !strings.EqualFold(status, "confirmed") {
			return false, "Booking already cancelled.", nil
		}
		start := bookingMetaFromSlotsJSON(slotsJSON).Start
		if start.IsZero() {
			start = ts
		}
		now := time.Now().In(bookingTZ())
		if !isUpcomingBooking(status, start, now) {
			return false, "Too late to cancel (must be 15+ minutes before start).", nil
		}
		_, err = s.DB.ExecContext(ctx, `
UPDATE app_bookings SET status='cancelled', cancelled_at=NOW()
WHERE id=$1 AND user_id=$2 AND status='confirmed'`, bid, uid)
		if err != nil {
			return false, "Cancellation failed.", nil
		}
		s.releaseBookingSlots(ctx, facilityID, sportID, slotsJSON)
		s.cancelLedgerForAppBooking(ctx, facilityID, bid)
		// Spot / free → no refund coupon (PHP: transaction_amount > 0 only).
		if strings.EqualFold(payMode, "spot") || strings.EqualFold(payMode, "free") {
			paidAmount = 0
		}
		couponCode, couponAmt, expiry := s.mintCancelRefundCoupon(ctx, uid, facilityID, bid, paidAmount, notes, priorCoupon)
		s.notifyBookingCancelled(ctx, uid, facilityID, bid, fname, slotsJSON, couponCode, couponAmt)
		var coupon any
		if couponCode != "" {
			coupon = couponCode
		} else {
			coupon = nil
		}
		out := map[string]any{
			"facilityId": facilityID, "bookingId": strconv.FormatInt(bid, 10),
			"facilityName": fname, "coupon": coupon, "amount": couponAmt,
		}
		if expiry != "" {
			out["expiry"] = expiry
		}
		return true, "Booking cancelled.", out
	default:
		return false, "Unknown action. Use: facilities, list, cancel", nil
	}
}

func (s *Service) FacilityAccess(ctx context.Context, body map[string]any) (bool, string, any, int) {
	fid, _ := strconv.ParseInt(fmt.Sprint(body["facilityId"]), 10, 64)
	var allowed, blocked, maint bool
	err := s.DB.QueryRowContext(ctx, `
SELECT customer_ok, booking_blocked, maintenance FROM app_facilities WHERE id=$1`, fid).
		Scan(&allowed, &blocked, &maint)
	if err != nil {
		return true, "OK", map[string]any{"allowed": true, "booking_blocked": false}, 200
	}
	ok := allowed && !blocked && !maint
	code := 200
	if !ok {
		code = 402
	}
	return ok, "OK", map[string]any{"allowed": ok, "booking_blocked": blocked || maint}, code
}

func (s *Service) FacilityFeedback(ctx context.Context, body map[string]any) (bool, string, any) {
	fid, _ := strconv.ParseInt(fmt.Sprint(body["facilityId"]), 10, 64)
	uid, _ := strconv.ParseInt(fmt.Sprint(body["userId"]), 10, 64)
	rating, _ := strconv.Atoi(fmt.Sprint(body["rating"]))
	msg := strings.TrimSpace(fmt.Sprint(body["message"]))
	wa := normalizeWA(fmt.Sprint(body["whatsappNo"]))
	if rating < 1 || rating > 5 || fid <= 0 {
		return false, "Invalid rating", nil
	}
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO app_feedback (facility_id, user_id, rating, message, whatsapp_no)
VALUES ($1,$2,$3,$4,$5)`, fid, nullInt(uid), rating, msg, wa)
	if err != nil {
		return false, err.Error(), nil
	}
	return true, "Thanks for your feedback", map[string]any{}
}

func nullInt(v int64) any {
	if v <= 0 {
		return nil
	}
	return v
}

func (s *Service) FCMRegister(ctx context.Context, body map[string]any) (bool, string, any) {
	action := strings.ToLower(strings.TrimSpace(fmt.Sprint(body["action"])))
	if action == "" {
		action = "register"
	}
	token := strings.TrimSpace(fmt.Sprint(body["token"]))
	if action == "unregister" {
		if token == "" {
			return false, "token required.", nil
		}
		_, _ = s.DB.ExecContext(ctx, `DELETE FROM app_fcm_devices WHERE token=$1`, token)
		_, _ = s.DB.ExecContext(ctx, `UPDATE device_tokens SET status='disabled', updated_at=NOW() WHERE token=$1`, token)
		return true, "Token removed.", nil
	}
	if action == "status" {
		uid, _ := strconv.ParseInt(fmt.Sprint(body["userId"]), 10, 64)
		tokenCount := 0
		if uid > 0 {
			_ = s.DB.QueryRowContext(ctx, `
SELECT COUNT(DISTINCT token) FROM app_fcm_devices WHERE user_id=$1 AND token<>''`, uid).Scan(&tokenCount)
		}
		s.ensureFCMAudienceMap(ctx)
		return true, "OK", map[string]any{
			"tokenTable":      true,
			"tokensForUser":   tokenCount,
			"audienceMapRows": s.audienceMapCount(ctx, uid),
			"roleRows":        s.audienceMapCount(ctx, uid),
		}
	}
	uid, _ := strconv.ParseInt(fmt.Sprint(body["userId"]), 10, 64)
	if uid <= 0 || token == "" {
		return false, "userId and token are required.", nil
	}
	platform := strings.ToLower(strings.TrimSpace(fmt.Sprint(body["platform"])))
	if platform == "" {
		platform = "android"
	}
	pushApp := "proximity"
	if strings.EqualFold(strings.TrimSpace(fmt.Sprint(body["app"])), "strollx") {
		pushApp = "strollx"
	}
	deviceID := strField(body, "deviceId")
	if deviceID == "" {
		deviceID = strField(body, "device_id")
	}
	syncRoles := parseSyncRoles(body)
	s.ensureFCMAudienceMap(ctx)
	_, _ = s.DB.ExecContext(ctx, `
ALTER TABLE device_tokens ADD COLUMN IF NOT EXISTS app VARCHAR(16) NOT NULL DEFAULT 'proximity'`)
	if pushApp == "strollx" {
		// StrollX login id is not an app_users id. Keep the token out of Proximity.
		_, _ = s.DB.ExecContext(ctx, `DELETE FROM app_fcm_devices WHERE token=$1`, token)
		_, err := s.DB.ExecContext(ctx, `
INSERT INTO device_tokens (user_id, token, platform, status, app)
VALUES ($1, $2, $3, 'active', 'strollx')
ON CONFLICT (user_id, token) DO UPDATE
SET platform=EXCLUDED.platform, status='active', app='strollx', updated_at=NOW()`,
			uid, token, platform)
		if err != nil {
			return false, err.Error(), nil
		}
		return true, "FCM token registered.", map[string]any{
			"ok": true, "userId": uid, "app": "strollx",
		}
	}
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO app_fcm_devices (user_id, token, platform, device_id, updated_at)
VALUES ($1,$2,$3,$4,NOW())
ON CONFLICT (token) DO UPDATE SET user_id=EXCLUDED.user_id, platform=EXCLUDED.platform,
  device_id=EXCLUDED.device_id, updated_at=NOW()`, uid, token, platform, deviceID)
	if err != nil {
		return false, err.Error(), nil
	}
	_, _ = s.DB.ExecContext(ctx, `
INSERT INTO device_tokens (user_id, token, platform, status, app)
VALUES ($1, $2, $3, 'active', 'proximity')
ON CONFLICT (user_id, token) DO UPDATE
SET platform=EXCLUDED.platform, status='active', app='proximity', updated_at=NOW()`,
		uid, token, platform)

	s.upsertFCMAudience(ctx, fcmAudienceUser, uid, uid, "")
	if syncRoles {
		s.syncAudienceForUser(ctx, uid)
	}
	tokenRows := 0
	_ = s.DB.QueryRowContext(ctx, `
SELECT COUNT(DISTINCT token) FROM app_fcm_devices WHERE user_id=$1 AND token<>''`, uid).Scan(&tokenRows)
	audienceRows := s.audienceMapCount(ctx, uid)
	return true, "FCM token registered.", map[string]any{
		"ok":              true,
		"userId":          uid,
		"userType":        fcmAudienceUser,
		"deviceTokens":    tokenRows,
		"audienceMapRows": audienceRows,
		"roleRows":        audienceRows,
		"syncRoles":       syncRoles,
	}
}

func (s *Service) AppNotifications(ctx context.Context, body map[string]any) (bool, string, any) {
	action := strings.ToLower(strings.TrimSpace(fmt.Sprint(body["action"])))
	uid, _ := strconv.ParseInt(fmt.Sprint(body["userId"]), 10, 64)
	if uid <= 0 {
		if v, ok := parseInt64Field(body, "user_id"); ok {
			uid = v
		}
	}
	s.ensureNotificationSoftDelete(ctx)

	if action == "delete" || action == "remove" {
		if uid <= 0 {
			return false, "userId required.", nil
		}
		deleteAll := body["deleteAll"] == true || fmt.Sprint(body["deleteAll"]) == "true" ||
			body["markAll"] == true || fmt.Sprint(body["markAll"]) == "true"
		ids := notificationIDsFromBody(body)
		n, err := s.softDeleteNotifications(ctx, uid, ids, deleteAll)
		if err != nil {
			return false, err.Error(), nil
		}
		return true, "Deleted.", map[string]any{"ok": true, "deleted": n}
	}

	if action == "mark_seen" || action == "seen" {
		markAll := body["markAll"] == true || fmt.Sprint(body["markAll"]) == "true"
		if markAll {
			_, _ = s.DB.ExecContext(ctx, `
UPDATE app_notifications SET seen=TRUE
WHERE user_id=$1 AND deleted_at IS NULL`, uid)
			return true, "OK", map[string]any{"ok": true}
		}
		if raw, ok := body["items"].([]any); ok && len(raw) > 0 {
			ids := []int64{}
			for _, row := range raw {
				m, _ := row.(map[string]any)
				if m == nil {
					continue
				}
				id, _ := strconv.ParseInt(fmt.Sprint(m["id"]), 10, 64)
				if id > 0 {
					ids = append(ids, id)
				}
			}
			for _, id := range ids {
				_, _ = s.DB.ExecContext(ctx, `
UPDATE app_notifications SET seen=TRUE
WHERE user_id=$1 AND id=$2 AND deleted_at IS NULL`, uid, id)
			}
			return true, "OK", map[string]any{"ok": true, "count": len(ids)}
		}
		_, _ = s.DB.ExecContext(ctx, `
UPDATE app_notifications SET seen=TRUE
WHERE user_id=$1 AND deleted_at IS NULL`, uid)
		return true, "OK", map[string]any{"ok": true}
	}

	sinceID, _ := strconv.ParseInt(fmt.Sprint(body["sinceEventId"]), 10, 64)
	if sinceID == 0 {
		sinceID, _ = strconv.ParseInt(fmt.Sprint(body["since_event_id"]), 10, 64)
	}

	rows, err := s.DB.QueryContext(ctx, `
SELECT id, user_id, facility_id, booking_id, ntype, title, body, facility_name, facility_logo,
       seen, source, meta_json, created_at, expires_at
FROM app_notifications WHERE user_id=$1
  AND deleted_at IS NULL
  AND (expires_at IS NULL OR expires_at > NOW())
  AND ($2 <= 0 OR id > $2)
ORDER BY created_at DESC LIMIT 100`, uid, sinceID)
	if err != nil {
		return false, err.Error(), nil
	}
	defer rows.Close()
	items := []map[string]any{}
	events := []map[string]any{}
	unread := 0
	var latest int64
	for rows.Next() {
		var id, userID int64
		var fid sql.NullInt64
		var bookingID sql.NullString
		var ntype, title, nbody, fname, flogo, source string
		var seen bool
		var meta []byte
		var created time.Time
		var expires sql.NullTime
		_ = rows.Scan(&id, &userID, &fid, &bookingID, &ntype, &title, &nbody, &fname, &flogo, &seen, &source, &meta, &created, &expires)
		// Hide billing reminders once the user has paid; mark meta so deletes are allowed later.
		if strings.EqualFold(strings.TrimSpace(ntype), "billing_reminder") {
			if s.notificationBillingIsPaid(ctx, userID, fid, meta) {
				_, _ = s.DB.ExecContext(ctx, `
UPDATE app_notifications
SET meta_json = COALESCE(meta_json,'{}'::jsonb) || '{"isPaid":true,"requiresPayment":false}'::jsonb,
    deleted_at = COALESCE(deleted_at, NOW())
WHERE id=$1 AND user_id=$2 AND deleted_at IS NULL`, id, userID)
				continue
			}
		}
		if id > latest {
			latest = id
		}
		if !seen {
			unread++
		}
		item := map[string]any{
			"id": id, "userId": userID, "type": ntype, "title": title, "body": nbody,
			"facilityName": fname, "facilityLogoUrl": flogo, "seen": seen, "source": source,
			"createdAt":      utcISO(created),
			"createdAtLabel": created.In(bookingTZ()).Format("02 Jan, 3:04 PM"),
			"meta":           json.RawMessage(meta),
		}
		if expires.Valid {
			item["expiresAt"] = utcISO(expires.Time)
		}
		if fid.Valid {
			item["facilityId"] = fid.Int64
		}
		if bookingID.Valid {
			item["bookingId"] = bookingID.String
		}
		items = append(items, item)
		events = append(events, map[string]any{
			"eventId": id, "id": id, "type": ntype, "title": title, "body": nbody,
			"createdAt": utcISO(created),
		})
	}
	if action == "poll" {
		return true, "OK", map[string]any{
			"hasNew": len(events) > 0, "latestEventId": latest, "unreadCount": unread,
			"events": events,
		}
	}
	return true, "OK", map[string]any{
		"notifications": items, "unreadCount": unread, "latestEventId": latest, "days": 7,
	}
}

func (s *Service) LiveArena(ctx context.Context, action string, q map[string]string) (bool, string, any) {
	switch action {
	case "ping":
		return true, "OK", map[string]any{"file": "live_arena", "php": "go-app"}
	case "list":
		s.syncLiveFromTournaments(ctx)
		list := s.liveArenaList(ctx)
		return true, "OK", map[string]any{"tournaments": list}
	case "board":
		s.syncLiveFromTournaments(ctx)
		tid, _ := strconv.ParseInt(q["tourId"], 10, 64)
		category := strings.TrimSpace(q["category"])
		if name, sport, fname, fid, matches, afterID, ok := s.liveBoardFromTournaments(ctx, tid, category); ok {
			cats := []string{}
			seen := map[string]struct{}{}
			for _, m := range matches {
				c := strings.TrimSpace(fmt.Sprint(m["category"]))
				if c == "" {
					continue
				}
				if _, e := seen[c]; e {
					continue
				}
				seen[c] = struct{}{}
				cats = append(cats, c)
			}
			_ = fid
			return true, "OK", map[string]any{
				"tourId": tid, "tournamentName": name, "facilityName": fname, "sportName": sport,
				"category": category, "categories": cats, "afterId": afterID, "matches": matches,
				"source": "tournaments",
			}
		}
		var name, sport string
		var fid int64
		_ = s.DB.QueryRowContext(ctx, `
SELECT tournament_name, facility_id, sport_name FROM app_live_tournaments WHERE id=$1`, tid).
			Scan(&name, &fid, &sport)
		var fname string
		_ = s.DB.QueryRowContext(ctx, `SELECT name FROM app_facilities WHERE id=$1`, fid).Scan(&fname)
		qMatch := `
SELECT id, category, gender, player1, player2, status, winner, court, score1, score2
FROM app_live_matches WHERE tour_id=$1`
		args := []any{tid}
		if category != "" {
			qMatch += ` AND category=$2`
			args = append(args, category)
		}
		qMatch += ` ORDER BY id`
		rows, err := s.DB.QueryContext(ctx, qMatch, args...)
		if err != nil {
			return false, err.Error(), nil
		}
		defer rows.Close()
		matches := []map[string]any{}
		var afterID int64
		for rows.Next() {
			var id int64
			var cat, gender, p1, p2, status, winner, court string
			var s1, s2 int
			_ = rows.Scan(&id, &cat, &gender, &p1, &p2, &status, &winner, &court, &s1, &s2)
			if id > afterID {
				afterID = id
			}
			matches = append(matches, map[string]any{
				"id": id, "matchId": id, "category": cat, "gender": gender,
				"player1": p1, "player2": p2, "status": status, "winner": winner, "court": court,
				"live1": s1, "live2": s2, "score_details": fmt.Sprintf("%d-%d", s1, s2),
			})
		}
		return true, "OK", map[string]any{
			"tourId": tid, "tournamentName": name, "facilityName": fname, "sportName": sport,
			"category": category, "categories": []string{}, "afterId": afterID, "matches": matches,
			"source": "app_live",
		}
	default:
		return false, "unknown_action", nil
	}
}

func (s *Service) liveArenaList(ctx context.Context) []map[string]any {
	list := []map[string]any{}
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, tournament_name, tournament_date::text, facility_id, sport_name, categories::text, open_match_count
FROM app_live_tournaments ORDER BY tournament_date DESC, id DESC`)
	if err != nil {
		return list
	}
	defer rows.Close()
	for rows.Next() {
		var id, fid int64
		var name, date, sport string
		var open int
		var catStr string
		_ = rows.Scan(&id, &name, &date, &fid, &sport, &catStr, &open)
		cats := parsePgTextArray(catStr)
		var fname string
		_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(name,'') FROM app_facilities WHERE id=$1`, fid).Scan(&fname)
		if fname == "" {
			_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(name,'') FROM facilities WHERE id=$1`, fid).Scan(&fname)
		}
		list = append(list, map[string]any{
			"tourId": id, "tournamentName": name, "tournamentDate": date,
			"facilityId": fid, "facilityName": fname, "facilityLogoUrl": "",
			"sportName": sport, "categories": cats, "openMatchCount": open,
		})
	}
	return list
}

func parsePgTextArray(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" || s == "{}" {
		return []string{}
	}
	s = strings.TrimPrefix(s, "{")
	s = strings.TrimSuffix(s, "}")
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.Trim(strings.TrimSpace(p), `"`)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (s *Service) ScoreLive(ctx context.Context, action string, q map[string]string) (bool, string, any) {
	switch action {
	case "ping_public", "ping":
		return true, "OK", map[string]any{"ok": true}
	case "since_public", "since":
		s.syncLiveFromTournaments(ctx)
		after, _ := strconv.ParseInt(q["afterId"], 10, 64)
		mid, _ := strconv.ParseInt(q["matchId"], 10, 64)
		tourID, _ := strconv.ParseInt(q["tourId"], 10, 64)
		// Prefer tournament_score_events when present.
		if s.tableExists(ctx, "tournament_score_events") {
			var eid, matchID int64
			var s1, s2 int
			var status, winner sql.NullString
			err := s.DB.QueryRowContext(ctx, `
SELECT id, match_id, COALESCE(score1,0), COALESCE(score2,0), status, winner
FROM tournament_score_events
WHERE ($1=0 OR tournament_id=$1)
  AND ($2=0 OR match_id=$2)
  AND id > $3
ORDER BY id DESC LIMIT 1`, tourID, mid, after).Scan(&eid, &matchID, &s1, &s2, &status, &winner)
			if err == nil {
				ev := map[string]any{"matchId": matchID, "score1": s1, "score2": s2, "eventId": eid}
				if status.Valid {
					ev["status"] = status.String
				}
				if winner.Valid {
					ev["winner"] = winner.String
				}
				return true, "OK", map[string]any{
					"changed": true, "version": eid, "matchId": matchID, "score1": s1, "score2": s2,
					"events": []map[string]any{ev}, "afterId": eid, "source": "tournament_score_events",
				}
			}
		}
		var id int64
		var s1, s2 int
		var ver int64
		err := s.DB.QueryRowContext(ctx, `
SELECT id, score1, score2, version FROM app_live_matches
WHERE ($1=0 OR id=$1) AND ($2=0 OR tour_id=$2) AND id > $3
ORDER BY version DESC, id DESC LIMIT 1`, mid, tourID, after).Scan(&id, &s1, &s2, &ver)
		if err != nil {
			// Also peek tournament_fixtures updates by id.
			if mid > 0 {
				var fs1, fs2 int
				var st string
				err2 := s.DB.QueryRowContext(ctx, `
SELECT COALESCE(score1,0), COALESCE(score2,0), COALESCE(status,'')
FROM tournament_fixtures WHERE id=$1`, mid).Scan(&fs1, &fs2, &st)
				if err2 == nil {
					return true, "OK", map[string]any{
						"changed": true, "version": mid, "matchId": mid, "score1": fs1, "score2": fs2,
						"status":  mapFixtureStatus(st),
						"events":  []map[string]any{{"matchId": mid, "score1": fs1, "score2": fs2}},
						"afterId": mid, "source": "tournament_fixtures",
					}
				}
			}
			return true, "OK", map[string]any{"changed": false, "version": 0}
		}
		return true, "OK", map[string]any{
			"changed": true, "version": ver, "matchId": id, "score1": s1, "score2": s2,
			"events": []map[string]any{{"matchId": id, "score1": s1, "score2": s2}}, "afterId": id,
			"source": "app_live_matches",
		}
	case "wait_public", "wait":
		return s.ScoreLive(ctx, "since_public", q)
	default:
		return false, "unknown_action", nil
	}
}

func (s *Service) ScoreFollows(ctx context.Context, body map[string]any) (bool, string, any) {
	action := strings.ToLower(strings.TrimSpace(fmt.Sprint(body["action"])))
	uid, _ := strconv.ParseInt(fmt.Sprint(body["userId"]), 10, 64)
	targetType := firstNonEmpty(fmt.Sprint(body["targetType"]), fmt.Sprint(body["target_type"]))
	targetKey := firstNonEmpty(fmt.Sprint(body["targetKey"]), fmt.Sprint(body["target_key"]))
	switch action {
	case "prefs_get":
		var my, fr bool
		err := s.DB.QueryRowContext(ctx, `SELECT my_matches, friend_matches FROM app_score_prefs WHERE user_id=$1`, uid).Scan(&my, &fr)
		if err != nil {
			my, fr = true, true
		}
		return true, "OK", map[string]any{"myMatches": my, "friendMatches": fr}
	case "prefs_set":
		my := body["myMatches"] == true || fmt.Sprint(body["myMatches"]) == "true"
		fr := body["friendMatches"] == true || fmt.Sprint(body["friendMatches"]) == "true"
		_, _ = s.DB.ExecContext(ctx, `
INSERT INTO app_score_prefs (user_id, my_matches, friend_matches) VALUES ($1,$2,$3)
ON CONFLICT (user_id) DO UPDATE SET my_matches=$2, friend_matches=$3`, uid, my, fr)
		return true, "OK", map[string]any{"myMatches": my, "friendMatches": fr}
	case "follow":
		tid, _ := strconv.ParseInt(fmt.Sprint(body["tourId"]), 10, 64)
		mid, _ := strconv.ParseInt(fmt.Sprint(body["matchId"]), 10, 64)
		if strings.EqualFold(targetType, "match") && mid <= 0 {
			mid, _ = strconv.ParseInt(targetKey, 10, 64)
		}
		_, _ = s.DB.ExecContext(ctx, `
INSERT INTO app_score_follows (user_id, tour_id, match_id, target_type, target_key)
VALUES ($1,$2,$3,$4,$5)`, uid, tid, mid, targetType, targetKey)
		return true, "OK", nil
	case "unfollow":
		tid, _ := strconv.ParseInt(fmt.Sprint(body["tourId"]), 10, 64)
		mid, _ := strconv.ParseInt(fmt.Sprint(body["matchId"]), 10, 64)
		if targetKey != "" {
			_, _ = s.DB.ExecContext(ctx, `
DELETE FROM app_score_follows WHERE user_id=$1
  AND ($2=0 OR tour_id=$2)
  AND target_key=$3
  AND ($4='' OR target_type=$4)`, uid, tid, targetKey, targetType)
		} else if mid > 0 {
			_, _ = s.DB.ExecContext(ctx, `DELETE FROM app_score_follows WHERE user_id=$1 AND match_id=$2`, uid, mid)
		}
		return true, "OK", nil
	case "status_for_match":
		tid, _ := strconv.ParseInt(fmt.Sprint(body["tourId"]), 10, 64)
		mid, _ := strconv.ParseInt(fmt.Sprint(body["matchId"]), 10, 64)
		side1 := firstNonEmpty(fmt.Sprint(body["side1Key"]), fmt.Sprint(body["side1_key"]))
		side2 := firstNonEmpty(fmt.Sprint(body["side2Key"]), fmt.Sprint(body["side2_key"]))
		var watch int
		_ = s.DB.QueryRowContext(ctx, `
SELECT COUNT(*) FROM app_score_follows
WHERE user_id=$1 AND (
  (match_id=$2 AND $2>0) OR (target_type='match' AND target_key=$3)
)`, uid, mid, fmt.Sprint(mid)).Scan(&watch)
		var follow1, follow2 int
		if side1 != "" {
			_ = s.DB.QueryRowContext(ctx, `
SELECT COUNT(*) FROM app_score_follows
WHERE user_id=$1 AND ($2=0 OR tour_id=$2) AND target_key=$3`, uid, tid, side1).Scan(&follow1)
		}
		if side2 != "" {
			_ = s.DB.QueryRowContext(ctx, `
SELECT COUNT(*) FROM app_score_follows
WHERE user_id=$1 AND ($2=0 OR tour_id=$2) AND target_key=$3`, uid, tid, side2).Scan(&follow2)
		}
		_, _, pdata := s.ScoreFollows(ctx, map[string]any{"action": "prefs_get", "userId": uid})
		return true, "OK", map[string]any{
			"signedIn": uid > 0, "watchMatch": watch > 0,
			"followPlayer1": follow1 > 0, "followPlayer2": follow2 > 0,
			"side1Key": side1, "side2Key": side2, "prefs": pdata,
		}
	case "list":
		rows, err := s.DB.QueryContext(ctx, `
SELECT tour_id, match_id, target_type, target_key FROM app_score_follows WHERE user_id=$1`, uid)
		if err != nil {
			return false, err.Error(), nil
		}
		defer rows.Close()
		list := []map[string]any{}
		for rows.Next() {
			var tid, mid int64
			var tt, tk string
			_ = rows.Scan(&tid, &mid, &tt, &tk)
			list = append(list, map[string]any{
				"tourId": tid, "matchId": mid, "target_type": tt, "targetType": tt,
				"target_key": tk, "targetKey": tk,
			})
		}
		return true, "OK", map[string]any{"follows": list}
	default:
		return false, "unknown_action", nil
	}
}

func (s *Service) StudentPortal(ctx context.Context, body map[string]any) (bool, string, any) {
	action := strings.ToLower(strings.TrimSpace(fmt.Sprint(body["action"])))
	switch action {
	case "my_enrollments":
		return s.studentPortalMyEnrollments(ctx, body)
	case "reports":
		return s.studentPortalReports(ctx, body)
	case "month_calendar":
		return s.studentPortalMonthCalendar(ctx, body)
	case "leave_request":
		return s.studentPortalLeaveRequest(ctx, body)
	case "cancel_leave":
		return s.studentPortalCancelLeave(ctx, body)
	default:
		return false, "unknown_action", nil
	}
}

func (s *Service) PrimaryMembers(ctx context.Context, body map[string]any) (bool, string, any) {
	action := strings.ToLower(strings.TrimSpace(fmt.Sprint(body["action"])))
	switch action {
	case "my_plans":
		return s.primaryMembersMyPlans(ctx, body)
	case "plan_members":
		return s.primaryMembersPlanMembers(ctx, body)
	case "toggle_coming_today":
		return s.primaryMembersToggleComing(ctx, body)
	case "add_member":
		return s.primaryMembersAddMember(ctx, body)
	case "submit_request":
		return s.primaryMembersSubmitRequest(ctx, body)
	default:
		return false, "unknown_action", nil
	}
}
