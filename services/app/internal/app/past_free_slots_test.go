package app

import (
	"testing"
	"time"
)

func TestPastFreeSlot(t *testing.T) {
	now, err := time.ParseInLocation("2006-01-02 15:04:05", "2026-10-24 08:15:00", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		date, status, end string
		want              bool
	}{
		{"2026-10-23", "available", "10:00", true},
		{"2026-10-23", "blocked", "10:00", false},
		{"2026-10-23", "booked", "10:00", false},
		{"2026-10-24", "available", "07:00", true},
		{"2026-10-24", "free", "07:00", true},
		{"2026-10-24", "available", "08:30", true},
		{"2026-10-24", "available", "08:31", false},
		{"2026-10-24", "blocked", "08:30", false},
		{"2026-10-24", "available", "12:00", false},
		{"2026-10-24", "blocked", "07:00", false},
		{"2026-10-25", "available", "08:00", false},
	}
	for _, c := range cases {
		if got := pastFreeSlot(c.date, c.status, c.end, now); got != c.want {
			t.Errorf("%s %s %s = %v want %v", c.date, c.status, c.end, got, c.want)
		}
	}
}
