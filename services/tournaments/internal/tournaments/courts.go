package tournaments

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// CourtMatchCard is one match tile on the courts board (PHP CourtsBoard row).
type CourtMatchCard struct {
	ID        int64       `json:"id"`
	Player1   string      `json:"player1"`
	Player2   string      `json:"player2"`
	Status    string      `json:"status"`
	Score1    interface{} `json:"score1"`
	Score2    interface{} `json:"score2"`
	MatchTime string      `json:"match_time,omitempty"`
	RoundName string      `json:"round_name,omitempty"`
	RoundNo   int         `json:"round_no"`
	MatchNo   int         `json:"match_no"`
}

type CourtLane struct {
	Court  string           `json:"court"`
	Now    *CourtMatchCard  `json:"now"`
	UpNext []CourtMatchCard `json:"up_next"`
}

type CourtsBoardResult struct {
	TourID        int64      `json:"tourId"`
	Courts        []CourtLane `json:"courts"`
	Conflicts     []string   `json:"conflicts"`
	BracketLocked bool       `json:"bracket_locked"`
}

func fixtureToCard(f Fixture) CourtMatchCard {
	var s1, s2 interface{}
	if f.Score1 != nil {
		s1 = *f.Score1
	}
	if f.Score2 != nil {
		s2 = *f.Score2
	}
	return CourtMatchCard{
		ID: f.ID, Player1: f.Team1, Player2: f.Team2, Status: f.Status,
		Score1: s1, Score2: s2, MatchTime: f.MatchTime, RoundName: f.RoundName,
		RoundNo: f.RoundNo, MatchNo: f.MatchNo,
	}
}

// BuildCourtsBoard mirrors PHP CourtsBoard::build + bracket_locked.
func BuildCourtsBoard(tourID int64, matches []Fixture, seedCourts []string) CourtsBoardResult {
	byCourt := map[string]*CourtLane{}
	for _, c := range seedCourts {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		byCourt[c] = &CourtLane{Court: c, UpNext: []CourtMatchCard{}}
	}

	type pendingItem struct {
		court string
		card  CourtMatchCard
		time  string
	}
	live := []struct {
		court string
		card  CourtMatchCard
	}{}
	pending := []pendingItem{}

	for _, m := range matches {
		if m.IsBye || strings.EqualFold(strings.TrimSpace(m.Team2), "BYE") {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(m.Status))
		court := strings.TrimSpace(m.Court)
		if court == "" {
			court = "Unassigned"
		}
		if byCourt[court] == nil {
			byCourt[court] = &CourtLane{Court: court, UpNext: []CourtMatchCard{}}
		}
		card := fixtureToCard(m)
		switch status {
		case "live", "in_progress", "playing":
			live = append(live, struct {
				court string
				card  CourtMatchCard
			}{court, card})
		case "pending", "scheduled", "ready", "", "not started":
			pending = append(pending, pendingItem{court, card, m.MatchTime})
		}
	}
	for _, item := range live {
		byCourt[item.court].Now = &item.card
	}
	sort.SliceStable(pending, func(i, j int) bool {
		return pending[i].time < pending[j].time
	})
	for _, item := range pending {
		byCourt[item.court].UpNext = append(byCourt[item.court].UpNext, item.card)
	}

	// Stable court order: seeded first, then remaining sorted.
	ordered := make([]CourtLane, 0, len(byCourt))
	seen := map[string]bool{}
	for _, c := range seedCourts {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] || byCourt[c] == nil {
			continue
		}
		seen[c] = true
		ordered = append(ordered, *byCourt[c])
	}
	rest := []string{}
	for c := range byCourt {
		if !seen[c] {
			rest = append(rest, c)
		}
	}
	sort.Strings(rest)
	for _, c := range rest {
		ordered = append(ordered, *byCourt[c])
	}

	return CourtsBoardResult{
		TourID:        tourID,
		Courts:        ordered,
		Conflicts:     detectCourtOverlaps(matches),
		BracketLocked: hasRealResults(matches),
	}
}

