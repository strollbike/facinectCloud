package tournaments

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// --- Entries ---

type Entry struct {
	ID            int64  `json:"id"`
	TournamentID  int64  `json:"tournament_id"`
	Category      string `json:"category"`
	Gender        string `json:"gender"`
	PlayerName    string `json:"player_name"`
	Phone         string `json:"phone,omitempty"`
	Email         string `json:"email,omitempty"`
	PaymentStatus string `json:"payment_status"`
	Status        string `json:"status"`
	CreatedAt     string `json:"created_at,omitempty"`
}

type EntryInput struct {
	Category      string `json:"category"`
	Gender        string `json:"gender"`
	PlayerName    string `json:"player_name"`
	Phone         string `json:"phone"`
	Email         string `json:"email"`
	PaymentStatus string `json:"payment_status"`
	Status        string `json:"status"`
}

func (s *Service) ListEntries(ctx context.Context, tournamentID int64) ([]Entry, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, tournament_id, COALESCE(category,''), COALESCE(gender,''),
       player_name, COALESCE(phone,''), COALESCE(email,''),
       payment_status, status, created_at::text
FROM tournament_entries WHERE tournament_id=$1 ORDER BY id`, tournamentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.TournamentID, &e.Category, &e.Gender,
			&e.PlayerName, &e.Phone, &e.Email, &e.PaymentStatus, &e.Status, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Service) CreateEntry(ctx context.Context, tournamentID int64, in EntryInput) (*Entry, error) {
	name := strings.TrimSpace(in.PlayerName)
	if tournamentID <= 0 || name == "" {
		return nil, fmt.Errorf("invalid_entry")
	}
	pay := strings.TrimSpace(in.PaymentStatus)
	if pay == "" {
		pay = "pending"
	}
	st := strings.TrimSpace(in.Status)
	if st == "" {
		st = "active"
	}
	var id int64
	err := s.DB.QueryRowContext(ctx, `
INSERT INTO tournament_entries
  (tournament_id, category, gender, player_name, phone, email, payment_status, status)
VALUES ($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),$7,$8)
RETURNING id`,
		tournamentID,
		strings.TrimSpace(in.Category),
		strings.TrimSpace(in.Gender),
		name,
		strings.TrimSpace(in.Phone),
		strings.TrimSpace(in.Email),
		pay, st,
	).Scan(&id)
	if err != nil {
		return nil, err
	}
	entry, err := s.getEntry(ctx, id)
	if err != nil {
		return nil, err
	}
	// Paid/spot on create → confirmation WA (PHP webhook / spot path).
	if isPaidTournamentStatus(pay) {
		s.notifyTournamentRegistrationConfirmation(ctx, entry, 0, "")
	}
	return entry, nil
}

func (s *Service) GetEntry(ctx context.Context, id int64) (*Entry, error) {
	return s.getEntry(ctx, id)
}

func (s *Service) getEntry(ctx context.Context, id int64) (*Entry, error) {
	var e Entry
	err := s.DB.QueryRowContext(ctx, `
SELECT id, tournament_id, COALESCE(category,''), COALESCE(gender,''),
       player_name, COALESCE(phone,''), COALESCE(email,''),
       payment_status, status, created_at::text
FROM tournament_entries WHERE id=$1`, id).Scan(
		&e.ID, &e.TournamentID, &e.Category, &e.Gender,
		&e.PlayerName, &e.Phone, &e.Email, &e.PaymentStatus, &e.Status, &e.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *Service) UpdateEntryPayment(ctx context.Context, entryID int64, paymentStatus string) (*Entry, error) {
	pay := strings.TrimSpace(paymentStatus)
	if entryID <= 0 || pay == "" {
		return nil, fmt.Errorf("invalid_payment_status")
	}
	prev, _ := s.getEntry(ctx, entryID)
	res, err := s.DB.ExecContext(ctx, `
