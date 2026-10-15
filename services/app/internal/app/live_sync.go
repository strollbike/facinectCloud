package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// syncLiveFromTournaments copies staff tournament rows into app_live_* used by Proximity boards.
func (s *Service) syncLiveFromTournaments(ctx context.Context) {
	if !s.tableExists(ctx, "tournaments") {
		return
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT t.id,
       COALESCE(NULLIF(t.name,''), 'Tournament'),
       COALESCE(t.start_date::text, t.created_at::date::text, CURRENT_DATE::text),
       COALESCE(t.facility_id, 0),
       COALESCE(t.sport_id, 0),
       COALESCE(t.status, 'upcoming')
FROM tournaments t
WHERE COALESCE(t.status,'draft') NOT IN ('cancelled','deleted')
ORDER BY t.id DESC
LIMIT 100`)
	if err != nil {
		return
	}
	defer rows.Close()

	type tourRow struct {
		id, fid, sportID int64
		name, date, status string
	}
	tours := []tourRow{}
	for rows.Next() {
		var tr tourRow
		if err := rows.Scan(&tr.id, &tr.name, &tr.date, &tr.fid, &tr.sportID, &tr.status); err != nil {
			continue
		}
		tours = append(tours, tr)
	}

	for _, tr := range tours {
		sportName := ""
		if tr.sportID > 0 {
			_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(name,'') FROM app_sports WHERE id=$1`, tr.sportID).Scan(&sportName)
		}
		openCount := 0
		_ = s.DB.QueryRowContext(ctx, `
SELECT COUNT(*) FROM tournament_fixtures
WHERE tournament_id=$1 AND LOWER(COALESCE(status,'')) NOT IN ('completed','cancelled','walkover')`, tr.id).Scan(&openCount)

		_, _ = s.DB.ExecContext(ctx, `
INSERT INTO app_live_tournaments (id, tournament_name, tournament_date, facility_id, sport_name, categories, open_match_count)
OVERRIDING SYSTEM VALUE
VALUES ($1,$2,$3::date,$4,$5,'{}',$6)
ON CONFLICT (id) DO UPDATE SET
  tournament_name=EXCLUDED.tournament_name,
  tournament_date=EXCLUDED.tournament_date,
  facility_id=EXCLUDED.facility_id,
  sport_name=CASE WHEN EXCLUDED.sport_name<>'' THEN EXCLUDED.sport_name ELSE app_live_tournaments.sport_name END,
  open_match_count=EXCLUDED.open_match_count`,
			tr.id, tr.name, tr.date, tr.fid, sportName, openCount)

		// Categories aggregate
		catRows, err := s.DB.QueryContext(ctx, `
SELECT DISTINCT NULLIF(category,'') FROM tournament_fixtures WHERE tournament_id=$1`, tr.id)
		if err == nil {
			cats := []string{}
			for catRows.Next() {
				var c *string
				_ = catRows.Scan(&c)
				if c != nil && strings.TrimSpace(*c) != "" {
					cats = append(cats, strings.TrimSpace(*c))
				}
			}
			catRows.Close()
					if len(cats) > 0 {
				quoted := make([]string, 0, len(cats))
				for _, c := range cats {
					quoted = append(quoted, strconv.Quote(c))
				}
				pgArr := "{" + strings.Join(quoted, ",") + "}"
				_, _ = s.DB.ExecContext(ctx, `UPDATE app_live_tournaments SET categories=$2::text[] WHERE id=$1`, tr.id, pgArr)
			}
		}

		fRows, err := s.DB.QueryContext(ctx, `
SELECT id, COALESCE(category,''), COALESCE(gender,''),
       COALESCE(team1,''), COALESCE(team2,''),
       COALESCE(status,'scheduled'), COALESCE(winner,''), COALESCE(court,''),
       COALESCE(score1,0), COALESCE(score2,0)
FROM tournament_fixtures WHERE tournament_id=$1
ORDER BY round_no, match_no, id`, tr.id)
		if err != nil {
			continue
		}
		for fRows.Next() {
			var mid int64
			var cat, gender, p1, p2, status, winner, court string
			var s1, s2 int
			if err := fRows.Scan(&mid, &cat, &gender, &p1, &p2, &status, &winner, &court, &s1, &s2); err != nil {
				continue
			}
			status = mapFixtureStatus(status)
			_, _ = s.DB.ExecContext(ctx, `
INSERT INTO app_live_matches (id, tour_id, category, gender, player1, player2, status, winner, court, score1, score2, version)
OVERRIDING SYSTEM VALUE
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,1)
ON CONFLICT (id) DO UPDATE SET
  tour_id=EXCLUDED.tour_id,
  category=EXCLUDED.category,
  gender=EXCLUDED.gender,
  player1=EXCLUDED.player1,
  player2=EXCLUDED.player2,
  status=EXCLUDED.status,
  winner=EXCLUDED.winner,
  court=EXCLUDED.court,
  score1=EXCLUDED.score1,
  score2=EXCLUDED.score2,
  version=app_live_matches.version+1`,
				mid, tr.id, cat, gender, p1, p2, status, winner, court, s1, s2)
		}
		fRows.Close()
	}
}

