package tournaments

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// GenerateFixturesInput matches StrollX generateFixtures body.
type GenerateFixturesInput struct {
	Category         string `json:"category"`
	Gender           string `json:"gender"`
	MatchType        string `json:"matchType"`
	PoolDistribution string `json:"poolDistribution"`
	MatchesPerPool   int    `json:"matchesPerPool"`
	PairingMode      string `json:"pairingMode"`
	Force            bool   `json:"force"`
}

// GenerateFixturesEx builds pool/RR fixtures (PHP generate_fixtures).
func (s *Service) GenerateFixturesEx(ctx context.Context, tournamentID int64, in GenerateFixturesInput) ([]Fixture, error) {
	cat := strings.TrimSpace(in.Category)
	gen := strings.TrimSpace(in.Gender)
	existing, err := s.listFixturesFiltered(ctx, tournamentID, cat, gen)
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		if !in.Force {
			if hasRealResults(existing) {
				return nil, fmt.Errorf("bracket_locked")
			}
			return existing, nil
		}
		if err := s.ResetFixtures(ctx, tournamentID, ResetFixturesInput{
			Category: cat, Gender: gen, Force: true,
		}); err != nil {
			return nil, err
		}
	}

	entries, err := s.listEntriesFiltered(ctx, tournamentID, cat, gen)
	if err != nil {
		return nil, err
	}
	if len(entries) < 2 {
		return nil, fmt.Errorf("need_at_least_2_sides")
	}

	// Group by category|gender when filters empty.
	groups := map[string][]Entry{}
	for _, e := range entries {
		key := e.Category + "|" + e.Gender
		if cat != "" || gen != "" {
			key = cat + "|" + gen
		}
		groups[key] = append(groups[key], e)
	}

	capPerPool := in.MatchesPerPool
	if capPerPool <= 0 {
		capPerPool = 6
	}
	mode := in.PairingMode
	if mode == "" {
		mode = "academy_separated"
	}

	matchNo := 0
	for key, list := range groups {
		parts := strings.SplitN(key, "|", 2)
		gCat, gGen := parts[0], ""
		if len(parts) > 1 {
			gGen = parts[1]
		}
		names := make([]string, 0, len(list))
		sides := make([]PairingSide, 0, len(list))
		for i, e := range list {
			names = append(names, e.PlayerName)
			sides = append(sides, PairingSide{Name: e.PlayerName, Seed: i + 1})
		}
		ordered, _ := OrderSides(sides, mode)
		ordNames := make([]string, 0, len(ordered))
		for _, o := range ordered {
			ordNames = append(ordNames, o.Name)
		}

		dist := []int{}
		if strings.TrimSpace(in.PoolDistribution) != "" {
			dist, err = ParsePoolDistribution(in.PoolDistribution, len(ordNames))
			if err != nil {
				dist = SuggestPools(len(ordNames), 4)
			}
		} else {
			dist = SuggestPools(len(ordNames), 4)
		}

		offset := 0
		for poolIdx, size := range dist {
			if offset >= len(ordNames) {
				break
			}
			end := offset + size
			if end > len(ordNames) {
				end = len(ordNames)
			}
			poolPlayers := ordNames[offset:end]
			offset = end
			poolNo := poolIdx + 1
			pairs := RRPairsWithinPool(poolPlayers, capPerPool)
			for _, pair := range pairs {
				matchNo++
				if err := s.insertFixtureRow(ctx, insertFixtureArgs{
					TournamentID: tournamentID,
					Category:     gCat,
					Gender:       gGen,
					RoundNo:      1,
					MatchNo:      matchNo,
					Team1:        pair[0],
					Team2:        pair[1],
					Status:       "scheduled",
					PoolNo:       &poolNo,
					RoundName:    "Pool",
					Locked:       true,
				}); err != nil {
					return nil, err
				}
			}
		}
	}
	return s.listFixturesFiltered(ctx, tournamentID, cat, gen)
}

type insertFixtureArgs struct {
	TournamentID int64
	Category     string
	Gender       string
	RoundNo      int
	MatchNo      int
	Team1        string
	Team2        string
	Status       string
	Winner       string
	Score1       *int
	Score2       *int
	PoolNo       *int
	RoundName    string
	IsBye        bool
	NextMatchID  *int64
	NextSlot     string
	Source1      *int64
	Source2      *int64
	Locked       bool
}