UPDATE tournament_entries SET payment_status=$1 WHERE id=$2`, pay, entryID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("not_found")
	}
	entry, err := s.getEntry(ctx, entryID)
	if err != nil {
		return nil, err
	}
	wasPaid := prev != nil && isPaidTournamentStatus(prev.PaymentStatus)
	if isPaidTournamentStatus(pay) && !wasPaid {
		s.notifyTournamentRegistrationConfirmation(ctx, entry, 0, pay)
	}
	return entry, nil
}

func isPaidTournamentStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "paid", "completed", "success", "spot":
		return true
	default:
		return false
	}
}

// --- Fixtures ---

type Fixture struct {
	ID           int64  `json:"id"`
	TournamentID int64  `json:"tournament_id"`
	Category     string `json:"category"`
	Gender       string `json:"gender"`
	RoundNo      int    `json:"round_no"`
	MatchNo      int    `json:"match_no"`
	Team1        string `json:"team1"`
	Team2        string `json:"team2"`
	Score1       *int   `json:"score1,omitempty"`
	Score2       *int   `json:"score2,omitempty"`
	Winner       string `json:"winner,omitempty"`
	Status       string `json:"status"`
	Court        string `json:"court,omitempty"`
	RefID        *int64 `json:"ref_id,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}

type ScoreInput struct {
	Score1 *int   `json:"score1"`
	Score2 *int   `json:"score2"`
	Status string `json:"status"`
	Winner string `json:"winner"`
}

func (s *Service) ListFixtures(ctx context.Context, tournamentID int64) ([]Fixture, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, tournament_id, COALESCE(category,''), COALESCE(gender,''),
       round_no, match_no, team1, team2, score1, score2,
       COALESCE(winner,''), status, COALESCE(court,''), ref_id,
       created_at::text, updated_at::text
FROM tournament_fixtures WHERE tournament_id=$1
ORDER BY round_no, match_no, id`, tournamentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Fixture{}
	for rows.Next() {
		f, err := scanFixture(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

func (s *Service) GenerateFixtures(ctx context.Context, tournamentID int64) ([]Fixture, error) {
	existing, err := s.ListFixtures(ctx, tournamentID)
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		return existing, nil
	}
	entries, err := s.ListEntries(ctx, tournamentID)
	if err != nil {
		return nil, err
	}
	// Group by category|gender
	groups := map[string][]Entry{}
	for _, e := range entries {
		if strings.ToLower(e.Status) != "active" && e.Status != "" {
			continue
		}
		key := e.Category + "|" + e.Gender
		groups[key] = append(groups[key], e)
	}
	matchNo := 0
	for key, list := range groups {
		parts := strings.SplitN(key, "|", 2)
		cat, gen := parts[0], ""
		if len(parts) > 1 {
			gen = parts[1]
		}
		for i := 0; i < len(list); i++ {
			for j := i + 1; j < len(list); j++ {
				matchNo++
				_, err := s.DB.ExecContext(ctx, `
INSERT INTO tournament_fixtures
  (tournament_id, category, gender, round_no, match_no, team1, team2, status)
VALUES ($1,$2,$3,1,$4,$5,$6,'scheduled')`,
					tournamentID, cat, gen, matchNo, list[i].PlayerName, list[j].PlayerName)
				if err != nil {
					return nil, err
				}
			}
		}
	}
	return s.ListFixtures(ctx, tournamentID)
}

func (s *Service) GetFixture(ctx context.Context, id int64) (*Fixture, error) {
	return s.getFixture(ctx, id)
}

func (s *Service) getFixture(ctx context.Context, id int64) (*Fixture, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT id, tournament_id, COALESCE(category,''), COALESCE(gender,''),
       round_no, match_no, team1, team2, score1, score2,
       COALESCE(winner,''), status, COALESCE(court,''), ref_id,
       created_at::text, updated_at::text
FROM tournament_fixtures WHERE id=$1`, id)
	return scanFixture(row)
}

