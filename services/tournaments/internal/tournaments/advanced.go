package tournaments

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// --- Knockout / preview / walkover / reset ---

type KnockoutInput struct {
	Category string `json:"category"`
	Gender   string `json:"gender"`
}

type PreviewInput struct {
	Category      string `json:"category"`
	Gender        string `json:"gender"`
	SideCount     int    `json:"sideCount"`
	BracketMethod string `json:"bracketMethod"`
}

type PreviewResult struct {
	ByeCount     int `json:"bye_count"`
	Preliminary  int `json:"preliminary"`
	SideCount    int `json:"side_count"`
	Rounds       int `json:"rounds"`
	BracketSize  int `json:"bracket_size"`
}

type WalkoverInput struct {
	WinnerSide int `json:"winner_side"`
}

type ResetFixturesInput struct {
	Category string `json:"category"`
	Gender   string `json:"gender"`
}

func nextPow2(n int) int {
	if n <= 1 {
		return 1
	}
	p := 1
	for p < n {
		p <<= 1
	}
	return p
}

func estimateBracket(sideCount int) PreviewResult {
	if sideCount < 0 {
		sideCount = 0
	}
	if sideCount == 0 {
		return PreviewResult{}
	}
	bracket := nextPow2(sideCount)
	byes := bracket - sideCount
	prelim := 0
	if byes == 0 && sideCount > 1 {
		// perfect power of 2 — no prelim/bye needed beyond first round
		prelim = 0
	} else if byes > 0 {
		// sides that play in round of bracket: sideCount - byes play first,
		// equivalently preliminary matches = (sideCount - bracket/2) when not power-of-2
		half := bracket / 2
		if sideCount > half {
			prelim = sideCount - half
		}
	}
	rounds := 0
	if bracket > 1 {
		rounds = int(math.Log2(float64(bracket)))
	}
	return PreviewResult{
		ByeCount:    byes,
		Preliminary: prelim,
		SideCount:   sideCount,
		Rounds:      rounds,
		BracketSize: bracket,
	}
}

func (s *Service) FixturePreview(ctx context.Context, tournamentID int64, in PreviewInput) (*PreviewResult, error) {
	sideCount := in.SideCount
	if sideCount <= 0 {
		entries, err := s.listEntriesFiltered(ctx, tournamentID, in.Category, in.Gender)
		if err != nil {
			return nil, err
		}
		sideCount = len(entries)
	}
	r := estimateBracket(sideCount)
	return &r, nil
}