func (s *Service) insertFixtureRow(ctx context.Context, a insertFixtureArgs) error {
	var s1, s2, pool, next, src1, src2 interface{}
	if a.Score1 != nil {
		s1 = *a.Score1
	}
	if a.Score2 != nil {
		s2 = *a.Score2
	}
	if a.PoolNo != nil {
		pool = *a.PoolNo
	}
	if a.NextMatchID != nil {
		next = *a.NextMatchID
	}
	if a.Source1 != nil {
		src1 = *a.Source1
	}
	if a.Source2 != nil {
		src2 = *a.Source2
	}
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO tournament_fixtures
  (tournament_id, category, gender, round_no, match_no, team1, team2,
   score1, score2, winner, status, pool_no, round_name, is_bye,
   next_match_id, next_slot, source_match1_id, source_match2_id, locked)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,''),$11,$12,NULLIF($13,''),$14,$15,NULLIF($16,''),$17,$18,$19)`,
		a.TournamentID, a.Category, a.Gender, a.RoundNo, a.MatchNo, a.Team1, a.Team2,
		s1, s2, a.Winner, a.Status, pool, a.RoundName, a.IsBye,
		next, a.NextSlot, src1, src2, a.Locked,
	)
	return err
}

func hasRealResults(list []Fixture) bool {
	for _, f := range list {
		if f.IsBye {
			continue
		}
		if isCompletedFixtureStatus(f.Status) &&
			!strings.EqualFold(f.Team1, "BYE") && !strings.EqualFold(f.Team2, "BYE") {
			return true
		}
	}
	return false
}

// GenerateKnockout: no fixtures → initial KO; else advance next round (PHP generate_knockout).
func (s *Service) GenerateKnockout(ctx context.Context, tournamentID int64, in KnockoutInput) ([]Fixture, error) {
	cat := strings.TrimSpace(in.Category)
	gen := strings.TrimSpace(in.Gender)
	existing, err := s.listFixturesFiltered(ctx, tournamentID, cat, gen)
	if err != nil {
		return nil, err
	}
	if len(existing) == 0 {
		return s.generateInitialKnockout(ctx, tournamentID, in)
	}
	if in.Force {
		if err := s.ResetFixtures(ctx, tournamentID, ResetFixturesInput{
			Category: cat, Gender: gen, Force: true,
		}); err != nil {
			return nil, err
		}
		return s.generateInitialKnockout(ctx, tournamentID, in)
	}
	return s.GenerateNextRound(ctx, tournamentID, in)
}

func (s *Service) generateInitialKnockout(ctx context.Context, tournamentID int64, in KnockoutInput) ([]Fixture, error) {
	cat := strings.TrimSpace(in.Category)
	gen := strings.TrimSpace(in.Gender)
	method := in.BracketMethod
	if method == "" {
		method = MethodBye
	}
	mode := in.PairingMode
	if mode == "" {
		mode = "academy_separated"
	}

	entries, err := s.listEntriesFiltered(ctx, tournamentID, cat, gen)
	if err != nil {
		return nil, err
	}
	if len(entries) < 2 {
		return nil, fmt.Errorf("need_at_least_2_sides")
	}
	sides := make([]PairingSide, 0, len(entries))
	for i, e := range entries {
		sides = append(sides, PairingSide{Name: e.PlayerName, Seed: i + 1})
	}
	ordered, _ := OrderSides(sides, mode)
	names := make([]string, 0, len(ordered))
	for _, o := range ordered {
		names = append(names, o.Name)
	}

	pairs, sitOuts, plan, err := BuildFirstRound(names, method)
	if err != nil {
		return nil, err
	}
	_ = sitOuts // used when advancing from prelim via GenerateNextRound

	matchNo := 0
	for _, p := range pairs {
		matchNo++
		status := "scheduled"
		winner := ""
		var s1, s2 *int
		if p.IsBye {
			status = "completed"
			winner = p.Player1
			if strings.EqualFold(p.Player1, "BYE") {
				winner = p.Player2
			}
			one, zero := 1, 0
			if winner == p.Player1 {
				s1, s2 = &one, &zero
			} else {
				s1, s2 = &zero, &one
			}
		}
		if err := s.insertFixtureRow(ctx, insertFixtureArgs{
			TournamentID: tournamentID,
			Category:     cat,
			Gender:       gen,
			RoundNo:      1,
			MatchNo:      matchNo,
			Team1:        p.Player1,
			Team2:        p.Player2,
			Status:       status,
			Winner:       winner,
			Score1:       s1,
			Score2:       s2,
			RoundName:    plan.FirstRoundLabel,
			IsBye:        p.IsBye,
			Locked:       true,
		}); err != nil {
			return nil, err
		}
	}

	// Persist sit-outs as completed bye placeholders? PHP keeps them only in memory until next round.
	// Store on round 0 metadata via empty? Skip — GenerateNextRound rehydrates from entries+winners.
	list, err := s.listFixturesFiltered(ctx, tournamentID, cat, gen)
	if err != nil {
		return nil, err
	}
	// Auto-advance bye winners into nothing yet (no next match). Done.
	_ = plan
	return list, nil
}

// GenerateNextRound advances KO after current round completes (PHP generate_next_round).
func (s *Service) GenerateNextRound(ctx context.Context, tournamentID int64, in KnockoutInput) ([]Fixture, error) {
	cat := strings.TrimSpace(in.Category)
	gen := strings.TrimSpace(in.Gender)
	existing, err := s.listFixturesFiltered(ctx, tournamentID, cat, gen)
	if err != nil {
		return nil, err
	}
	if len(existing) == 0 {
		return s.generateInitialKnockout(ctx, tournamentID, in)
	}

	maxRound := 0
	for _, f := range existing {
		if f.RoundNo > maxRound {
			maxRound = f.RoundNo
		}
	}
	curRound := []Fixture{}
	for _, f := range existing {
		if f.RoundNo == maxRound {
			curRound = append(curRound, f)
		}
	}
	if len(curRound) == 0 {
		return nil, fmt.Errorf("no_current_round")
	}

	// Stage D: pool stage (R1 with pool_no) → hybrid qualifiers, not match winners.
	if maxRound == 1 && hasPoolStage(curRound) {
		rules := SportRulesFromKnockout(in)
		qualified, err := HybridQualifiers(curRound, rules)
		if err != nil {
			return nil, err
		}
		return s.insertKnockoutRound(ctx, tournamentID, cat, gen, maxRound+1, qualified, map[string]int64{})
	}

	for _, f := range curRound {
		if !isCompletedFixtureStatus(f.Status) || strings.TrimSpace(f.Winner) == "" {
			return nil, fmt.Errorf("round_incomplete")
		}
	}

	isPrelim := false
	for _, f := range curRound {
		if strings.Contains(strings.ToLower(f.RoundName), "prelim") {
			isPrelim = true
			break
		}
	}

	qualified := []string{}
	if isPrelim {
		winners := []string{}
		for _, f := range curRound {
			w := strings.TrimSpace(f.Winner)
			if w != "" && !strings.EqualFold(w, "BYE") {
				winners = append(winners, w)
			}
		}
		entries, err := s.listEntriesFiltered(ctx, tournamentID, cat, gen)
		if err != nil {
			return nil, err
		}
		played := map[string]bool{}
		for _, f := range curRound {
			played[f.Team1] = true
			played[f.Team2] = true
		}
		sitOuts := []string{}
		for _, e := range entries {
			if !played[e.PlayerName] {
				sitOuts = append(sitOuts, e.PlayerName)
			}
		}
		qualified = append(sitOuts, winners...)
	} else {
		for _, f := range curRound {
			w := strings.TrimSpace(f.Winner)
			if w != "" && !strings.EqualFold(w, "BYE") {
				qualified = append(qualified, w)
			}
		}
	}
	if len(qualified) < 2 {
		return nil, fmt.Errorf("need_at_least_2_sides")
	}
	winnerSource := map[string]int64{}
	for _, f := range curRound {
		w := strings.TrimSpace(f.Winner)
		if w != "" {
			winnerSource[w] = f.ID
		}
	}
	return s.insertKnockoutRound(ctx, tournamentID, cat, gen, maxRound+1, qualified, winnerSource)
}

func (s *Service) insertKnockoutRound(ctx context.Context, tournamentID int64, cat, gen string, nextRoundNo int, qualified []string, winnerSource map[string]int64) ([]Fixture, error) {
	if len(qualified) < 2 {
		return nil, fmt.Errorf("need_at_least_2_sides")
	}
	pairs := FirstRoundPairs(qualified)
	label := RoundLabel(len(pairs), nextRoundNo)
	if len(pairs) == 1 {
		label = "Final"
	} else if len(pairs) == 2 {
		label = "Semi-Final"
	}
	poolZero := 0
	matchNo := 0
	for _, p := range pairs {
		matchNo++
		status := "scheduled"
		winner := ""
		var s1, s2 *int
		var src1, src2 *int64
		if id, ok := winnerSource[p.Player1]; ok {
			v := id
			src1 = &v
		}
		if id, ok := winnerSource[p.Player2]; ok {
			v := id
			src2 = &v
		}
		if p.IsBye {
			status = "completed"
			winner = p.Player1
			if strings.EqualFold(p.Player1, "BYE") {
				winner = p.Player2
			}
			one, zero := 1, 0
			if winner == p.Player1 {
				s1, s2 = &one, &zero
			} else {
				s1, s2 = &zero, &one
			}
		}
		var id int64
		err := s.DB.QueryRowContext(ctx, `
