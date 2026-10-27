package tournaments

import "testing"

func TestCrossPoolPairOrderTwoPools(t *testing.T) {
	tops := map[int][]string{
		1: {"A1", "A2"},
		2: {"B1", "B2"},
	}
	got := CrossPoolPairOrder(tops, 2)
	want := []string{"A1", "B2", "B1", "A2"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestQualifyCountEliminated(t *testing.T) {
	elim := 2
	q := QualifyCountForPool(4, SportRules{EliminatedPerPool: &elim})
	if q != 2 {
		t.Fatalf("q=%d", q)
	}
	q = QualifyCountForPool(4, SportRules{QualifiersPerPool: 2})
	if q != 2 {
		t.Fatalf("default q=%d", q)
	}
}

func TestHybridQualifiersFromStandings(t *testing.T) {
	pool1, pool2 := 1, 2
	completed := func(t1, t2, w string, s1, s2, pool int) Fixture {
		a, b := s1, s2
		p := pool
		return Fixture{
			RoundNo: 1, PoolNo: &p, Team1: t1, Team2: t2, Winner: w,
			Score1: &a, Score2: &b, Status: "completed",
		}
	}
	// Pool 1: A beats B,C,D → A1; B beats C,D → A2
	// Pool 2: E beats F,G,H → B1; F beats G,H → B2
	matches := []Fixture{
		completed("A", "B", "A", 2, 0, pool1),
		completed("A", "C", "A", 2, 0, pool1),
		completed("A", "D", "A", 2, 0, pool1),
		completed("B", "C", "B", 2, 0, pool1),
		completed("B", "D", "B", 2, 0, pool1),
		completed("C", "D", "C", 2, 0, pool1),
		completed("E", "F", "E", 2, 0, pool2),
		completed("E", "G", "E", 2, 0, pool2),
		completed("E", "H", "E", 2, 0, pool2),
		completed("F", "G", "F", 2, 0, pool2),
		completed("F", "H", "F", 2, 0, pool2),
		completed("G", "H", "G", 2, 0, pool2),
	}
	got, err := HybridQualifiers(matches, DefaultSportRules())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"A", "F", "E", "B"}
	if len(got) != 4 {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestAssertPoolsIncomplete(t *testing.T) {
	p := 1
	ok, pending, _ := AssertPoolsComplete([]Fixture{
		{RoundNo: 1, PoolNo: &p, Team1: "A", Team2: "B", Status: "scheduled"},
	})
	if ok || pending != 1 {
		t.Fatalf("ok=%v pending=%d", ok, pending)
	}
}

func TestRankMatchWinsThenPD(t *testing.T) {
	rows := []*StandingRow{
		{SideName: "X", Wins: 1, PointDifference: 10, PointsFor: 20},
		{SideName: "Y", Wins: 2, PointDifference: 1, PointsFor: 5},
		{SideName: "Z", Wins: 1, PointDifference: 12, PointsFor: 15},
	}
	ranked := RankStandings(rows, DefaultRankingRules(), nil)
	if ranked[0].SideName != "Y" {
		t.Fatalf("top=%s", ranked[0].SideName)
	}
	if ranked[1].SideName != "Z" { // same wins, higher PD
		t.Fatalf("second=%s", ranked[1].SideName)
	}
}
