package tournaments

import (
	"fmt"
	"strings"
)

const (
	MethodBye          = "bye"
	MethodPreliminary  = "preliminary"
	MethodManual       = "manual"
)

// BracketPair is one first-round (or prelim) pairing.
type BracketPair struct {
	Player1 string
	Player2 string
	IsBye   bool
}

// BracketPlan mirrors PHP KnockoutBracket::plan.
type BracketPlan struct {
	Method               string   `json:"method"`
	Sides                int      `json:"sides"`
	BracketSize          int      `json:"bracket_size"`
	ByeCount             int      `json:"bye_count"`
	PrelimMatches        int      `json:"prelim_matches"`
	SitOutCount          int      `json:"sit_out_count"`
	FirstRoundPlayable   int      `json:"first_round_playable"`
	FirstRoundRows       int      `json:"first_round_rows"`
	FirstRoundLabel      string   `json:"first_round_label"`
	TotalPlayableMatches int      `json:"total_playable_matches"`
	Rounds               []string `json:"rounds"`
}

func NextPowerOfTwo(n int) int {
	if n <= 1 {
		return 1
	}
	p := 1
	for p < n {
		p <<= 1
	}
	return p
}

func PrevPowerOfTwo(n int) int {
	if n <= 1 {
		return 1
	}
	p := NextPowerOfTwo(n)
	if p == n {
		return n
	}
	return p / 2
}

func NormalizeBracketMethod(method string) string {
	m := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(method), " ", "_"), "-", "_"))
	switch m {
	case "prelim", "qualifying", "preliminary_round", MethodPreliminary:
		return MethodPreliminary
	case "auto", "automatic", "automatic_bye", "byes", MethodBye, "":
		return MethodBye
	case MethodManual:
		return MethodManual
	default:
		return MethodBye
	}
}

func RoundLabel(matchesInRound, roundNo int) string {
	switch matchesInRound {
	case 1:
		return "Final"
	case 2:
		return "Semi-Final"
	case 4:
		return "Quarter-Final"
	case 8:
		return "Round of 16"
	case 16:
		return "Round of 32"
	case 32:
		return "Round of 64"
	default:
		if roundNo > 0 {
			return fmt.Sprintf("Round %d", roundNo)
		}
		return fmt.Sprintf("Round of %d", matchesInRound*2)
	}
}

func RoundSequence(bracketSize int, withPrelim bool) []string {
	names := []string{}
	if withPrelim {
		names = append(names, "Preliminary Round")
	}
	for slots := bracketSize; slots >= 2; slots /= 2 {
		matches := slots / 2
		names = append(names, RoundLabel(matches, 0))
	}
	return names
}

func ByeMetrics(sideCount int) (bracketSize, byeCount, firstRoundMatches, firstRoundPlayable int, err error) {
	if sideCount < 2 {
		return 0, 0, 0, 0, fmt.Errorf("need_at_least_2_sides")
	}
	bracketSize = NextPowerOfTwo(sideCount)
	byeCount = bracketSize - sideCount
	firstRoundMatches = bracketSize / 2
	firstRoundPlayable = firstRoundMatches - byeCount
	return bracketSize, byeCount, firstRoundMatches, firstRoundPlayable, nil
}

func PrelimMetrics(sideCount int) (target, prelimMatches, sitOut, playIn int, available bool, err error) {
	if sideCount < 2 {
		return 0, 0, 0, 0, false, fmt.Errorf("need_at_least_2_sides")
	}
	target = PrevPowerOfTwo(sideCount)
	prelimMatches = sideCount - target
	playIn = prelimMatches * 2
	sitOut = sideCount - playIn
	available = prelimMatches > 0
	return target, prelimMatches, sitOut, playIn, available, nil
}