func (s *Service) listEntriesFiltered(ctx context.Context, tournamentID int64, category, gender string) ([]Entry, error) {
	all, err := s.ListEntries(ctx, tournamentID)
	if err != nil {
		return nil, err
	}
	cat := strings.TrimSpace(strings.ToLower(category))
	gen := strings.TrimSpace(strings.ToLower(gender))
	out := []Entry{}
	for _, e := range all {
		if strings.ToLower(e.Status) != "active" && e.Status != "" {
			continue
		}
		if cat != "" && strings.ToLower(e.Category) != cat {
			continue
		}
		if gen != "" && strings.ToLower(e.Gender) != gen {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

func (s *Service) GenerateKnockout(ctx context.Context, tournamentID int64, in KnockoutInput) ([]Fixture, error) {
	cat := strings.TrimSpace(in.Category)
	gen := strings.TrimSpace(in.Gender)

	// Prefer winners from existing completed fixtures in this category; else entries.
	sides := []string{}
	existing, err := s.ListFixtures(ctx, tournamentID)
	if err != nil {
		return nil, err
	}
	winners := []string{}
	for _, f := range existing {
		if cat != "" && !strings.EqualFold(f.Category, cat) {
			continue
		}
		if gen != "" && !strings.EqualFold(f.Gender, gen) {
			continue
		}
		if strings.EqualFold(f.Status, "completed") && strings.TrimSpace(f.Winner) != "" {
			winners = append(winners, f.Winner)
		}
	}
	if len(winners) >= 2 {
		sides = winners
	} else {
		entries, err := s.listEntriesFiltered(ctx, tournamentID, cat, gen)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			sides = append(sides, e.PlayerName)
		}
	}
	if len(sides) < 2 {
		return nil, fmt.Errorf("need_at_least_2_sides")
	}

	// Clear existing knockout-ish fixtures for this category (round >= 1 already used;
	// we delete matching category fixtures then insert fresh KO round 1).
	if err := s.ResetFixtures(ctx, tournamentID, ResetFixturesInput{Category: cat, Gender: gen}); err != nil {
		return nil, err
	}

	est := estimateBracket(len(sides))
	// Pad with BYE placeholders so pairing is even for first round playable matches.
	padded := make([]string, 0, est.BracketSize)
	padded = append(padded, sides...)
	for len(padded) < est.BracketSize {
		padded = append(padded, "BYE")
	}

	matchNo := 0
	for i := 0; i < len(padded); i += 2 {
		matchNo++
		t1, t2 := padded[i], padded[i+1]
		status := "scheduled"
		var winner interface{}
		var s1, s2 interface{}
		if t1 == "BYE" && t2 != "BYE" {
			status = "completed"
			winner = t2
			zero, one := 0, 1
			s1, s2 = zero, one
		} else if t2 == "BYE" && t1 != "BYE" {
			status = "completed"
			winner = t1
			one, zero := 1, 0
			s1, s2 = one, zero
		}
		_, err := s.DB.ExecContext(ctx, `
INSERT INTO tournament_fixtures
  (tournament_id, category, gender, round_no, match_no, team1, team2, score1, score2, winner, status)
VALUES ($1,$2,$3,1,$4,$5,$6,$7,$8,$9,$10)`,
			tournamentID, cat, gen, matchNo, t1, t2, s1, s2, winner, status)
		if err != nil {
			return nil, err
		}
	}
	return s.listFixturesFiltered(ctx, tournamentID, cat, gen)
}

func (s *Service) listFixturesFiltered(ctx context.Context, tournamentID int64, category, gender string) ([]Fixture, error) {
	all, err := s.ListFixtures(ctx, tournamentID)
	if err != nil {
		return nil, err
	}
	cat := strings.TrimSpace(strings.ToLower(category))
	gen := strings.TrimSpace(strings.ToLower(gender))
	if cat == "" && gen == "" {
		return all, nil
	}
	out := []Fixture{}
	for _, f := range all {
		if cat != "" && strings.ToLower(f.Category) != cat {
			continue
		}
		if gen != "" && strings.ToLower(f.Gender) != gen {
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

func (s *Service) Walkover(ctx context.Context, fixtureID int64, winnerSide int) (*Fixture, error) {
	if winnerSide != 1 && winnerSide != 2 {
		return nil, fmt.Errorf("invalid_winner_side")
	}
	cur, err := s.getFixture(ctx, fixtureID)
	if err != nil {
		return nil, err
	}
	winner := cur.Team1
	s1, s2 := 1, 0
	if winnerSide == 2 {
		winner = cur.Team2
		s1, s2 = 0, 1
	}
	in := ScoreInput{
		Score1: &s1,
		Score2: &s2,
		Status: "completed",
		Winner: winner,
	}
	return s.UpdateFixtureScore(ctx, fixtureID, in)
}

func (s *Service) ResetFixtures(ctx context.Context, tournamentID int64, in ResetFixturesInput) error {
	cat := strings.TrimSpace(in.Category)
	gen := strings.TrimSpace(in.Gender)
	if cat == "" && gen == "" {
		_, err := s.DB.ExecContext(ctx, `
DELETE FROM tournament_fixtures WHERE tournament_id=$1`, tournamentID)
		return err
	}
	_, err := s.DB.ExecContext(ctx, `
DELETE FROM tournament_fixtures
WHERE tournament_id=$1
  AND ($2='' OR LOWER(category)=LOWER($2))
  AND ($3='' OR LOWER(gender)=LOWER($3))`,
		tournamentID, cat, gen)
	return err
}

// --- Players template / import ---

var defaultFormFieldNames = []string{
	"first_name", "last_name", "dob", "gender", "whatsapp_number",
	"alternate_contact", "address", "club_or_academy", "id_proof", "id_number",
}

func DefaultFormCatalog() []map[string]interface{} {
	locked := map[string]bool{
		"first_name": true, "last_name": true, "dob": true,
		"gender": true, "whatsapp_number": true,
	}
	out := make([]map[string]interface{}, 0, len(defaultFormFieldNames))
	for _, name := range defaultFormFieldNames {
		mand := 0
		lock := 0
		if locked[name] {
			mand = 1
			lock = 1
		}
		out = append(out, map[string]interface{}{
			"field_name":   name,
			"is_mandatory": mand,
			"locked":       lock,
		})
	}
	return out
}

func (s *Service) PlayersTemplateCSV(_ context.Context, _ int64) (string, []map[string]interface{}) {
	header := strings.Join(defaultFormFieldNames, ",") + "\n"
	return header, DefaultFormCatalog()
}

type ImportPlayersInput struct {
	Text string                   `json:"text"`
	CSV  string                   `json:"csv"`
	Rows []map[string]interface{} `json:"rows"`
}

func (s *Service) ImportPlayers(ctx context.Context, tournamentID int64, in ImportPlayersInput) ([]Entry, error) {
	rows := in.Rows
	if len(rows) == 0 {
		raw := strings.TrimSpace(in.Text)
		if raw == "" {
			raw = strings.TrimSpace(in.CSV)
		}
		if raw != "" {
			parsed, err := parsePlayersCSV(raw)
			if err != nil {
				return nil, err
			}
			rows = parsed
		}
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("no_rows")
	}
	created := []Entry{}
	for _, row := range rows {
		name := playerNameFromRow(row)
		if name == "" {
			continue
		}
		e, err := s.CreateEntry(ctx, tournamentID, EntryInput{
			Category:      strFromRow(row, "category", "category_name"),
			Gender:        strFromRow(row, "gender"),
			PlayerName:    name,
			Phone:         strFromRow(row, "whatsapp_number", "phone", "mobile"),
			Email:         strFromRow(row, "email"),
			PaymentStatus: "pending",
			Status:        "active",
		})
		if err != nil {
			return created, err
		}
		created = append(created, *e)
	}
	return created, nil
}

func playerNameFromRow(row map[string]interface{}) string {
	if n := strFromRow(row, "player_name", "name"); n != "" {
		return n
	}
	first := strFromRow(row, "first_name", "firstname")
	last := strFromRow(row, "last_name", "lastname")
	return strings.TrimSpace(strings.TrimSpace(first + " " + last))
}

func strFromRow(row map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := row[k]; ok && v != nil {
			s := strings.TrimSpace(fmt.Sprint(v))
			if s != "" && s != "<nil>" {
				return s
			}
		}
		// case-insensitive
		lk := strings.ToLower(k)
		for rk, rv := range row {
			if strings.ToLower(rk) == lk && rv != nil {
				s := strings.TrimSpace(fmt.Sprint(rv))
				if s != "" && s != "<nil>" {
					return s
				}
			}
		}
	}
	return ""
}

func parsePlayersCSV(raw string) ([]map[string]interface{}, error) {
	r := csv.NewReader(strings.NewReader(raw))
	r.TrimLeadingSpace = true
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("invalid_csv")
	}
	if len(records) == 0 {
		return nil, nil
	}
	headers := make([]string, len(records[0]))
	hasHeader := false
	for i, h := range records[0] {
		headers[i] = strings.ToLower(strings.TrimSpace(h))
	}
	for _, h := range headers {
		if h == "first_name" || h == "last_name" || h == "player_name" || h == "name" || h == "whatsapp_number" {
			hasHeader = true
			break
		}
	}
	start := 0
	if hasHeader {
		start = 1
	} else {
		// assume first_name,last_name,... order matching default catalog
		headers = append([]string{}, defaultFormFieldNames...)
	}
	out := []map[string]interface{}{}
	for _, rec := range records[start:] {
		if len(rec) == 0 {
			continue
		}
		row := map[string]interface{}{}
		empty := true
		for i, cell := range rec {
			key := fmt.Sprintf("col_%d", i)
			if i < len(headers) && headers[i] != "" {
				key = headers[i]
			}
			val := strings.TrimSpace(cell)
			if val != "" {
				empty = false
			}
			row[key] = val
		}
		if !empty {
			out = append(out, row)
		}
	}
	return out, nil
}

// --- Ledger ---

type LedgerData struct {
	Total   int     `json:"total"`
	Paid    int     `json:"paid"`
	Unpaid  int     `json:"unpaid"`
	Entries []Entry `json:"entries"`
}

func (s *Service) Ledger(ctx context.Context, tournamentID int64) (*LedgerData, error) {
	entries, err := s.ListEntries(ctx, tournamentID)
	if err != nil {
		return nil, err
	}
	data := &LedgerData{Entries: entries, Total: len(entries)}
	for _, e := range entries {
		ps := strings.ToLower(strings.TrimSpace(e.PaymentStatus))
		if ps == "paid" || ps == "completed" || ps == "success" {
			data.Paid++
		} else {
			data.Unpaid++
		}
	}
	return data, nil
}

// --- Form fields ---

func (s *Service) GetFormFields(ctx context.Context, tournamentID int64) ([]map[string]interface{}, error) {
	var raw []byte
	err := s.DB.QueryRowContext(ctx, `
SELECT fields FROM tournament_form_fields WHERE tournament_id=$1`, tournamentID).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var fields []map[string]interface{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

func (s *Service) SaveFormFields(ctx context.Context, tournamentID int64, fields []map[string]interface{}) error {
	if tournamentID <= 0 {
		return fmt.Errorf("invalid_tournament")
	}
	if fields == nil {
		fields = []map[string]interface{}{}
	}
	b, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `
INSERT INTO tournament_form_fields (tournament_id, fields, updated_at)
VALUES ($1,$2::jsonb,NOW())
ON CONFLICT (tournament_id) DO UPDATE SET fields=EXCLUDED.fields, updated_at=NOW()`,
		tournamentID, string(b))
	return err
}

// --- Mark all paid ---

func (s *Service) NotifyContacts(ctx context.Context, tournamentID, facilityID int64, message, template string) (int, error) {
	msg := strings.TrimSpace(message)
	if msg == "" {
		msg = "Tournament update from Facinect."
	}
	tmpl := strings.TrimSpace(template)
	if tmpl == "" {
		tmpl = "tournament_registration_link" // PHP catalog default for share/notify
	}
	tour, _ := s.Get(ctx, tournamentID)
	tourName := "Tournament"
	tourDate := ""
	buttonToken := fmt.Sprintf("%d", tournamentID)
	if tour != nil {
		if strings.TrimSpace(tour.Name) != "" {
			tourName = tour.Name
		}
		tourDate = strings.TrimSpace(tour.StartDate)
		if tourDate == "" {
			tourDate = "TBA"
		}
		if slug := strings.TrimSpace(tour.Slug); slug != "" {
			buttonToken = slug
		}
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT DISTINCT COALESCE(NULLIF(phone,''), ''), COALESCE(NULLIF(player_name,''), 'Player')
FROM tournament_entries WHERE tournament_id=$1 AND COALESCE(phone,'') <> ''`, tournamentID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	sent := 0
	for rows.Next() {
		var phone, player string
		if err := rows.Scan(&phone, &player); err != nil {
			continue
		}
		phone = digitsOnlyTourPhone(phone)
		if phone == "" {
			continue
		}
		if player == "" {
			player = "Player"
		}
		var data map[string]interface{}
		switch tmpl {
		case "tournament_registration_link":
			data = map[string]interface{}{
				"body_params":       []string{player, tourName, tourDate},
				"button_url_param":  buttonToken,
				"title":             "Tournament registration",
				"body":              msg,
			}
		case "hello_world1":
			data = map[string]interface{}{
				"header_image":         "https://strollbike.com/Services/Facinect/images/poster.jpeg",
				"quick_reply_payload":  "I_AM_INTERESTED",
				"title":                "Facinect",
				"body":                 msg,
			}
		default:
			data = map[string]interface{}{
				"title": "Tournament update", "body": msg, "message": msg, "text": msg, "type": "tournament",
			}
		}
		if err := s.notifyWhatsAppData(ctx, facilityID, phone, tmpl, data); err == nil {
			sent++
		}
	}
	return sent, nil
}

func digitsOnlyTourPhone(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	d := b.String()
	if len(d) == 10 {
		d = "91" + d
	}
	return d
}

func (s *Service) notifyWhatsApp(ctx context.Context, facilityID int64, phone, template, body string) error {
	return s.notifyWhatsAppData(ctx, facilityID, phone, template, map[string]interface{}{
		"title": "Tournament update", "body": body, "message": body, "text": body, "type": "tournament",
	})
}

func (s *Service) notifyWhatsAppData(ctx context.Context, facilityID int64, phone, template string, data map[string]interface{}) error {
	base := strings.TrimRight(strings.TrimSpace(s.Cfg.NotificationsURL), "/")
	if base == "" {
		return fmt.Errorf("notifications_url_missing")
	}
	if data == nil {
		data = map[string]interface{}{}
	}
	payload := map[string]interface{}{
		"channel":     "whatsapp",
		"facility_id": facilityID,
		"template":    template,
		"to":          map[string]string{"whatsapp": phone},
		"data":        data,
	}
	raw, _ := json.Marshal(payload)
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/notifications/send", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.Cfg.NotificationsKey != "" {
		req.Header.Set("X-Service-Key", s.Cfg.NotificationsKey)
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return fmt.Errorf("notify_http_%d: %s", res.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// notifyTournamentRegistrationConfirmation mirrors PHP facinect_send_tournament_registration_whatsapp.
func (s *Service) notifyTournamentRegistrationConfirmation(ctx context.Context, entry *Entry, amount float64, txn string) {
	if entry == nil {
		return
	}
	phone := digitsOnlyTourPhone(entry.Phone)
	if phone == "" {
		return
	}
	tour, err := s.Get(ctx, entry.TournamentID)
	if err != nil || tour == nil {
		return
	}
	tourName := strings.TrimSpace(tour.Name)
	if tourName == "" {
		tourName = "Tournament"
	}
	cat := strings.TrimSpace(entry.Category)
	if cat == "" {
		cat = "Open"
	}
	regCode := fmt.Sprintf("REG-%d", entry.ID)
	if txn == "" {
		txn = "SPOT_" + regCode
	}
	amt := fmt.Sprintf("%.2f", amount)
	_ = s.notifyWhatsAppData(ctx, tour.FacilityID, phone, "tournament_registration_confirmation", map[string]interface{}{
		"body_params": []string{
			tourName, cat, regCode, amt, txn,
		},
		"button_url_param": "code=" + regCode,
	})
}

func (s *Service) MarkAllPaid(ctx context.Context, tournamentID int64, category string) (int64, error) {
	cat := strings.TrimSpace(category)
	res, err := s.DB.ExecContext(ctx, `
UPDATE tournament_entries SET payment_status='paid'
WHERE tournament_id=$1
  AND LOWER(payment_status) NOT IN ('paid','completed','success')
  AND ($2='' OR LOWER(category)=LOWER($2))`,
		tournamentID, cat)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// --- Referee match assign / history / validate ---

type AssignMatchInput struct {
	MatchID int64 `json:"match_id"`
	RefID   int64 `json:"ref_id"`
}

func (s *Service) AssignRefereeMatch(ctx context.Context, tournamentID, matchID, refID int64) (*Fixture, error) {
	if tournamentID <= 0 || matchID <= 0 || refID <= 0 {
		return nil, fmt.Errorf("invalid_assign_match")
	}
	fx, err := s.getFixture(ctx, matchID)
	if err != nil {
		return nil, err
	}
	if fx.TournamentID != tournamentID {
		return nil, fmt.Errorf("fixture_mismatch")
	}
	_, err = s.DB.ExecContext(ctx, `
UPDATE tournament_fixtures SET ref_id=$1, updated_at=NOW() WHERE id=$2`, refID, matchID)
	if err != nil {
		return nil, err
	}
	return s.getFixture(ctx, matchID)
}

func (s *Service) UnassignRefereeMatch(ctx context.Context, tournamentID, matchID int64) error {
	if tournamentID <= 0 || matchID <= 0 {
		return fmt.Errorf("invalid_unassign_match")
	}
	fx, err := s.getFixture(ctx, matchID)
	if err != nil {
		return err
	}
	if fx.TournamentID != tournamentID {
		return fmt.Errorf("fixture_mismatch")
	}
	_, err = s.DB.ExecContext(ctx, `
UPDATE tournament_fixtures SET ref_id=NULL, updated_at=NOW() WHERE id=$1`, matchID)
	return err
}

func (s *Service) OwnerRefereeHistory(ctx context.Context, facilityID int64, limit int) ([]Referee, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	if facilityID > 0 {
		rows, err := s.DB.QueryContext(ctx, `
SELECT r.id, r.tournament_id, COALESCE(r.category,''), COALESCE(r.gender,''),
       COALESCE(r.court,''), COALESCE(r.email,''), r.ref_id, r.pool_no, r.round_no,
       r.created_at::text
FROM tournament_referees r
JOIN tournaments t ON t.id = r.tournament_id
WHERE t.facility_id=$1
ORDER BY r.id DESC LIMIT $2`, facilityID, limit)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		return scanReferees(rows)
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, tournament_id, COALESCE(category,''), COALESCE(gender,''),
       COALESCE(court,''), COALESCE(email,''), ref_id, pool_no, round_no,
       created_at::text
FROM tournament_referees
ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReferees(rows)
}

// --- Ref tokens (public scoring) ---

type RefTokenRow struct {
	Token        string `json:"token"`
	TournamentID int64  `json:"tournament_id"`
	Category     string `json:"category"`
	Gender       string `json:"gender"`
	Court        string `json:"court"`
	RoundNo      *int   `json:"round_no,omitempty"`
	PoolNo       *int   `json:"pool_no,omitempty"`
}

type CreateRefLinkInput struct {
	Category string `json:"category"`
	Gender   string `json:"gender"`
	Court    string `json:"court"`
	RoundNo  *int   `json:"round_no"`
	PoolNo   *int   `json:"pool_no"`
}

type RefLinkResult struct {
	Token string `json:"token"`
	URL   string `json:"url"`
}

func randomToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *Service) CreateRefLink(ctx context.Context, tournamentID int64, in CreateRefLinkInput) (*RefLinkResult, error) {
	if tournamentID <= 0 {
		return nil, fmt.Errorf("invalid_tournament")
	}
	tok, err := randomToken()
	if err != nil {
		return nil, err
	}
	var round, pool interface{}
	if in.RoundNo != nil {
		round = *in.RoundNo
	}
	if in.PoolNo != nil {
		pool = *in.PoolNo
	}
	_, err = s.DB.ExecContext(ctx, `
INSERT INTO tournament_ref_tokens
  (token, tournament_id, category, gender, court, round_no, pool_no)
VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		tok, tournamentID,
		strings.TrimSpace(in.Category),
		strings.TrimSpace(in.Gender),
		strings.TrimSpace(in.Court),
		round, pool,
	)
	if err != nil {
		return nil, err
	}
	return &RefLinkResult{
		Token: tok,
		URL:   "/v1/tournaments/ref?token=" + tok,
	}, nil
}

func (s *Service) ResolveRefToken(ctx context.Context, token string) (*RefTokenRow, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("missing_token")
	}
	var row RefTokenRow
	var round, pool sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `
SELECT token, tournament_id, COALESCE(category,''), COALESCE(gender,''),
       COALESCE(court,''), round_no, pool_no
FROM tournament_ref_tokens WHERE token=$1`, token).Scan(
		&row.Token, &row.TournamentID, &row.Category, &row.Gender, &row.Court, &round, &pool)
	if err != nil {
		return nil, err
	}
	if round.Valid {
		v := int(round.Int64)
		row.RoundNo = &v
	}
	if pool.Valid {
		v := int(pool.Int64)
		row.PoolNo = &v
	}
	return &row, nil
}

func (s *Service) RefMatches(ctx context.Context, token string) ([]Fixture, error) {
	rt, err := s.ResolveRefToken(ctx, token)
	if err != nil {
		return nil, err
	}
	all, err := s.ListFixtures(ctx, rt.TournamentID)
	if err != nil {
		return nil, err
	}
	out := []Fixture{}
	for _, f := range all {
		if rt.Category != "" && !strings.EqualFold(f.Category, rt.Category) {
			continue
		}
		if rt.Gender != "" && !strings.EqualFold(f.Gender, rt.Gender) {
			continue
		}
		// Only enforce court when the fixture has a court assigned.
		if rt.Court != "" && strings.TrimSpace(f.Court) != "" && !strings.EqualFold(f.Court, rt.Court) {
			continue
		}
		if rt.RoundNo != nil && *rt.RoundNo > 0 && f.RoundNo != *rt.RoundNo {
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

func (s *Service) RefScore(ctx context.Context, token string, matchID int64, score1, score2 int, complete bool, winner string) (*Fixture, error) {
	rt, err := s.ResolveRefToken(ctx, token)
	if err != nil {
		return nil, err
	}
	fx, err := s.getFixture(ctx, matchID)
	if err != nil {
		return nil, err
	}
	if fx.TournamentID != rt.TournamentID {
		return nil, fmt.Errorf("fixture_mismatch")
	}
	if rt.Category != "" && !strings.EqualFold(fx.Category, rt.Category) {
		return nil, fmt.Errorf("category_mismatch")
	}
	st := fx.Status
	w := strings.TrimSpace(winner)
	if complete {
		st = "completed"
		if w == "" {
			if score1 > score2 {
				w = fx.Team1
			} else if score2 > score1 {
				w = fx.Team2
			}
		}
	} else if !strings.EqualFold(st, "completed") {
		st = "in_progress"
	}
	return s.UpdateFixtureScore(ctx, matchID, ScoreInput{
		Score1: &score1,
		Score2: &score2,
		Status: st,
		Winner: w,
	})
}

func (s *Service) RefHistory(ctx context.Context, token string, limit int) ([]ScoreEvent, error) {
	rt, err := s.ResolveRefToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	q := `
SELECT id, tournament_id, COALESCE(category,''), match_id,
       score1, score2, COALESCE(status,''), COALESCE(winner,''), created_at::text
FROM tournament_score_events
WHERE tournament_id=$1`
	args := []interface{}{rt.TournamentID}
	n := 2
	if rt.Category != "" {
		q += fmt.Sprintf(` AND category=$%d`, n)
		args = append(args, rt.Category)
		n++
	}
	q += fmt.Sprintf(` ORDER BY id DESC LIMIT $%d`, n)
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScoreEvent{}
	for rows.Next() {
		var e ScoreEvent
		var s1, s2 sql.NullInt64
		if err := rows.Scan(&e.ID, &e.TournamentID, &e.Category, &e.MatchID,
			&s1, &s2, &e.Status, &e.Winner, &e.CreatedAt); err != nil {
			return nil, err
		}
		if s1.Valid {
			v := int(s1.Int64)
			e.Score1 = &v
		}
		if s2.Valid {
			v := int(s2.Int64)
			e.Score2 = &v
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