func (s *Service) UpdateFixtureScore(ctx context.Context, fixtureID int64, in ScoreInput) (*Fixture, error) {
	cur, err := s.getFixture(ctx, fixtureID)
	if err != nil {
		return nil, err
	}
	st := strings.TrimSpace(in.Status)
	if st == "" {
		st = cur.Status
	}
	winner := strings.TrimSpace(in.Winner)
	var score1, score2 interface{}
	if in.Score1 != nil {
		score1 = *in.Score1
	} else if cur.Score1 != nil {
		score1 = *cur.Score1
	}
	if in.Score2 != nil {
		score2 = *in.Score2
	} else if cur.Score2 != nil {
		score2 = *cur.Score2
	}
	_, err = s.DB.ExecContext(ctx, `
UPDATE tournament_fixtures SET
  score1=$1, score2=$2, status=$3, winner=NULLIF($4,''), updated_at=NOW()
WHERE id=$5`, score1, score2, st, winner, fixtureID)
	if err != nil {
		return nil, err
	}
	_, err = s.DB.ExecContext(ctx, `
INSERT INTO tournament_score_events
  (tournament_id, category, match_id, score1, score2, status, winner)
VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''))`,
		cur.TournamentID, cur.Category, fixtureID, score1, score2, st, winner)
	if err != nil {
		return nil, err
	}
	return s.getFixture(ctx, fixtureID)
}

