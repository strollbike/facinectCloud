package tournaments

import (
	"fmt"
	"sort"
	"strings"
)

func AssertPoolsComplete(matches []Fixture) (ok bool, pending int, message string) {
	for _, m := range matches {
		if m.RoundNo != 1 || m.PoolNo == nil || *m.PoolNo <= 0 {
			continue
		}
		if m.IsBye || strings.EqualFold(m.Team1, "BYE") || strings.EqualFold(m.Team2, "BYE") {
			continue
		}
		if !fixtureCompleted(m.Status) {
			pending++
		}
	}
	if pending > 0 {
		return false, pending, "Complete all pool matches before generating the knockout stage."
	}
	return true, 0, ""
}

func QualifyCountForPool(teamCount int, rules SportRules) int {
	if teamCount < 0 {
		teamCount = 0
	}
	if rules.EliminatedPerPool != nil {
		elim := *rules.EliminatedPerPool
		if elim < 0 {
			elim = 0
		}
		if teamCount <= 0 {
			return 1
		}
		q := teamCount - elim
		if q < 1 {
			return 1
		}
		return q
	}
	q := rules.QualifiersPerPool
	if q < 1 {
		q = 2
	}
	return q
}

// CrossPoolPairOrder: A1,B2,B1,A2 then extras (PHP AdvancementEngine::crossPoolPairOrder).
func CrossPoolPairOrder(topsByPool map[int][]string, qualifiersPerPool int) []string {
	keys := make([]int, 0, len(topsByPool))
	for k := range topsByPool {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	if len(keys) == 0 {
		return nil
	}
	if qualifiersPerPool < 1 {
		qualifiersPerPool = 2
	}
	ordered := []string{}
	for i := 0; i < len(keys); i += 2 {
		a := keys[i]
		aList := topsByPool[a]
		if i+1 >= len(keys) {
			ordered = append(ordered, aList...)
			continue
		}
		b := keys[i+1]
		bList := topsByPool[b]
		a1, a2, b1, b2 := pick(aList, 0), pick(aList, 1), pick(bList, 0), pick(bList, 1)
		if a1 != "" && b2 != "" {
			ordered = append(ordered, a1, b2)
		} else if a1 != "" {
			ordered = append(ordered, a1)
		}
		if b1 != "" && a2 != "" {
			ordered = append(ordered, b1, a2)
		} else if b1 != "" {
			ordered = append(ordered, b1)
		} else if a2 != "" {
			ordered = append(ordered, a2)
		}
		for q := 2; q < qualifiersPerPool; q++ {
			if v := pick(aList, q); v != "" {
				ordered = append(ordered, v)
			}
			if v := pick(bList, q); v != "" {
				ordered = append(ordered, v)
			}
		}
	}
	out := make([]string, 0, len(ordered))
	for _, n := range ordered {
		if strings.TrimSpace(n) != "" {
			out = append(out, n)
		}
	}
	return out
}

func pick(list []string, i int) string {
	if i < 0 || i >= len(list) {
		return ""
	}
	return list[i]
}

// HybridQualifiers returns bracket-order names from completed pool stage.
func HybridQualifiers(poolMatches []Fixture, rules SportRules) ([]string, error) {
	ok, pending, msg := AssertPoolsComplete(poolMatches)
	if !ok {
		return nil, fmt.Errorf("%s (%d pending)", msg, pending)
	}
	if len(rules.RankingRules) == 0 {
		rules.RankingRules = DefaultRankingRules()
	}
	byPool := BuildStandingsByPool(poolMatches, rules)
	keys := make([]int, 0, len(byPool))
	for k := range byPool {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	tops := map[int][]string{}
	maxQ := 1
	for _, pKey := range keys {
		rows := byPool[pKey]
		qPer := QualifyCountForPool(len(rows), rules)
		if qPer > maxQ {
			maxQ = qPer
		}
		pm := []Fixture{}
		for _, m := range poolMatches {
			if m.PoolNo != nil && *m.PoolNo == pKey {
				pm = append(pm, m)
			}
		}
		tops[pKey] = TopNames(rows, rules.RankingRules, qPer, pm)
	}
	return CrossPoolPairOrder(tops, maxQ), nil
}

func hasPoolStage(fixtures []Fixture) bool {
	for _, f := range fixtures {
		if f.RoundNo == 1 && f.PoolNo != nil && *f.PoolNo > 0 {
			return true
		}
	}
	return false
}
