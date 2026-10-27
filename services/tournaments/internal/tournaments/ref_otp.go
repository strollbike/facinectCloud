package tournaments

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"math/big"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type RefProfile struct {
	RefID            int64  `json:"ref_id"`
	Email            string `json:"email"`
	Phone            string `json:"phone,omitempty"`
	FullName         string `json:"full_name,omitempty"`
	RefereeIdentity  string `json:"referee_identity,omitempty"`
	IsActive         bool   `json:"is_active"`
}

type AssignCourtResult struct {
	Referee     *Referee `json:"referee,omitempty"`
	AccessCode  string   `json:"access_code,omitempty"`
	Token       string   `json:"token,omitempty"`
	ExpiresAt   string   `json:"expires_at,omitempty"`
	URL         string   `json:"url,omitempty"`
	WhatsAppSent bool    `json:"whatsapp_sent,omitempty"`
}

type Scorecard struct {
	AssignmentID       int64  `json:"assignment_id"`
	OwnerType          string `json:"owner_type"`
	OwnerID            int64  `json:"owner_id"`
	OwnerName          string `json:"owner_name"`
	OwnerLabel         string `json:"owner_label"`
	TourID             int64  `json:"tour_id"`
	TournamentName     string `json:"tournament_name"`
	TournamentDate     string `json:"tournament_date,omitempty"`
	TournStatus        string `json:"tourn_status,omitempty"`
	Category           string `json:"category"`
	Gender             string `json:"gender"`
	Court              string `json:"court"`
	PoolNo             int    `json:"pool_no"`
	RoundNo            int    `json:"round_no"`
	AssignedAt         string `json:"assigned_at,omitempty"`
	ExpiresAt          string `json:"expires_at,omitempty"`
	RequiresAccessCode bool   `json:"requires_access_code"`
	Unlocked           bool   `json:"unlocked"`
	CodeValidHours     int    `json:"code_valid_hours"`
	Token              string `json:"token,omitempty"`
	AccessCode         *string `json:"access_code"` // always null for client APIs
}

type RefereeLoginResult struct {
	Referee    *RefProfile             `json:"referee"`
	Owners     []map[string]interface{} `json:"owners"`
	Scorecards []Scorecard             `json:"scorecards"`
}

func formatRefereeIdentity(refID int64, stored string) string {
	stored = strings.TrimSpace(stored)
	if stored != "" {
		return strings.ToUpper(stored)
	}
	return fmt.Sprintf("REF%04d", refID)
}

func generateAccessCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func (s *Service) EnsureRefProfile(ctx context.Context, email, phone, fullName, identity string, preferredRefID *int64) (*RefProfile, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || !strings.Contains(email, "@") {
		return nil, fmt.Errorf("invalid_email")
	}
	phone = digitsOnlyTourPhone(phone)
	identity = strings.ToUpper(strings.TrimSpace(identity))

	existing, err := s.getRefProfileByEmail(ctx, email)
	if err == nil && existing != nil {
		_, _ = s.DB.ExecContext(ctx, `
UPDATE tournament_ref_profiles SET
  phone=COALESCE(NULLIF($2,''), phone),
  full_name=COALESCE(NULLIF($3,''), full_name),
  referee_identity=COALESCE(NULLIF($4,''), referee_identity),
  updated_at=NOW()
WHERE ref_id=$1`, existing.RefID, phone, strings.TrimSpace(fullName), identity)
		return s.getRefProfileByID(ctx, existing.RefID)
	}

	var id int64
	if preferredRefID != nil && *preferredRefID > 0 {
		err = s.DB.QueryRowContext(ctx, `
INSERT INTO tournament_ref_profiles (ref_id, email, phone, full_name, referee_identity)
VALUES ($1,$2,NULLIF($3,''),NULLIF($4,''),NULLIF($5,''))
ON CONFLICT (email) DO UPDATE SET
  phone=COALESCE(EXCLUDED.phone, tournament_ref_profiles.phone),
  full_name=COALESCE(EXCLUDED.full_name, tournament_ref_profiles.full_name),
  referee_identity=COALESCE(EXCLUDED.referee_identity, tournament_ref_profiles.referee_identity),
  updated_at=NOW()
RETURNING ref_id`,
			*preferredRefID, email, phone, strings.TrimSpace(fullName), identity).Scan(&id)
	} else {
		err = s.DB.QueryRowContext(ctx, `
INSERT INTO tournament_ref_profiles (email, phone, full_name, referee_identity)
VALUES ($1,NULLIF($2,''),NULLIF($3,''),NULLIF($4,''))
ON CONFLICT (email) DO UPDATE SET
  phone=COALESCE(EXCLUDED.phone, tournament_ref_profiles.phone),
  full_name=COALESCE(EXCLUDED.full_name, tournament_ref_profiles.full_name),
  referee_identity=COALESCE(EXCLUDED.referee_identity, tournament_ref_profiles.referee_identity),
  updated_at=NOW()
RETURNING ref_id`,
			email, phone, strings.TrimSpace(fullName), identity).Scan(&id)
	}
	if err != nil {
		return nil, err
	}
	p, err := s.getRefProfileByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.RefereeIdentity) == "" {
		ident := formatRefereeIdentity(p.RefID, "")
		_, _ = s.DB.ExecContext(ctx, `UPDATE tournament_ref_profiles SET referee_identity=$1 WHERE ref_id=$2`, ident, p.RefID)
		p.RefereeIdentity = ident
	}
	return p, nil
}