INSERT INTO tournament_fixtures
  (tournament_id, category, gender, round_no, match_no, team1, team2,
   score1, score2, winner, status, pool_no, round_name, is_bye,
   source_match1_id, source_match2_id, locked)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,''),$11,$12,$13,$14,$15,$16,TRUE)
RETURNING id`,
			tournamentID, cat, gen, nextRoundNo, matchNo, p.Player1, p.Player2,
			ptrOrNil(s1), ptrOrNil(s2), winner, status, poolZero, label, p.IsBye,
			ptrI64OrNil(src1), ptrI64OrNil(src2),
		).Scan(&id)
		if err != nil {
			return nil, err
		}
		if src1 != nil {
			_ = s.linkForward(ctx, *src1, id, "1")
		}
		if src2 != nil {
			_ = s.linkForward(ctx, *src2, id, "2")
		}
		if p.IsBye && winner != "" {
			fx, _ := s.getFixture(ctx, id)
			if fx != nil {
				_ = s.advanceWinner(ctx, fx)
			}
		}
	}
	return s.listFixturesFiltered(ctx, tournamentID, cat, gen)
}

// BuildStandings returns ranked pool tables (PHP TournamentEngine::buildStandings).
func (s *Service) BuildStandings(ctx context.Context, tournamentID int64, category, gender string) (map[string][]StandingRow, error) {
	list, err := s.listFixturesFiltered(ctx, tournamentID, category, gender)
	if err != nil {
		return nil, err
	}
	rules := DefaultSportRules()
	byPool := BuildStandingsByPool(list, rules)
	out := map[string][]StandingRow{}
	keys := make([]int, 0, len(byPool))
	for k := range byPool {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	for _, k := range keys {
		rowsMap := byPool[k]
		rows := make([]*StandingRow, 0, len(rowsMap))
		for _, r := range rowsMap {
			rows = append(rows, r)
		}
		pm := []Fixture{}
		for _, m := range list {
			if m.PoolNo != nil && *m.PoolNo == k {
				pm = append(pm, m)
			}
		}
		ranked := RankStandings(rows, rules.RankingRules, pm)
		arr := make([]StandingRow, 0, len(ranked))
		for _, r := range ranked {
			arr = append(arr, *r)
		}
		out[fmt.Sprintf("%d", k)] = arr
	}
	return out, nil
}

func ptrOrNil(p *int) interface{} {
	if p == nil {
		return nil
	}
	return *p
}

func ptrI64OrNil(p *int64) interface{} {
	if p == nil {
		return nil
	}
	return *p
}

func (s *Service) linkForward(ctx context.Context, fromID, nextID int64, slot string) error {
	_, err := s.DB.ExecContext(ctx, `
