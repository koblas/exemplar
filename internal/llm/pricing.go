package llm

// Price is USD per million tokens.
type Price struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64 // 5-minute TTL writes
}

// prices is the cost table, keyed by provider then model. Cost is
// informational, but it feeds the soft ceiling, so unknown
// prices are reported, never guessed.
//
// Claude API rates are from Anthropic's published model table as cached on
// 2026-10-06; 5-minute cache writes are 1.25x input. The Haiku 5.5 cache
// read rate is not in that table and is taken as 0.1x input. Confirm
// against the pricing page before relying on totals.
//
// Bedrock is billed by AWS under its own price list, so it has no entries
// here: Bedrock calls record cost 0 with PriceKnown=false and do not count
// toward the soft ceiling until the contracted rates are added.
var prices = map[string]map[string]Price{
	ProviderClaude: {
		"claude-opus-5-5":   {Input: 4.00, Output: 20.00, CacheRead: 0.20, CacheWrite: 5.00},
		"claude-sonnet-5-5": {Input: 2.00, Output: 10.00, CacheRead: 0.20, CacheWrite: 2.50},
		"claude-haiku-5-5":  {Input: 0.10, Output: 0.50, CacheRead: 0.01, CacheWrite: 0.125},
	},
}

// Usage is token usage as reported by a provider. Input excludes cache
// reads and writes, matching the Anthropic usage block.
type Usage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_input_tokens"`
	CacheWriteTokens int `json:"cache_creation_input_tokens"`
}

// Cost returns the USD cost of usage on provider/model, and false when the
// price is unknown.
func Cost(provider, model string, u Usage) (float64, bool) {
	p, ok := prices[provider][model]
	if !ok {
		return 0, false
	}
	const perM = 1_000_000
	return (float64(u.InputTokens)*p.Input +
		float64(u.OutputTokens)*p.Output +
		float64(u.CacheReadTokens)*p.CacheRead +
		float64(u.CacheWriteTokens)*p.CacheWrite) / perM, true
}