func (s *Service) getRefProfileByEmail(ctx context.Context, email string) (*RefProfile, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT ref_id, COALESCE(email,''), COALESCE(phone,''), COALESCE(full_name,''),
       COALESCE(referee_identity,''), is_active
FROM tournament_ref_profiles WHERE LOWER(email)=LOWER($1)`, strings.TrimSpace(email))
	return scanRefProfile(row)
}

func (s *Service) getRefProfileByID(ctx context.Context, id int64) (*RefProfile, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT ref_id, COALESCE(email,''), COALESCE(phone,''), COALESCE(full_name,''),
       COALESCE(referee_identity,''), is_active
FROM tournament_ref_profiles WHERE ref_id=$1`, id)
	return scanRefProfile(row)
}

func scanRefProfile(row scannable) (*RefProfile, error) {
	var p RefProfile
	err := row.Scan(&p.RefID, &p.Email, &p.Phone, &p.FullName, &p.RefereeIdentity, &p.IsActive)
	if err != nil {
		return nil, err
	}
	p.RefereeIdentity = formatRefereeIdentity(p.RefID, p.RefereeIdentity)
	return &p, nil
}

// ValidateRefereeProfile resolves email + optional identity for admin assign / login.
func (s *Service) ValidateRefereeProfile(ctx context.Context, email, refRaw string) (*RefProfile, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, fmt.Errorf("email_required")
	}
	p, err := s.getRefProfileByEmail(ctx, email)
	if err != nil {
		// Soft-create from email so admin assign can proceed (PHP has platform directory).
		var pref *int64
		if id, e := parseRefID(refRaw); e == nil && id > 0 {
			pref = &id
		}
		return s.EnsureRefProfile(ctx, email, "", "", strings.TrimSpace(refRaw), pref)
	}
	if !p.IsActive {
		return nil, fmt.Errorf("referee_inactive")
	}
	raw := strings.ToUpper(strings.TrimSpace(refRaw))
	if raw != "" {
		want := formatRefereeIdentity(p.RefID, p.RefereeIdentity)
		if raw != want && raw != fmt.Sprintf("%d", p.RefID) {
			if id, e := parseRefID(raw); e == nil {
				if id != p.RefID {
					return nil, fmt.Errorf("referee_mismatch")
				}
			} else {
				return nil, fmt.Errorf("referee_mismatch")
			}
		}
	}
	return p, nil
}

func parseRefID(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("empty")
	}
	var n int64
	_, err := fmt.Sscanf(raw, "%d", &n)
	if err == nil && n > 0 {
		return n, nil
	}
	raw = strings.ToUpper(raw)
	raw = strings.TrimPrefix(raw, "REF")
	_, err = fmt.Sscanf(raw, "%d", &n)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid")
	}
	return n, nil
}