func mapFixtureStatus(st string) string {
	switch strings.ToLower(strings.TrimSpace(st)) {
	case "live", "in_progress", "playing":
		return "live"
	case "completed", "done", "finished", "walkover":
		return "completed"
	case "upcoming", "scheduled", "":
		return "upcoming"
	default:
		return strings.ToLower(strings.TrimSpace(st))
	}
}

func (s *Service) liveBoardFromTournaments(ctx context.Context, tourID int64, category string) (name, sport, fname string, fid int64, matches []map[string]any, afterID int64, ok bool) {
	var sportID int64
	err := s.DB.QueryRowContext(ctx, `
SELECT COALESCE(NULLIF(name,''),'Tournament'), COALESCE(facility_id,0), COALESCE(sport_id,0)
FROM tournaments WHERE id=$1`, tourID).Scan(&name, &fid, &sportID)
	if err != nil {
		return "", "", "", 0, nil, 0, false
	}
	if sportID > 0 {
		_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(name,'') FROM app_sports WHERE id=$1`, sportID).Scan(&sport)
	}
	_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(name,'') FROM app_facilities WHERE id=$1`, fid).Scan(&fname)
	if fname == "" {
		_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(name,'') FROM facilities WHERE id=$1`, fid).Scan(&fname)
	}
	q := `
SELECT id, COALESCE(category,''), COALESCE(gender,''),
       COALESCE(team1,''), COALESCE(team2,''),
       COALESCE(status,'scheduled'), COALESCE(winner,''), COALESCE(court,''),
       COALESCE(score1,0), COALESCE(score2,0)
FROM tournament_fixtures WHERE tournament_id=$1`
	args := []any{tourID}
	if strings.TrimSpace(category) != "" {
		q += ` AND category=$2`
		args = append(args, category)
	}
	q += ` ORDER BY round_no, match_no, id`
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return name, sport, fname, fid, nil, 0, true
	}
	defer rows.Close()
	matches = []map[string]any{}
	for rows.Next() {
		var mid int64
		var cat, gender, p1, p2, status, winner, court string
		var s1, s2 int
		_ = rows.Scan(&mid, &cat, &gender, &p1, &p2, &status, &winner, &court, &s1, &s2)
		if mid > afterID {
			afterID = mid
		}
		matches = append(matches, map[string]any{
			"id": mid, "matchId": mid, "category": cat, "gender": gender,
			"player1": p1, "player2": p2, "status": mapFixtureStatus(status),
			"winner": winner, "court": court,
			"live1": s1, "live2": s2, "score_details": fmt.Sprintf("%d-%d", s1, s2),
		})
	}
	return name, sport, fname, fid, matches, afterID, true
}
