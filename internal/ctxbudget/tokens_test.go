package ctxbudget

import "testing"

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		{"empty", "", 0},
		{"one char", "a", 1},
		{"two chars", "ab", 1},
		{"three chars", "abc", 1},
		{"five chars", "abcde", 1},
		{"six chars", "abcdef", 2},
		{"nine chars", "abcdefghi", 3},
		{"short sentence", "hello world", 3},
		{"long text", "The quick brown fox jumps over the lazy dog and then some more text.", 22},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateTokens(tt.text)
			if got != tt.want {
				t.Errorf("EstimateTokens(%q) = %d, want %d", tt.text, got, tt.want)
			}
		})
	}
}

func TestEstimateTokensNonEmpty(t *testing.T) {
	// Any non-empty string should return at least 1.
	for _, s := range []string{"a", "ab", "abc"} {
		if got := EstimateTokens(s); got < 1 {
			t.Errorf("EstimateTokens(%q) = %d, want >= 1", s, got)
		}
	}
}
