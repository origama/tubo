package domain

import "testing"

func TestParseRef(t *testing.T) {
	ref, err := ParseRef("pipe/lmstudio-team")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Kind != KindPipe || ref.Name != "lmstudio-team" || ref.String() != "pipe/lmstudio-team" {
		t.Fatalf("unexpected ref: %#v", ref)
	}
}

func TestParseRefRejectsNestedAndUppercaseNames(t *testing.T) {
	for _, raw := range []string{"pipe/a/b", "pipe/LMS", "unknown/foo", "pipe/"} {
		if _, err := ParseRef(raw); err == nil {
			t.Fatalf("expected %q to fail", raw)
		}
	}
}
