package tournaments

import "testing"

func TestIsPublicRegistrationStatus(t *testing.T) {
	cases := map[string]bool{
		"upcoming": true,
		"live":     true,
		"LIVE":     true,
		"draft":    false,
		"ended":    false,
		"":         false,
	}
	for in, want := range cases {
		if got := isPublicRegistrationStatus(in); got != want {
			t.Fatalf("%q: got %v want %v", in, got, want)
		}
	}
}