// AssignRefereeCourtWithPIN assigns a court and issues a one-time 6-digit access code.
func (s *Service) AssignRefereeCourtWithPIN(ctx context.Context, tournamentID int64, in AssignCourtInput) (*AssignCourtResult, error) {
	court := strings.TrimSpace(in.Court)
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if tournamentID <= 0 || court == "" || email == "" {
		return nil, fmt.Errorf("invalid_assign")
	}
	tour, err := s.Get(ctx, tournamentID)
	if err != nil {
		return nil, err
	}
	refHint := ""
	if in.RefID != nil && *in.RefID > 0 {
		refHint = fmt.Sprintf("%d", *in.RefID)
	}
	profile, err := s.ValidateRefereeProfile(ctx, email, refHint)
	if err != nil {
		return nil, err
	}
	if phone := digitsOnlyTourPhone(in.Phone); phone != "" {
		profile, _ = s.EnsureRefProfile(ctx, email, phone, profile.FullName, profile.RefereeIdentity, &profile.RefID)
	}

	code, err := generateAccessCode()
	if err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(code), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	expires := time.Now().UTC().Add(24 * time.Hour)
	link, err := s.CreateRefLink(ctx, tournamentID, CreateRefLinkInput{
		Category: in.Category, Gender: in.Gender, Court: court,
		RoundNo: in.RoundNo, PoolNo: in.PoolNo,
	})
	if err != nil {
		return nil, err
	}

	var pool, round interface{}
	if in.PoolNo != nil {
		pool = *in.PoolNo
	}
	if in.RoundNo != nil {
		round = *in.RoundNo
	}
	phone := digitsOnlyTourPhone(profile.Phone)
	if phone == "" {
		phone = digitsOnlyTourPhone(in.Phone)
	}

	// Clear previous active assignment for same court scope, then insert.
	_, _ = s.DB.ExecContext(ctx, `
UPDATE tournament_referees SET status='cleared', pin_hash=NULL
WHERE tournament_id=$1 AND court=$2 AND ($3='' OR category=$3) AND status='active'`,
		tournamentID, court, strings.TrimSpace(in.Category))

	var id int64
	err = s.DB.QueryRowContext(ctx, `
INSERT INTO tournament_referees
  (tournament_id, category, gender, court, email, phone, ref_id, pool_no, round_no,
   status, pin_hash, pin_expires_at, access_token, assigned_at)
VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),$7,$8,$9,'active',$10,$11,$12,NOW())
RETURNING id`,
		tournamentID,
		strings.TrimSpace(in.Category),
		strings.TrimSpace(in.Gender),
		court, email, phone, profile.RefID, pool, round,
		string(hash), expires, link.Token,
	).Scan(&id)
	if err != nil {
		return nil, err
	}
	ref, err := s.getReferee(ctx, id)
	if err != nil {
		return nil, err
	}

	waSent := false
	if phone != "" {
		if err := s.notifyWhatsAppData(ctx, tour.FacilityID, phone, "facinect_otp", map[string]interface{}{
			"title": "Referee access code",
			"body":  fmt.Sprintf("Your Facinect referee access code is %s (valid 24h).", code),
			"otp":   code,
			"code":  code,
		}); err == nil {
			waSent = true
		}
	}

	return &AssignCourtResult{
		Referee: ref, AccessCode: code, Token: link.Token,
		ExpiresAt: expires.Format(time.RFC3339), URL: link.URL, WhatsAppSent: waSent,
	}, nil
}

func (s *Service) RefereeLogin(ctx context.Context, email, refRaw string) (*RefereeLoginResult, error) {
	profile, err := s.ValidateRefereeProfile(ctx, email, refRaw)
	if err != nil {
		return nil, err
	}
	cards, err := s.listScorecardsForReferee(ctx, profile.RefID, false)
	if err != nil {
		return nil, err
	}
	owners := groupScorecardsByOwner(cards)
	return &RefereeLoginResult{Referee: profile, Owners: owners, Scorecards: cards}, nil
}

func groupScorecardsByOwner(cards []Scorecard) []map[string]interface{} {
	order := []string{}
	byKey := map[string]map[string]interface{}{}
	for _, c := range cards {
		key := fmt.Sprintf("%s:%d", c.OwnerType, c.OwnerID)
		if _, ok := byKey[key]; !ok {
			order = append(order, key)
			byKey[key] = map[string]interface{}{
				"owner_type": c.OwnerType, "owner_id": c.OwnerID,
				"owner_name": c.OwnerName, "owner_label": c.OwnerLabel,
				"scorecards": []Scorecard{},
			}
		}
		list := byKey[key]["scorecards"].([]Scorecard)
		byKey[key]["scorecards"] = append(list, c)
	}
	out := make([]map[string]interface{}, 0, len(order))
	for _, k := range order {
		out = append(out, byKey[k])
	}
	return out
}