func detectCourtOverlaps(matches []Fixture) []string {
	byTime := map[string][]Fixture{}
	for _, m := range matches {
		if m.IsBye {
			continue
		}
		if isCompletedFixtureStatus(m.Status) {
			continue
		}
		t := strings.TrimSpace(m.MatchTime)
		if t == "" || strings.EqualFold(t, "TBD") {
			continue
		}
		byTime[t] = append(byTime[t], m)
	}
	out := []string{}
	seenMsg := map[string]bool{}
	for t, group := range byTime {
		seen := map[string]int64{}
		for _, m := range group {
			for _, name := range []string{m.Team1, m.Team2} {
				name = strings.TrimSpace(name)
				if name == "" || strings.EqualFold(name, "BYE") {
					continue
				}
				key := strings.ToLower(name)
				if prev, ok := seen[key]; ok {
					msg := fmt.Sprintf("%s double-booked at %s (matches %d & %d)", name, t, prev, m.ID)
					if !seenMsg[msg] {
						seenMsg[msg] = true
						out = append(out, msg)
					}
				} else {
					seen[key] = m.ID
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// ScheduleSlot assigns court + HH:MM (PHP SchedulingEngine::slot).
func ScheduleSlot(courts []string, startTime string, durationMin, playableIndex int, isBye bool) (court, matchTime string) {
	if isBye {
		return "", ""
	}
	if len(courts) == 0 {
		courts = []string{"Court 1"}
	}
	if durationMin < 1 {
		durationMin = 20
	}
	court = courts[playableIndex%len(courts)]
	slotIndex := playableIndex / len(courts)
	base, err := time.Parse("15:04", strings.TrimSpace(startTime))
	if err != nil {
		base, _ = time.Parse("15:04", "09:00")
	}
	t := base.Add(time.Duration(slotIndex*durationMin) * time.Minute)
	return court, t.Format("15:04")
}

type ScheduleOpts struct {
	Courts        []string
	StartTime     string
	MatchDuration int
}

func normalizeScheduleOpts(in ScheduleOpts) ScheduleOpts {
	out := in
	if strings.TrimSpace(out.StartTime) == "" {
		out.StartTime = "09:00"
	}
	if out.MatchDuration < 1 {
		out.MatchDuration = 20
	}
	return out
}

// ResolveCourts prefers client list, else active booking_courts / facility_courts.
func (s *Service) ResolveCourts(ctx context.Context, facilityID int64, clientCourts []string) []string {
	out := make([]string, 0, len(clientCourts))
	seen := map[string]bool{}
	for _, c := range clientCourts {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	if len(out) > 0 {
		return out
	}
	if facilityID <= 0 || s.DB == nil {
		return []string{"Court 1"}
	}
	for _, q := range []string{
		`SELECT name FROM booking_courts WHERE facility_id=$1 AND LOWER(COALESCE(status,'')) IN ('active','') ORDER BY name, id`,
		`SELECT name FROM facility_courts WHERE facility_id=$1 AND LOWER(COALESCE(status,'')) IN ('active','') ORDER BY name, id`,
	} {
		rows, err := s.DB.QueryContext(ctx, q, facilityID)
		if err != nil {
			continue
		}
		for rows.Next() {
			var name string
			if rows.Scan(&name) != nil {
				continue
			}
			name = strings.TrimSpace(name)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
		rows.Close()
		if len(out) > 0 {
			return out
		}
	}
	return []string{"Court 1"}
}

// GetCourtsBoard builds the StrollX projector / journey courts board.
func (s *Service) GetCourtsBoard(ctx context.Context, tournamentID int64, category, gender string) (*CourtsBoardResult, error) {
	tour, err := s.Get(ctx, tournamentID)
	if err != nil {
		return nil, err
	}
	matches, err := s.listFixturesFiltered(ctx, tournamentID, category, gender)
	if err != nil {
		return nil, err
	}
	seed := s.ResolveCourts(ctx, tour.FacilityID, nil)
	board := BuildCourtsBoard(tournamentID, matches, seed)
	return &board, nil
}

func (s *Service) scheduleForInsert(ctx context.Context, tournamentID int64, opts ScheduleOpts, playableIndex *int, isBye bool) (court, matchTime string) {
	opts = normalizeScheduleOpts(opts)
	tour, err := s.Get(ctx, tournamentID)
	facilityID := int64(0)
	if err == nil && tour != nil {
		facilityID = tour.FacilityID
	}
	courts := s.ResolveCourts(ctx, facilityID, opts.Courts)
	idx := 0
	if playableIndex != nil {
		idx = *playableIndex
	}
	court, matchTime = ScheduleSlot(courts, opts.StartTime, opts.MatchDuration, idx, isBye)
	if !isBye && playableIndex != nil {
		*playableIndex = idx + 1
	}
	return court, matchTime
}
