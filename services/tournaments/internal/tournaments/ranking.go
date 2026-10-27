package tournaments

import (
	"sort"
	"strings"
)

func DefaultRankingRules() []string {
	return []string{"match_wins", "point_difference", "points_for", "head_to_head"}
}

// RankStandings orders rows best-first (PHP RankingEngine::rank).
func RankStandings(rows []*StandingRow, rules []string, poolMatches []Fixture) []*StandingRow {
	if len(rows) == 0 {
		return nil
	}
	if len(rules) == 0 {
		rules = DefaultRankingRules()
	}
	out := append([]*StandingRow{}, rows...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		for _, rule := range rules {
			cmp := compareByRule(a, b, rule, poolMatches)
			if cmp != 0 {
				return cmp < 0
			}
		}
		return a.SideName < b.SideName
	})
	return out
}

func TopNames(standings map[string]*StandingRow, rules []string, n int, poolMatches []Fixture) []string {
	rows := make([]*StandingRow, 0, len(standings))
	for _, r := range standings {
		rows = append(rows, r)
	}
	ranked := RankStandings(rows, rules, poolMatches)
	out := []string{}
	for _, r := range ranked {
		if len(out) >= n {
			break
		}
		out = append(out, r.SideName)
	}
	return out
}

func compareByRule(a, b *StandingRow, rule string, poolMatches []Fixture) int {
	rule = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(rule), " ", "_"), "-", "_"))
	switch rule {
	case "points":
		return descF(a.Points, b.Points)
	case "wins", "match_wins":
		return descI(a.Wins, b.Wins)
	case "draws":
		return descI(a.Draws, b.Draws)
	case "losses":
		return ascI(a.Losses, b.Losses)
	case "score_difference", "goal_difference":
		return descI(a.ScoreDifference, b.ScoreDifference)
	case "score_for", "goals_for":
		return descI(a.ScoreFor, b.ScoreFor)
	case "points_for", "points_scored":
		return descI(a.PointsFor, b.PointsFor)
	case "point_difference", "points_difference":
		return descI(a.PointDifference, b.PointDifference)
	case "buchholz":
		return descF(a.Buchholz, b.Buchholz)
	case "head_to_head":
		return headToHead(a, b, poolMatches)
	default:
		return 0
	}
}

func descI(a, b int) int {
	if a == b {
		return 0
	}
	if a > b {
		return -1
	}
	return 1
}

func ascI(a, b int) int {
	if a == b {
		return 0
	}
	if a < b {
		return -1
	}
	return 1
}

func descF(a, b float64) int {
	if a == b {
		return 0
	}
	if a > b {
		return -1
	}
	return 1
}

func headToHead(a, b *StandingRow, poolMatches []Fixture) int {
	na, nb := a.SideName, b.SideName
	wa, wb := 0, 0
	for _, m := range poolMatches {
		if !fixtureCompleted(m.Status) {
			continue
		}
		p1, p2 := m.Team1, m.Team2
		if !((p1 == na && p2 == nb) || (p1 == nb && p2 == na)) {
			continue
		}
		win := strings.TrimSpace(m.Winner)
		if win == na {
			wa++
		} else if win == nb {
			wb++
		}
	}
	return descI(wa, wb)
}
