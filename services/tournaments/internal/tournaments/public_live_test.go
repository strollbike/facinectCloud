package tournaments

import "testing"

func TestIsPublicLiveStatus(t *testing.T) {
	if !isPublicLiveStatus("live") || !isPublicLiveStatus("upcoming") {
		t.Fatal("expected live/upcoming public")
	}
	if isPublicLiveStatus("draft") || isPublicLiveStatus("completed") {
		t.Fatal("draft/completed not public")
	}
}

func TestGenerateAccessCodeSixDigits(t *testing.T) {
	for i := 0; i < 20; i++ {
		code, err := generateAccessCode()
		if err != nil {
			t.Fatal(err)
		}
		if !regexpSixDigit(code) {
			t.Fatalf("bad code %q", code)
		}
	}
}

func TestFormatRefereeIdentity(t *testing.T) {
	if got := formatRefereeIdentity(12, ""); got != "REF0012" {
		t.Fatalf("got %s", got)
	}
	if got := formatRefereeIdentity(12, "refbh99"); got != "REFBH99" {
		t.Fatalf("got %s", got)
	}
}

func TestRegexpSixDigit(t *testing.T) {
	if !regexpSixDigit("012345") || regexpSixDigit("12345") || regexpSixDigit("abcdef") {
		t.Fatal("six digit check failed")
	}
}