func (s *Service) listScorecardsForReferee(ctx context.Context, refID int64, includeToken bool) ([]Scorecard, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT r.id, r.tournament_id, COALESCE(t.name,''), COALESCE(t.start_date::text,''), COALESCE(t.status,''),
       t.facility_id, COALESCE(f.name,''),
       COALESCE(r.category,''), COALESCE(r.gender,''), COALESCE(r.court,''),
       COALESCE(r.pool_no,0), COALESCE(r.round_no,0),
       COALESCE(r.assigned_at::text, r.created_at::text), COALESCE(r.pin_expires_at::text,''),
       COALESCE(r.pin_hash,''), COALESCE(r.access_token,''), COALESCE(r.status,'active')
FROM tournament_referees r
JOIN tournaments t ON t.id = r.tournament_id
LEFT JOIN facilities f ON f.id = t.facility_id
WHERE r.ref_id=$1 AND LOWER(COALESCE(r.status,'active'))='active'
ORDER BY r.id DESC`, refID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Scorecard{}
	now := time.Now().UTC()
	for rows.Next() {
		var c Scorecard
		var pinHash, token, status string
		var facilityID int64
		if err := rows.Scan(
			&c.AssignmentID, &c.TourID, &c.TournamentName, &c.TournamentDate, &c.TournStatus,
			&facilityID, &c.OwnerName,
			&c.Category, &c.Gender, &c.Court, &c.PoolNo, &c.RoundNo,
			&c.AssignedAt, &c.ExpiresAt, &pinHash, &token, &status,
		); err != nil {
			return nil, err
		}
		if exp, err := time.Parse(time.RFC3339, c.ExpiresAt); err == nil && exp.Before(now) {
			_, _ = s.DB.ExecContext(ctx, `UPDATE tournament_referees SET status='expired', pin_hash=NULL WHERE id=$1`, c.AssignmentID)
			continue
		}
		c.OwnerType = "facility"
		c.OwnerID = facilityID
		if c.OwnerName == "" {
			c.OwnerName = fmt.Sprintf("Facility #%d", facilityID)
		}
		c.OwnerLabel = "Facility / Academy"
		c.RequiresAccessCode = strings.TrimSpace(pinHash) != ""
		c.Unlocked = !c.RequiresAccessCode
		c.CodeValidHours = 24
		c.AccessCode = nil
		if includeToken && c.Unlocked {
			c.Token = token
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Service) UnlockRefereeScorecard(ctx context.Context, refID, assignmentID int64, code string) (*Scorecard, error) {
	code = strings.TrimSpace(code)
	if !regexpSixDigit(code) {
		return nil, fmt.Errorf("invalid_access_code")
	}
	if refID <= 0 || assignmentID <= 0 {
		return nil, fmt.Errorf("invalid_scorecard")
	}
	var pinHash, token, expiresAt string
	var tourID int64
	err := s.DB.QueryRowContext(ctx, `
SELECT COALESCE(pin_hash,''), COALESCE(access_token,''), COALESCE(pin_expires_at::text,''), tournament_id
FROM tournament_referees
WHERE id=$1 AND ref_id=$2 AND LOWER(COALESCE(status,'active'))='active'`,
		assignmentID, refID).Scan(&pinHash, &token, &expiresAt, &tourID)
	if err != nil {
		return nil, fmt.Errorf("not_found")
	}
	if strings.TrimSpace(pinHash) == "" {
		return nil, fmt.Errorf("access_code_already_used")
	}
	if exp, e := time.Parse(time.RFC3339, expiresAt); e == nil && exp.Before(time.Now().UTC()) {
		_, _ = s.DB.ExecContext(ctx, `UPDATE tournament_referees SET status='expired', pin_hash=NULL WHERE id=$1`, assignmentID)
		return nil, fmt.Errorf("access_code_expired")
	}
	if bcrypt.CompareHashAndPassword([]byte(pinHash), []byte(code)) != nil {
		return nil, fmt.Errorf("invalid_access_code")
	}
	_, err = s.DB.ExecContext(ctx, `
UPDATE tournament_referees SET pin_hash=NULL WHERE id=$1 AND ref_id=$2`, assignmentID, refID)
	if err != nil {
		return nil, err
	}
	if token == "" {
		link, err := s.CreateRefLink(ctx, tourID, CreateRefLinkInput{})
		if err != nil {
			return nil, err
		}
		token = link.Token
		_, _ = s.DB.ExecContext(ctx, `UPDATE tournament_referees SET access_token=$1 WHERE id=$2`, token, assignmentID)
	}
	cards, err := s.listScorecardsForReferee(ctx, refID, true)
	if err != nil {
		return nil, err
	}
	for i := range cards {
		if cards[i].AssignmentID == assignmentID {
			cards[i].Token = token
			cards[i].RequiresAccessCode = false
			cards[i].Unlocked = true
			return &cards[i], nil
		}
	}
	return &Scorecard{
		AssignmentID: assignmentID, TourID: tourID, Token: token,
		RequiresAccessCode: false, Unlocked: true, CodeValidHours: 24,
	}, nil
}

func regexpSixDigit(code string) bool {
	if len(code) != 6 {
		return false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (s *Service) ValidateRefereeAssignment(ctx context.Context, refID, assignmentID int64) (*Scorecard, error) {
	cards, err := s.listScorecardsForReferee(ctx, refID, true)
	if err != nil {
		return nil, err
	}
	for i := range cards {
		if cards[i].AssignmentID == assignmentID {
			c := cards[i]
			c.Token = "" // only return token when unlocked
			if c.Unlocked {
				// re-fetch token
				_ = s.DB.QueryRowContext(ctx, `
SELECT COALESCE(access_token,'') FROM tournament_referees WHERE id=$1`, assignmentID).Scan(&c.Token)
			}
			return &c, nil
		}
	}
	return nil, fmt.Errorf("not_found")
}

var _ = sql.ErrNoRows
