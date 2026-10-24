package booking

import (
	"testing"
	"time"
)

func TestOmitPastFreeSlots(t *testing.T) {
	slots := []Slot{
		{ID: 1, SlotDate: "2026-10-23", EndTime: "10:00:00", Status: "available"},
		{ID: 2, SlotDate: "2026-10-23", EndTime: "10:00:00", Status: "blocked"},
		{ID: 3, SlotDate: "2026-10-23", EndTime: "10:00:00", Status: "booked"},
		{ID: 4, SlotDate: "2026-10-24", EndTime: "07:00:00", Status: "available"},
		{ID: 5, SlotDate: "2026-10-24", EndTime: "08:30:00", Status: "available"},
		{ID: 6, SlotDate: "2026-10-24", EndTime: "08:31:00", Status: "available"},
		{ID: 7, SlotDate: "2026-10-24", EndTime: "12:00:00", Status: "available"},
		{ID: 8, SlotDate: "2026-10-25", EndTime: "08:00:00", Status: "available"},
	}
	now, err := time.ParseInLocation("2006-01-02 15:04:05", "2026-10-24 08:15:00", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	got := omitPastFreeSlots(slots, now)
	want := []int64{2, 3, 6, 7, 8}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("slot %d id=%d want %d", i, got[i].ID, id)
		}
	}
}