UPDATE tournament_fixtures SET next_match_id=$1, next_slot=$2 WHERE id=$3`,
		nextID, slot, fromID)
	return err
}

func (s *Service) advanceWinner(ctx context.Context, fx *Fixture) error {
	if fx == nil {
		return nil
	}
	winner := strings.TrimSpace(fx.Winner)
	if winner == "" || strings.EqualFold(winner, "BYE") {
		return nil
	}
	// Path A: explicit next_match_id
	if fx.NextMatchID != nil && *fx.NextMatchID > 0 {
		return s.fillNextSlot(ctx, *fx.NextMatchID, fx.NextSlot, winner)
	}
	// Path B: find match that lists this as source
	var nextID int64
	var slot string
	err := s.DB.QueryRowContext(ctx, `
SELECT id,
  CASE WHEN source_match1_id=$1 THEN '1' ELSE '2' END
FROM tournament_fixtures
WHERE tournament_id=$2 AND (source_match1_id=$1 OR source_match2_id=$1)
ORDER BY id LIMIT 1`, fx.ID, fx.TournamentID).Scan(&nextID, &slot)
	if err != nil {
		return nil // no next match yet
	}
	_ = s.linkForward(ctx, fx.ID, nextID, slot)
	return s.fillNextSlot(ctx, nextID, slot, winner)
}

func (s *Service) fillNextSlot(ctx context.Context, nextID int64, slot, winner string) error {
	next, err := s.getFixture(ctx, nextID)
	if err != nil {
		return err
	}
	if isCompletedFixtureStatus(next.Status) && !next.IsBye {
		return nil
	}
	slot = strings.TrimSpace(strings.ToLower(slot))
	if slot == "2" || slot == "b" || slot == "player2" {
		if strings.TrimSpace(next.Team2) != "" && !strings.EqualFold(next.Team2, "TBD") && next.Team2 != winner {
			// already filled with different player — skip overwrite of completed
			if isCompletedFixtureStatus(next.Status) {
				return nil
			}
		}
		_, err = s.DB.ExecContext(ctx, `
UPDATE tournament_fixtures SET team2=$1, updated_at=NOW() WHERE id=$2`, winner, nextID)
		return err
	}
	_, err = s.DB.ExecContext(ctx, `
UPDATE tournament_fixtures SET team1=$1, updated_at=NOW() WHERE id=$2`, winner, nextID)
	return err
}
