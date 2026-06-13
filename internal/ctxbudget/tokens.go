package ctxbudget

// EstimateTokens returns a rough token count for prompt budgeting.
// Uses len(text)/3 as a conservative heuristic — agent conversations are
// JSON-heavy (tool calls, structured data) where tokenizers average ~3
// chars/token rather than the ~4 chars/token typical of plain English.
// Being conservative here prevents context overflow at the model provider.
func EstimateTokens(text string) int {
	n := len(text) / 3
	if n == 0 && len(text) > 0 {
		n = 1
	}
	return n
}
