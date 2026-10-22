package theme

import (
	"regexp"
	"testing"
)

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}([0-9a-fA-F]{2})?$`)

func TestEveryIDLooksUpItself(t *testing.T) {
	ids := IDs()
	if len(ids) == 0 {
		t.Fatal("no themes registered")
	}
	for _, id := range ids {
		th, ok := Lookup(id)
		if !ok {
			t.Errorf("Lookup(%q) failed for a listed id", id)
			continue
		}
		if th.ID != id {
			t.Errorf("Lookup(%q).ID = %q", id, th.ID)
		}
	}
}

func TestEveryColorIsHex(t *testing.T) {
	for _, id := range IDs() {
		th, _ := Lookup(id)
		colors := map[string]string{
			"Title":      th.Title,
			"Text":       th.Text,
			"Background": th.Background,
			"Stroke":     th.Stroke,
			"Muted":      th.Muted,
			"Accent":     th.Accent,
		}
		for field, c := range colors {
			if !hexColor.MatchString(c) {
				t.Errorf("%s.%s = %q, want #rrggbb or #rrggbbaa", id, field, c)
			}
		}
		if th.StrokeOpacity < 0 || th.StrokeOpacity > 1 {
			t.Errorf("%s.StrokeOpacity = %v, want 0..1", id, th.StrokeOpacity)
		}
	}
}
