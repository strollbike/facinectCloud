package tournaments

import "testing"

func TestScheduleSlotRoundRobin(t *testing.T) {
	courts := []string{"Court A", "Court B"}
	c0, t0 := ScheduleSlot(courts, "09:00", 20, 0, false)
	c1, t1 := ScheduleSlot(courts, "09:00", 20, 1, false)
	c2, t2 := ScheduleSlot(courts, "09:00", 20, 2, false)
	if c0 != "Court A" || t0 != "09:00" {
		t.Fatalf("idx0 got %s %s", c0, t0)
	}
	if c1 != "Court B" || t1 != "09:00" {
		t.Fatalf("idx1 got %s %s", c1, t1)
	}
	if c2 != "Court A" || t2 != "09:20" {
		t.Fatalf("idx2 got %s %s", c2, t2)
	}
	cBye, tBye := ScheduleSlot(courts, "09:00", 20, 0, true)
	if cBye != "" || tBye != "" {
		t.Fatalf("bye should be empty")
	}
}

func TestBuildCourtsBoardNowAndUpNext(t *testing.T) {
	live := Fixture{
		ID: 1, Team1: "A", Team2: "B", Status: "live", Court: "Court 1",
		MatchTime: "09:00", RoundNo: 1, MatchNo: 1,
	}
	pending := Fixture{
		ID: 2, Team1: "C", Team2: "D", Status: "scheduled", Court: "Court 1",
		MatchTime: "09:20", RoundNo: 1, MatchNo: 2,
	}
	bye := Fixture{
		ID: 3, Team1: "E", Team2: "BYE", Status: "completed", Court: "Court 1", IsBye: true,
	}
	board := BuildCourtsBoard(99, []Fixture{live, pending, bye}, []string{"Court 1", "Court 2"})
	if board.TourID != 99 {
		t.Fatalf("tourId")
	}
	if len(board.Courts) < 2 {
		t.Fatalf("expected seeded courts, got %d", len(board.Courts))
	}
	lane := board.Courts[0]
	if lane.Court != "Court 1" || lane.Now == nil || lane.Now.Player1 != "A" {
		t.Fatalf("now playing: %+v", lane.Now)
	}
	if len(lane.UpNext) != 1 || lane.UpNext[0].Player1 != "C" {
		t.Fatalf("up_next: %+v", lane.UpNext)
	}
}

func TestBuildCourtsBoardConflicts(t *testing.T) {
	m1 := Fixture{ID: 10, Team1: "Sam", Team2: "X", Status: "scheduled", Court: "C1", MatchTime: "10:00"}
	m2 := Fixture{ID: 11, Team1: "Y", Team2: "Sam", Status: "scheduled", Court: "C2", MatchTime: "10:00"}
	board := BuildCourtsBoard(1, []Fixture{m1, m2}, nil)
	if len(board.Conflicts) == 0 {
		t.Fatal("expected double-book conflict")
	}
}

func TestResolveCheckoutAmount(t *testing.T) {
	s := &Service{}
	tour := &Tournament{AllowFullPayment: true, AllowAdvancePayment: true, AllowSpotPayment: true, AdvanceAmount: 200}
	amt, err := s.resolveCheckoutAmount(tour, 500, "full")
	if err != nil || amt != 500 {
		t.Fatalf("full: %v %v", amt, err)
	}
	amt, err = s.resolveCheckoutAmount(tour, 500, "advance")
	if err != nil || amt != 200 {
		t.Fatalf("advance: %v %v", amt, err)
	}
	amt, err = s.resolveCheckoutAmount(tour, 500, "spot")
	if err != nil || amt != 0 {
		t.Fatalf("spot: %v %v", amt, err)
	}
	tour.AllowFullPayment = false
	if _, err := s.resolveCheckoutAmount(tour, 500, "full"); err == nil {
		t.Fatal("expected full disabled")
	}
}
