package platform

import "testing"

func TestValidMapsURL(t *testing.T) {
	ok := []string{
		"https://maps.app.goo.gl/PqbPFVx5fnXfAbHa6",
		"https://www.google.com/maps/place/Academy",
		"https://maps.google.com/?q=chennai",
		"https://goo.gl/maps/abc",
	}
	for _, u := range ok {
		if !validMapsURL(u) {
			t.Fatalf("expected valid: %s", u)
		}
	}
	bad := []string{
		"",
		"chennai",
		"https://example.com/maps",
		"maps.app.goo.gl/abc",
	}
	for _, u := range bad {
		if validMapsURL(u) {
			t.Fatalf("expected invalid: %s", u)
		}
	}
}

func TestNormalizeMapsURLIgnoresQuery(t *testing.T) {
	a := normalizeMapsURL("https://maps.app.goo.gl/PqbPFVx5fnXfAbHa6?g_st=ac")
	b := normalizeMapsURL("http://maps.app.goo.gl/PqbPFVx5fnXfAbHa6/")
	if a != b {
		t.Fatalf("same place should match: %q vs %q", a, b)
	}
}
