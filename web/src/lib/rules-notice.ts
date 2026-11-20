/**
 * The rules' notice (ADR 0040, "The rules' notice"): what a person reads and agrees to before Permaily sends their
 * words, a rule's condition or a message's sentences to a model for their own rules. It is apart from the model's
 * notice of connections (src/lib/model-notice.ts), so changing it stops no connection. The version is the server's
 * (`RulesNoticeVersion` in internal/domain), which a test there compares with this one. Its words are docs/web.md's
 * and come with the rules screens (ADR 0040's pull request 7); a change to them is a new version.
 */
export const RULES_NOTICE_VERSION = '2026-11-20'
