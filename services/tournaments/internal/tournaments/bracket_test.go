package tournaments

import "testing"

func TestKnockoutMatrixPHP(t *testing.T) {
	// Port of deploy/test_knockout_matrix.php + requested Stage C sizes.
	ns := []int{3, 4, 5, 7, 8, 9, 13, 15, 16, 17, 19, 23, 31, 32, 33, 43}
	for _, n := range ns {
		bracket, byes, r1, playable, err := ByeMetrics(n)
		if err != nil {
			t.Fatalf("n=%d bye metrics: %v", n, err)
		}
		if bracket != NextPowerOfTwo(n) {
			t.Fatalf("n=%d bracket=%d want %d", n, bracket, NextPowerOfTwo(n))
		}
		if byes != bracket-n {
			t.Fatalf("n=%d byes=%d", n, byes)
		}
		if playable != r1-byes {
			t.Fatalf("n=%d playable=%d", n, playable)
		}
		if playable+byes != n-0 && (playable != n-1-0) {
			// total playable matches across tournament = n-1; first round playable = r1-byes
		}
		sides := make([]string, n)
		for i := 0; i < n; i++ {
			sides[i] = string(rune('A'+i%26)) + string(rune('0'+i/26))
		}
		// unique names
		for i := range sides {
			sides[i] = "P" + itoa(i+1)
		}
		pairs := FirstRoundPairs(sides)
		byeRows := 0
		seen := map[string]int{}
		for _, p := range pairs {
			if p.IsBye {
				byeRows++
			}
			seen[p.Player1]++
			if p.Player2 != "BYE" {
				seen[p.Player2]++
			}
		}
		if byeRows != byes {
			t.Fatalf("n=%d bye rows=%d want %d", n, byeRows, byes)
		}
		if len(seen) != n {
			t.Fatalf("n=%d seen sides=%d", n, len(seen))
		}

		target, prelim, sitOut, playIn, avail, _ := PrelimMetrics(n)
		plan, err := PlanBracket(n, MethodPreliminary)
		if err != nil {
			t.Fatalf("n=%d plan: %v", n, err)
		}
		if !avail {
			if plan.Method != MethodBye {
				t.Fatalf("n=%d expected bye fallback, got %s", n, plan.Method)
			}
			if plan.PrelimMatches != 0 {
				t.Fatalf("n=%d prelim should be 0", n)
			}
			continue
		}
		if plan.Method != MethodPreliminary {
			t.Fatalf("n=%d method=%s", n, plan.Method)
		}
		if plan.PrelimMatches != prelim || plan.SitOutCount != sitOut {
			t.Fatalf("n=%d prelim=%d sit=%d", n, plan.PrelimMatches, plan.SitOutCount)
		}
		pp, sits := PreliminaryPairs(sides)
		if len(pp) != prelim {
			t.Fatalf("n=%d prelim pairs=%d want %d", n, len(pp), prelim)
		}
		if len(sits) != sitOut {
			t.Fatalf("n=%d sitOuts=%d want %d (playIn=%d target=%d)", n, len(sits), sitOut, playIn, target)
		}
		if len(sits)+prelim != target {
			// sitOuts + winners(=prelim) = target
			if len(sits)+len(pp) != target {
				t.Fatalf("n=%d sit+prelimPairs=%d want target %d", n, len(sits)+len(pp), target)
			}
		}
	}
}

func TestN17SpotCheck(t *testing.T) {
	planBye, _ := PlanBracket(17, MethodBye)
	if planBye.BracketSize != 32 || planBye.ByeCount != 15 || planBye.FirstRoundPlayable != 1 {
		t.Fatalf("bye17: %+v", planBye)
	}
	if planBye.FirstRoundLabel != "Round of 32" {
		t.Fatalf("label=%s", planBye.FirstRoundLabel)
	}
	planP, _ := PlanBracket(17, MethodPreliminary)
	if planP.PrelimMatches != 1 || planP.SitOutCount != 15 || planP.BracketSize != 16 {
		t.Fatalf("prelim17: %+v", planP)
	}
}

func TestSuggestPools(t *testing.T) {
	if got := SuggestPools(8, 4); len(got) != 2 || got[0] != 4 || got[1] != 4 {
		t.Fatalf("8/4: %v", got)
	}
	if got := SuggestPools(9, 4); len(got) != 2 || got[0]+got[1] != 9 {
		t.Fatalf("9/4: %v", got)
	}
}

func TestStandardSeedSlots8(t *testing.T) {
	got := StandardSeedSlots(8)
	want := []int{0, 4, 6, 2, 3, 7, 5, 1}
	if len(got) != len(want) {
		t.Fatalf("len %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
