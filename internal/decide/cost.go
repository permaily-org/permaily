package decide

// What a call costs (ADR 0028, "Spend"), in billionths of a US dollar, at
// the prices compiled in above. Input tokens are charged; output is not; a
// request of more than LongRequestTokens input tokens costs twice.

// The estimate's bound (ADR 0028, "Spend"): 1,000 tokens, 400 a statement
// and one a byte of the content's JSON. The re-measure's reported tokens
// were at most half of it (about 169 + 190 a statement + 0.204 a byte).
const (
	estimateBaseTokens      = 1000
	estimateStatementTokens = 400
)

// maxTokens bounds a count of tokens, so that no usage reported overflows
// what it costs.
const maxTokens = 1 << 40

// Cost is what a call's usage cost, in billionths of a US dollar, at the
// compiled prices. Estimate is an upper bound of it for a call not sent yet.
//
// The cached tokens and the tokens written to the cache are part of the
// input tokens, at their own prices; the rest is charged as input. A count
// below zero is read as zero. Whether the usage was reported is the
// caller's to check: an attempt whose answer reported none is charged its
// Estimate.
func Cost(u Usage) int64 {
	in := tokens(u.InputTokens)
	cached := tokens(u.CachedTokens)
	written := tokens(u.CacheWriteTokens)
	plain := max(in-cached-written, 0)
	c := plain*NanoUSDPerInputToken + cached*NanoUSDPerCachedToken + written*NanoUSDPerCacheWriteToken
	if in > LongRequestTokens {
		c *= 2
	}
	return c
}

// Estimate is an upper bound of what c costs once sent: 1,000 tokens, 400 a
// statement and one a byte of its content's JSON (ContentJSON), all at the
// input's price, doubled past LongRequestTokens. It is what the service
// reserves before the attempt and what an attempt whose billing is unknown
// is charged.
func Estimate(c Call) int64 {
	n := int64(estimateBaseTokens) + estimateStatementTokens*int64(len(c.Statements)) + int64(len(ContentJSON(c.Content)))
	return Cost(Usage{InputTokens: n})
}

func tokens(n int64) int64 {
	return min(max(n, 0), maxTokens)
}
