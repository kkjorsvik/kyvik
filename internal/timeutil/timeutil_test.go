package timeutil

import "testing"

func TestParseTimestampUTC(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "rfc3339", input: "2026-02-18T02:42:37Z"},
		{name: "sqlite", input: "2026-02-18 02:42:37"},
		{name: "go time string", input: "2026-02-18 01:31:42 +0000 UTC"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseTimestampUTC(tc.input)
			if err != nil {
				t.Fatalf("ParseTimestampUTC(%q): %v", tc.input, err)
			}
			if got.IsZero() {
				t.Fatalf("ParseTimestampUTC(%q) returned zero time", tc.input)
			}
			if got.Location().String() != "UTC" {
				t.Fatalf("expected UTC location, got %q", got.Location().String())
			}
		})
	}
}