func scanFixture(row scannable) (*Fixture, error) {
	var f Fixture
	var s1, s2 sql.NullInt64
	var refID sql.NullInt64
	err := row.Scan(
		&f.ID, &f.TournamentID, &f.Category, &f.Gender,
		&f.RoundNo, &f.MatchNo, &f.Team1, &f.Team2, &s1, &s2,
		&f.Winner, &f.Status, &f.Court, &refID, &f.CreatedAt, &f.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if s1.Valid {
		v := int(s1.Int64)
		f.Score1 = &v
	}
	if s2.Valid {
		v := int(s2.Int64)
		f.Score2 = &v
	}
	if refID.Valid {
		v := refID.Int64
		f.RefID = &v
	}
	return &f, nil
}

// --- Score live ---

type ScoreEvent struct {
	ID           int64  `json:"id"`
	TournamentID int64  `json:"tournament_id"`
	Category     string `json:"category"`
	MatchID      int64  `json:"match_id"`
	Score1       *int   `json:"score1,omitempty"`
	Score2       *int   `json:"score2,omitempty"`
	Status       string `json:"status,omitempty"`
	Winner       string `json:"winner,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
}

func (s *Service) PollScoreEvents(ctx context.Context, tournamentID int64, category string, afterID int64, waitSec int) ([]ScoreEvent, error) {
	deadline := time.Now()
	if waitSec > 0 {
		if waitSec > 30 {
			waitSec = 30
		}
		deadline = time.Now().Add(time.Duration(waitSec) * time.Second)
	}
	for {
		events, err := s.listScoreEvents(ctx, tournamentID, category, afterID)
		if err != nil {
			return nil, err
		}
		if len(events) > 0 || waitSec <= 0 || time.Now().After(deadline) {
			return events, nil
		}
		select {
		case <-ctx.Done():
			return events, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (s *Service) listScoreEvents(ctx context.Context, tournamentID int64, category string, afterID int64) ([]ScoreEvent, error) {
	q := `
SELECT id, tournament_id, COALESCE(category,''), match_id,
       score1, score2, COALESCE(status,''), COALESCE(winner,''), created_at::text
FROM tournament_score_events
WHERE tournament_id=$1 AND id>$2`
	args := []interface{}{tournamentID, afterID}
	if strings.TrimSpace(category) != "" {
		q += ` AND category=$3`
		args = append(args, strings.TrimSpace(category))
	}
	q += ` ORDER BY id ASC LIMIT 100`
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

// --- Templates ---

type Template struct {
	ID           int64  `json:"id"`
	TournamentID int64  `json:"tournament_id"`
	ImageURL     string `json:"image_url"`
	Ext          string `json:"ext,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
}

type TemplateInput struct {
	ImageURL string `json:"image_url"`
	Ext      string `json:"ext"`
}

func (s *Service) GetTemplate(ctx context.Context, tournamentID int64) (*Template, error) {
	var t Template
	err := s.DB.QueryRowContext(ctx, `
SELECT id, tournament_id, COALESCE(image_url,''), COALESCE(ext,''), created_at::text
FROM tournament_templates WHERE tournament_id=$1`, tournamentID).Scan(
		&t.ID, &t.TournamentID, &t.ImageURL, &t.Ext, &t.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Service) SaveTemplate(ctx context.Context, tournamentID int64, in TemplateInput) (*Template, error) {
	url := strings.TrimSpace(in.ImageURL)
	if tournamentID <= 0 || url == "" {
		return nil, fmt.Errorf("invalid_template")
	}
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO tournament_templates (tournament_id, image_url, ext)
VALUES ($1,$2,NULLIF($3,''))
ON CONFLICT (tournament_id) DO UPDATE SET image_url=EXCLUDED.image_url, ext=EXCLUDED.ext`,
		tournamentID, url, strings.TrimSpace(in.Ext))
	if err != nil {
		return nil, err
	}
	return s.GetTemplate(ctx, tournamentID)
}

func (s *Service) DeleteTemplate(ctx context.Context, tournamentID int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM tournament_templates WHERE tournament_id=$1`, tournamentID)
	return err
}

// --- Publish ---

func (s *Service) Publish(ctx context.Context, facilityID, tournamentID int64) (*Tournament, error) {
	return s.UpdateStatus(ctx, facilityID, tournamentID, "upcoming")
}

// --- Referees ---

type Referee struct {
	ID           int64  `json:"id"`
	TournamentID int64  `json:"tournament_id"`
	Category     string `json:"category"`
	Gender       string `json:"gender"`
	Court        string `json:"court"`
	Email        string `json:"email,omitempty"`
	RefID        *int64 `json:"ref_id,omitempty"`
	PoolNo       *int   `json:"pool_no,omitempty"`
	RoundNo      *int   `json:"round_no,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
}

type AssignCourtInput struct {
	Category string `json:"category"`
	Gender   string `json:"gender"`
	Court    string `json:"court"`
	Email    string `json:"email"`
	RefID    *int64 `json:"ref_id"`
	PoolNo   *int   `json:"pool_no"`
	RoundNo  *int   `json:"round_no"`
}

func (s *Service) ListRefereesByFacility(ctx context.Context, facilityID int64) ([]Referee, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT r.id, r.tournament_id, COALESCE(r.category,''), COALESCE(r.gender,''),
       COALESCE(r.court,''), COALESCE(r.email,''), r.ref_id, r.pool_no, r.round_no,
       r.created_at::text
FROM tournament_referees r
JOIN tournaments t ON t.id = r.tournament_id
WHERE t.facility_id=$1
ORDER BY r.id DESC`, facilityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReferees(rows)
}

func (s *Service) AssignRefereeCourt(ctx context.Context, tournamentID int64, in AssignCourtInput) (*Referee, error) {
	court := strings.TrimSpace(in.Court)
	if tournamentID <= 0 || court == "" {
		return nil, fmt.Errorf("invalid_assign")
	}
	var pool, round, refID interface{}
	if in.PoolNo != nil {
		pool = *in.PoolNo
	}
	if in.RoundNo != nil {
		round = *in.RoundNo
	}
	if in.RefID != nil {
		refID = *in.RefID
	}
	var id int64
	err := s.DB.QueryRowContext(ctx, `
INSERT INTO tournament_referees
  (tournament_id, category, gender, court, email, ref_id, pool_no, round_no)
VALUES ($1,$2,$3,$4,NULLIF($5,''),$6,$7,$8)
RETURNING id`,
		tournamentID,
		strings.TrimSpace(in.Category),
		strings.TrimSpace(in.Gender),
		court,
		strings.TrimSpace(in.Email),
		refID, pool, round,
	).Scan(&id)
	if err != nil {
		return nil, err
	}
	return s.getReferee(ctx, id)
}

func (s *Service) UnassignRefereeCourt(ctx context.Context, tournamentID int64, in AssignCourtInput) error {
	court := strings.TrimSpace(in.Court)
	cat := strings.TrimSpace(in.Category)
	if tournamentID <= 0 || court == "" {
		return fmt.Errorf("invalid_unassign")
	}
	_, err := s.DB.ExecContext(ctx, `
DELETE FROM tournament_referees
WHERE tournament_id=$1 AND court=$2 AND ($3='' OR category=$3)`,
		tournamentID, court, cat)
	return err
}

func (s *Service) ListRefereesByCourt(ctx context.Context, tournamentID int64, category, court string) ([]Referee, error) {
	q := `
SELECT id, tournament_id, COALESCE(category,''), COALESCE(gender,''),
       COALESCE(court,''), COALESCE(email,''), ref_id, pool_no, round_no,
       created_at::text
FROM tournament_referees WHERE tournament_id=$1`
	args := []interface{}{tournamentID}
	n := 2
	if strings.TrimSpace(category) != "" {
		q += fmt.Sprintf(` AND category=$%d`, n)
		args = append(args, strings.TrimSpace(category))
		n++
	}
	if strings.TrimSpace(court) != "" {
		q += fmt.Sprintf(` AND court=$%d`, n)
		args = append(args, strings.TrimSpace(court))
	}
	q += ` ORDER BY id`
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReferees(rows)
}

func (s *Service) getReferee(ctx context.Context, id int64) (*Referee, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT id, tournament_id, COALESCE(category,''), COALESCE(gender,''),
       COALESCE(court,''), COALESCE(email,''), ref_id, pool_no, round_no,
       created_at::text
FROM tournament_referees WHERE id=$1`, id)
	list, err := scanReferees(row)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, sql.ErrNoRows
	}
	return &list[0], nil
}

type refereeScanner interface {
	Next() bool
	Scan(dest ...interface{}) error
	Err() error
}

type singleRefereeRow struct {
	row scannable
	done bool
	err  error
}

func (s *singleRefereeRow) Next() bool {
	if s.done {
		return false
	}
	s.done = true
	return true
}

func (s *singleRefereeRow) Scan(dest ...interface{}) error {
	s.err = s.row.Scan(dest...)
	return s.err
}

func (s *singleRefereeRow) Err() error { return s.err }

func scanReferees(src interface{}) ([]Referee, error) {
	var rs refereeScanner
	switch v := src.(type) {
	case *sql.Rows:
		rs = v
	case scannable:
		rs = &singleRefereeRow{row: v}
	default:
		return nil, fmt.Errorf("invalid_scanner")
	}
	out := []Referee{}
	for rs.Next() {
		var r Referee
		var refID sql.NullInt64
		var pool, round sql.NullInt64
		if err := rs.Scan(&r.ID, &r.TournamentID, &r.Category, &r.Gender,
			&r.Court, &r.Email, &refID, &pool, &round, &r.CreatedAt); err != nil {
			return nil, err
		}
		if refID.Valid {
			v := refID.Int64
			r.RefID = &v
		}
		if pool.Valid {
			v := int(pool.Int64)
			r.PoolNo = &v
		}
		if round.Valid {
			v := int(round.Int64)
			r.RoundNo = &v
		}
		out = append(out, r)
	}
	return out, rs.Err()
}
