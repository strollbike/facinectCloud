package tournaments

import (
	"strings"
)

// SportRules controls standings points and ranking (PHP sportRules subset).
type SportRules struct {
	PtsWin            float64
	PtsDraw           float64
	PtsLoss           float64
	ScoreType         string
	RankingRules      []string
	QualifiersPerPool int
	EliminatedPerPool *int
}

func DefaultSportRules() SportRules {
	return SportRules{
		PtsWin:            2,
		PtsDraw:           0,
		PtsLoss:           0,
		ScoreType:         "numeric",
		RankingRules:      []string{"match_wins", "point_difference", "points_for", "head_to_head"},
		QualifiersPerPool: 2,
	}
}

func SportRulesFromKnockout(in KnockoutInput) SportRules {
	r := DefaultSportRules()
	r.EliminatedPerPool = in.EliminatedPerPool
	return r
}

// StandingRow is one team's pool table row.
type StandingRow struct {
	SideName         string   `json:"side_name"`
	Played           int      `json:"played"`
	Wins             int      `json:"wins"`
	Draws            int      `json:"draws"`
	Losses           int      `json:"losses"`
	ScoreFor         int      `json:"score_for"`
	ScoreAgainst     int      `json:"score_against"`
	ScoreDifference  int      `json:"score_difference"`
	PointsFor        int      `json:"points_for"`
	PointsAgainst    int      `json:"points_against"`
	PointDifference  int      `json:"point_difference"`
	Points           float64  `json:"points"`
	Buchholz         float64  `json:"buchholz"`
	P                int      `json:"p"`
	W                int      `json:"w"`
	D                int      `json:"d"`
	L                int      `json:"l"`
	Pts              float64  `json:"pts"`
	Opponents        []string `json:"-"`
}

func fixtureCompleted(st string) bool {
	return strings.EqualFold(strings.TrimSpace(st), "completed") ||
		strings.EqualFold(st, "finished") ||
		strings.EqualFold(st, "done")
}

// BuildStandingsByPool builds unsorted standings maps per pool (PHP StandingsEngine::buildByPool).
func BuildStandingsByPool(matches []Fixture, rules SportRules) map[int]map[string]*StandingRow {
	if rules.PtsWin == 0 && rules.PtsDraw == 0 && rules.PtsLoss == 0 && rules.QualifiersPerPool == 0 {
		// zero-value rules → defaults (but PtsWin 2 is intentional default)
	}
	if rules.RankingRules == nil {
		rules = DefaultSportRules()
		// preserve elim if set on empty
	}
	ptsWin, ptsDraw, ptsLoss := rules.PtsWin, rules.PtsDraw, rules.PtsLoss
	if ptsWin == 0 && ptsDraw == 0 && ptsLoss == 0 {
		ptsWin, ptsDraw, ptsLoss = 2, 0, 0
	}

	byPool := map[int]map[string]*StandingRow{}
	ensure := func(pool int, name string) *StandingRow {
		if pool <= 0 || name == "" || strings.EqualFold(name, "BYE") {
			return nil
		}
		if byPool[pool] == nil {
			byPool[pool] = map[string]*StandingRow{}
		}
		if byPool[pool][name] == nil {
			byPool[pool][name] = &StandingRow{SideName: name, Opponents: []string{}}
		}
		return byPool[pool][name]
	}

	for _, m := range matches {
		if m.RoundNo != 1 || m.PoolNo == nil || *m.PoolNo <= 0 {
			continue
		}
		ensure(*m.PoolNo, m.Team1)
		ensure(*m.PoolNo, m.Team2)
	}

	for _, m := range matches {
		if m.RoundNo != 1 || m.PoolNo == nil || *m.PoolNo <= 0 {
			continue
		}
		if !fixtureCompleted(m.Status) || m.IsBye {
			continue
		}
		p1, p2 := strings.TrimSpace(m.Team1), strings.TrimSpace(m.Team2)
		if p1 == "" || p2 == "" || strings.EqualFold(p1, "BYE") || strings.EqualFold(p2, "BYE") {
			continue
		}
		r1 := ensure(*m.PoolNo, p1)
		r2 := ensure(*m.PoolNo, p2)
		if r1 == nil || r2 == nil {
			continue
		}
		s1, s2 := 0, 0
		if m.Score1 != nil {
			s1 = *m.Score1
		}
		if m.Score2 != nil {
			s2 = *m.Score2
		}
		// numeric scoreType: score_for = board score; points_for = same
		r1.Played++
		r2.Played++
		r1.ScoreFor += s1
		r1.ScoreAgainst += s2
		r2.ScoreFor += s2
		r2.ScoreAgainst += s1
		r1.PointsFor += s1
		r1.PointsAgainst += s2
		r2.PointsFor += s2
		r2.PointsAgainst += s1
		r1.Opponents = append(r1.Opponents, p2)
		r2.Opponents = append(r2.Opponents, p1)

		win := strings.TrimSpace(m.Winner)
		isDraw := win == "" || s1 == s2
		if isDraw {
			r1.Draws++
			r2.Draws++
			r1.Points += ptsDraw
			r2.Points += ptsDraw
		} else if win == p1 || s1 > s2 {
			r1.Wins++
			r2.Losses++
			r1.Points += ptsWin
			r2.Points += ptsLoss
		} else {
			r2.Wins++
			r1.Losses++
			r2.Points += ptsWin
			r1.Points += ptsLoss
		}
	}

	for _, rows := range byPool {
		for _, row := range rows {
			row.ScoreDifference = row.ScoreFor - row.ScoreAgainst
			row.PointDifference = row.PointsFor - row.PointsAgainst
		}
		for _, row := range rows {
			buch := 0.0
			for _, opp := range row.Opponents {
				if o, ok := rows[opp]; ok {
					buch += o.Points
				}
			}
			row.Buchholz = buch
			row.P, row.W, row.D, row.L, row.Pts = row.Played, row.Wins, row.Draws, row.Losses, row.Points
			row.Opponents = nil
		}
	}
	return byPool
}
