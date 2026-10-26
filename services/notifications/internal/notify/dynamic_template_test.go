package notify

import "testing"

func TestResolveFacilityDynamicTemplate(t *testing.T) {
	for _, name := range []string{"news_v3", "student_broadcast", "member_broadcast", "news"} {
		got, lang := resolveWhatsAppTemplate(name, "en")
		if got != facilityDynamicTemplate || lang != "en_US" {
			t.Fatalf("%s -> %s %s", name, got, lang)
		}
	}
	got, lang := resolveWhatsAppTemplate(facilityDynamicTemplate, "en_US")
	if got != facilityDynamicTemplate || lang != "en_US" {
		t.Fatalf("passthrough -> %s %s", got, lang)
	}
}

func TestDataForRecipientSetsName(t *testing.T) {
	shared := map[string]interface{}{
		"body_params": []interface{}{"Academy", "Student", "Class is at 6"},
	}
	out := DataForRecipient(shared, "Raja")
	params := out["body_params"].([]interface{})
	if params[1] != "Raja" {
		t.Fatalf("name = %v", params[1])
	}
	orig := shared["body_params"].([]interface{})
	if orig[1] != "Student" {
		t.Fatalf("shared payload was changed: %v", orig[1])
	}
}
