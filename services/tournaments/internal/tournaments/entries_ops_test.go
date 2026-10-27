package tournaments

import "testing"

func TestNormalizeSpotGender(t *testing.T) {
	cases := map[string]string{
		"male":   "Male",
		"M":      "Male",
		"female": "Female",
		"Girl":   "Female",
		"Other":  "Other",
		"":       "",
	}
	for in, want := range cases {
		if got := normalizeSpotGender(in); got != want {
			t.Fatalf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestSpotPlayerFromMap(t *testing.T) {
	p := SpotPlayerFromMap(map[string]interface{}{
		"first_name":       "Ada",
		"last_name":        "Lovelace",
		"whatsapp_number":  "9876543210",
		"gender":           "female",
	})
	if p.FirstName != "Ada" || p.LastName != "Lovelace" {
		t.Fatalf("name: %+v", p)
	}
	if p.WhatsAppNumber != "9876543210" {
		t.Fatalf("phone: %+v", p)
	}
	if normalizeSpotGender(p.Gender) != "Female" {
		t.Fatalf("gender: %q", p.Gender)
	}
}
