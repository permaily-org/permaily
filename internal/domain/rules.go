package domain

// RulesNoticeVersion is the version of the rules' notice (ADR 0040, "The
// rules' notice"): what is sent to OpenAI to turn a person's words into a
// rule, to decide a rule's condition a model decides and to remove details by
// meaning, and how long a rule's decisions are kept. It is the notice's own,
// apart from the model's notice of connections (ModelNoticeVersion), so a
// change of it stops no connection: a rule saved under another version asks
// the model nothing until the person agrees to this one and saves it again.
// The web app's, in web/src/lib/rules-notice.ts, must be the same, which
// TestRulesNoticeVersionMatchesTheWebApp checks.
const RulesNoticeVersion = "2026-11-20"