func PlanBracket(sideCount int, method string) (BracketPlan, error) {
	method = NormalizeBracketMethod(method)
	if sideCount < 2 {
		return BracketPlan{}, fmt.Errorf("need_at_least_2_sides")
	}
	bracketSize, byeCount, firstRoundMatches, firstRoundPlayable, err := ByeMetrics(sideCount)
	if err != nil {
		return BracketPlan{}, err
	}
	target, prelimMatches, sitOut, _, avail, _ := PrelimMetrics(sideCount)
	if method == MethodPreliminary && !avail {
		method = MethodBye
	}
	if method == MethodPreliminary {
		return BracketPlan{
			Method:               MethodPreliminary,
			Sides:                sideCount,
			BracketSize:          target,
			ByeCount:             0,
			PrelimMatches:        prelimMatches,
			SitOutCount:          sitOut,
			FirstRoundPlayable:   prelimMatches,
			FirstRoundRows:       prelimMatches,
			FirstRoundLabel:      "Preliminary Round",
			TotalPlayableMatches: sideCount - 1,
			Rounds:               RoundSequence(target, true),
		}, nil
	}
	return BracketPlan{
		Method:               MethodBye,
		Sides:                sideCount,
		BracketSize:          bracketSize,
		ByeCount:             byeCount,
		PrelimMatches:        0,
		SitOutCount:          0,
		FirstRoundPlayable:   firstRoundPlayable,
		FirstRoundRows:       firstRoundMatches,
		FirstRoundLabel:      RoundLabel(firstRoundMatches, 1),
		TotalPlayableMatches: sideCount - 1,
		Rounds:               RoundSequence(bracketSize, false),
	}, nil
}

func FirstRoundPairs(sides []string) []BracketPair {
	sides = cleanSides(sides)
	n := len(sides)
	_, byeCount, _, _, err := ByeMetrics(n)
	if err != nil {
		return nil
	}
	byeSides := sides[:byeCount]
	playSides := sides[byeCount:]
	pairs := make([]BracketPair, 0, byeCount+len(playSides)/2+1)
	for _, side := range byeSides {
		pairs = append(pairs, BracketPair{Player1: side, Player2: "BYE", IsBye: true})
	}
	for i := 0; i < len(playSides); i += 2 {
		p1 := playSides[i]
		p2 := "BYE"
		if i+1 < len(playSides) {
			p2 = playSides[i+1]
		}
		pairs = append(pairs, BracketPair{Player1: p1, Player2: p2, IsBye: p2 == "BYE"})
	}
	return pairs
}

func PreliminaryPairs(sides []string) (pairs []BracketPair, sitOuts []string) {
	sides = cleanSides(sides)
	n := len(sides)
	_, _, sitOut, playIn, avail, err := PrelimMetrics(n)
	if err != nil || !avail {
		return FirstRoundPairs(sides), nil
	}
	sitOuts = append([]string{}, sides[:n-playIn]...)
	playInSides := sides[n-playIn:]
	for i := 0; i < len(playInSides); i += 2 {
		p1 := playInSides[i]
		if i+1 >= len(playInSides) {
			pairs = append(pairs, BracketPair{Player1: p1, Player2: "BYE", IsBye: true})
			continue
		}
		pairs = append(pairs, BracketPair{Player1: p1, Player2: playInSides[i+1], IsBye: false})
	}
	_ = sitOut
	return pairs, sitOuts
}

func BuildFirstRound(sides []string, method string) (pairs []BracketPair, sitOuts []string, plan BracketPlan, err error) {
	sides = cleanSides(sides)
	plan, err = PlanBracket(len(sides), method)
	if err != nil {
		return nil, nil, plan, err
	}
	if plan.Method == MethodPreliminary {
		pairs, sitOuts = PreliminaryPairs(sides)
		return pairs, sitOuts, plan, nil
	}
	return FirstRoundPairs(sides), nil, plan, nil
}

func cleanSides(sides []string) []string {
	out := make([]string, 0, len(sides))
	for _, s := range sides {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
