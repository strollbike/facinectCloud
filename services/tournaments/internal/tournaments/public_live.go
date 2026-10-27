package tournaments

import (
	"context"
	"fmt"
	"strings"
)

type PublicLiveListItem struct {
	TourID         int64    `json:"tourId"`
	TournamentName string   `json:"tournamentName"`
	TournamentDate string   `json:"tournamentDate,omitempty"`
	FacilityID     int64    `json:"facilityId"`
	FacilityName   string   `json:"facilityName,omitempty"`
	SportName      string   `json:"sportName,omitempty"`
	Categories     []string `json:"categories"`
	OpenMatchCount int      `json:"openMatchCount"`
}

type PublicLiveBoard struct {
	TourID         int64                    `json:"tourId"`
	TournamentName string                   `json:"tournamentName"`
	TournamentDate string                   `json:"tournamentDate,omitempty"`
	FacilityID     int64                    `json:"facilityId"`
	FacilityName   string                   `json:"facilityName,omitempty"`
	FacilityLogoURL string                  `json:"facilityLogoUrl,omitempty"`
	SportName      string                   `json:"sportName,omitempty"`
	Category       string                   `json:"category,omitempty"`
	Categories     []string                 `json:"categories"`
	AfterID        int64                    `json:"afterId"`
	Matches        []map[string]interface{} `json:"matches"`
}

func isPublicLiveStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "upcoming", "live":
		return true
	default:
		return false
	}
}

// ListPublicLiveTournaments mirrors PHP live_arena list for published tours.
func (s *Service) ListPublicLiveTournaments(ctx context.Context) ([]PublicLiveListItem, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT t.id, t.name, COALESCE(t.start_date::text,''), t.facility_id, COALESCE(f.name,'')
FROM tournaments t
LEFT JOIN facilities f ON f.id = t.facility_id
WHERE LOWER(t.status) IN ('upcoming','live')
ORDER BY COALESCE(t.start_date, DATE '1970-01-01') DESC, t.id DESC
LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PublicLiveListItem{}
	for rows.Next() {
		var item PublicLiveListItem
		if err := rows.Scan(&item.TourID, &item.TournamentName, &item.TournamentDate, &item.FacilityID, &item.FacilityName); err != nil {
			return nil, err
		}
		fixtures, err := s.ListFixtures(ctx, item.TourID)
		if err != nil {
			return nil, err
		}
		cats := map[string]bool{}
		open := 0
		for _, f := range fixtures {
			if f.IsBye {
				continue
			}
			if c := strings.TrimSpace(f.Category); c != "" {
				cats[c] = true
			}
			if !isCompletedFixtureStatus(f.Status) {
				open++
			}
		}
		item.Categories = make([]string, 0, len(cats))
		for c := range cats {
			item.Categories = append(item.Categories, c)
		}
		item.OpenMatchCount = open
		out = append(out, item)
	}
	return out, rows.Err()
}

// GetPublicLiveBoard returns a spectator board for a published tournament.
func (s *Service) GetPublicLiveBoard(ctx context.Context, tournamentID int64, category, gender string) (*PublicLiveBoard, error) {
	if tournamentID <= 0 {
		return nil, fmt.Errorf("invalid_tournament")
	}
	tour, err := s.Get(ctx, tournamentID)
	if err != nil {
		return nil, fmt.Errorf("not_found")
	}
	if !isPublicLiveStatus(tour.Status) {
		return nil, fmt.Errorf("not_public")
	}
	fixtures, err := s.listFixturesFiltered(ctx, tournamentID, category, gender)
	if err != nil {
		return nil, err
	}
	cats := map[string]bool{}
	matches := make([]map[string]interface{}, 0, len(fixtures))
	var afterID int64
	for _, f := range fixtures {
		if c := strings.TrimSpace(f.Category); c != "" {
			cats[c] = true
		}
		if f.ID > afterID {
			afterID = f.ID
		}
		var s1, s2 interface{}
		if f.Score1 != nil {
			s1 = *f.Score1
		}
		if f.Score2 != nil {
			s2 = *f.Score2
		}
		matches = append(matches, map[string]interface{}{
			"id": f.ID, "matchId": f.ID, "category": f.Category, "gender": f.Gender,
			"player1": f.Team1, "player2": f.Team2, "team1": f.Team1, "team2": f.Team2,
			"status": f.Status, "winner": f.Winner, "court": f.Court, "match_time": f.MatchTime,
			"score1": s1, "score2": s2, "round_no": f.RoundNo, "match_no": f.MatchNo,
			"round_name": f.RoundName, "is_bye": f.IsBye,
		})
	}
	catList := make([]string, 0, len(cats))
	for c := range cats {
		catList = append(catList, c)
	}
	fname := ""
	_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(name,'') FROM facilities WHERE id=$1`, tour.FacilityID).Scan(&fname)
	return &PublicLiveBoard{
		TourID: tournamentID, TournamentName: tour.Name, TournamentDate: tour.StartDate,
		FacilityID: tour.FacilityID, FacilityName: fname, Category: strings.TrimSpace(category),
		Categories: catList, AfterID: afterID, Matches: matches,
	}, nil
}

// PublicScoreLive polls the score bus without JWT (published tournaments only).
func (s *Service) PublicScoreLive(ctx context.Context, tournamentID, afterID int64, category string, waitSec int) (map[string]interface{}, error) {
	if tournamentID <= 0 {
		return nil, fmt.Errorf("invalid_tournament")
	}
	tour, err := s.Get(ctx, tournamentID)
	if err != nil {
		return nil, fmt.Errorf("not_found")
	}
	if !isPublicLiveStatus(tour.Status) {
		return nil, fmt.Errorf("not_public")
	}
	events, err := s.PollScoreEvents(ctx, tournamentID, category, afterID, waitSec)
	if err != nil {
		return nil, err
	}
	next := afterID
	for _, e := range events {
		if e.ID > next {
			next = e.ID
		}
	}
	return map[string]interface{}{
		"tourId": tournamentID, "afterId": next, "events": events,
	}, nil
}
