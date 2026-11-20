package attestation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/permaily-org/permaily/pkg/kmspolicy"
)

// ErrNotBound means an answer came over a TLS connection whose key is not the
// one the attestation document vouches for, so it may not come from the enclave.
var ErrNotBound = errors.New("attestation: not served with the attested TLS key")

// Status is the JSON body of GET /status: facts the enclave reports about its
// own environment. It carries no signature of its own; it is trusted only when
// fetched over the TLS key bound in an attestation document (FetchStatus).
type Status struct {
	Commit string `json:"commit"`
	// KMS describes the key that releases up to v0.7.0 wrapped data keys with
	// directly, or (v0.7.0) still read them from; nil in later releases and
	// when the image stores no mail.
	KMS *KMSStatus `json:"kms,omitempty"`
	// TLS describes the certificate key and the ACME account; nil in images
	// older than this field.
	TLS *TLSStatus `json:"tls,omitempty"`
	// Root describes the key that holds the root secret for this image; nil in
	// images that wrap every data key directly with KMS.
	Root *RootStatus `json:"root,omitempty"`
	// Sigstore says which of Sigstore's trust roots the enclave checks a
	// release against before it hands the root secret over; nil in images
	// older than this field.
	Sigstore *SigstoreStatus `json:"sigstore,omitempty"`
	// Anchors describes the records the enclave refuses an earlier copy of;
	// nil in images older than this field or without such records.
	Anchors *AnchorStatus `json:"anchors,omitempty"`
	// Mail describes the mail the enclave sends through Amazon SES (sign-in
	// messages) and what SES would do with it besides delivering it; nil in
	// images older than this field or that send no mail.
	Mail *MailStatus `json:"mail,omitempty"`
	// Admin counts what the operator did to accounts through the enclave; nil
	// in images older than this field or without accounts.
	Admin *AdminStatus `json:"admin,omitempty"`
	// Model describes the model the enclave asks about what depends on
	// meaning (ADR 0028); nil in images older than this field or that ask
	// none.
	Model *ModelStatus `json:"model,omitempty"`
	// Connections describes the authorization server of the API and MCP,
	// the vendors it marks verified, the rule every connection's reads apply
	// first and the record of what left (ADR 0030, ADR 0031); nil in images
	// older than this field or without connections.
	Connections *ConnectionsStatus `json:"connections,omitempty"`
	// Webhooks describes how a connection's webhook is called (ADR 0039);
	// nil in images older than this field or without webhooks.
	Webhooks *WebhooksStatus `json:"webhooks,omitempty"`
}

// WebhooksStatus is what the enclave reports of connections' webhooks (ADR
// 0039, "What /status and permaily-verify show"). Everything but Since,
// Outcomes and Skipped is compiled into the image. It never holds a vendor
// that is not verified, a URL, a subscription, an account, a connection or
// a count of any of them.
type WebhooksStatus struct {
	// Signature is WebhookSignatureV1a: notices are signed with Ed25519 as
	// Standard Webhooks' "v1a" says; Keys are every key a notice may be
	// signed with now, which the api host also publishes at KeysURL as a
	// JWK set: permaily-verify fails when the two differ.
	Signature string       `json:"signature"`
	Keys      []WebhookKey `json:"keys"`
	KeysURL   string       `json:"keys_url"`
	// NoticeHoldsMail is false: a notice holds no mail, address or
	// identifier of an account, a mailbox, a connection or a message (ADR
	// 0039, "The notice"); permaily-verify fails on true.
	NoticeHoldsMail bool `json:"notice_holds_mail"`
	// SameHost is true: a webhook is only on its vendor's client_id host;
	// Redirects is "none"; Transport is "h2". permaily-verify fails on any
	// other value.
	SameHost  bool   `json:"same_host"`
	Redirects string `json:"redirects"`
	Transport string `json:"transport"`
	// NewMail is WebhookNewMailAfterRules: a notice of new mail is sent only
	// for a message that would leave, every rule applied first and the model
	// asked where it had to be; permaily-verify fails on any other value.
	NewMail string `json:"new_mail"`
	// RevocationSeconds bounds when another enclave stops calling a revoked
	// connection's webhook; permaily-verify fails above 5.
	RevocationSeconds int `json:"revocation_seconds"`
	// The bounds, in seconds: the least time between two notices of one
	// connection, one call's timeout and how long a notice is tried.
	MinIntervalSeconds int `json:"min_interval_seconds"`
	TimeoutSeconds     int `json:"timeout_seconds"`
	GiveUpSeconds      int `json:"give_up_seconds"`
	// Schedules are the fixed choices a schedule may be ("day", "week").
	Schedules []string `json:"schedules"`
	// Since is when this enclave started counting; Outcomes counts its calls
	// by fixed outcome, and Skipped the notices it did not send by fixed
	// reason (every key, also at zero): one enclave's observation.
	Since    time.Time        `json:"since"`
	Outcomes map[string]int64 `json:"outcomes"`
	Skipped  map[string]int64 `json:"skipped"`
}

// WebhookKey is one key a notice may be signed with: its identifier and its
// Ed25519 public key, base64url without padding (a JWK's "x").
type WebhookKey struct {
	ID string `json:"id"`
	X  string `json:"x"`
}

// WebhookSignatureV1a: Ed25519 over the notice's identifier, timestamp and
// body (Standard Webhooks).
const WebhookSignatureV1a = "standard-webhooks-v1a"

// WebhookNewMailAfterRules: a notice of new mail follows the rules and the
// model, never an exact match alone.
const WebhookNewMailAfterRules = "after_rules"

// ConnectionsStatus is what the enclave reports of connections (ADR 0030,
// "What /status and permaily-verify show"). Everything but Refused and Since
// is compiled into the image, so it is what the measured code does. It never
// holds a token, a code, a client's identifier other than the verified
// vendors' domains, an account, a connection or a count of either.
type ConnectionsStatus struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RevocationEndpoint    string   `json:"revocation_endpoint"`
	PushedEndpoint        string   `json:"pushed_endpoint"`
	Resources             []string `json:"resources"`
	// Grants are the grant types the token endpoint takes; ClientAuth the
	// ways a client proves itself there ("none", "private_key_jwt").
	Grants     []string `json:"grants"`
	ClientAuth []string `json:"client_auth"`
	// ClientDocuments is true: clients are identified by their metadata
	// document's URL. DynamicRegistration is false: there is no
	// registration endpoint. permaily-verify fails on any other value.
	ClientDocuments     bool `json:"client_documents"`
	DynamicRegistration bool `json:"dynamic_registration"`
	// The lifetimes, in seconds, and the connection's default and longest
	// life, in days; RevocationSeconds is how long another enclave may take
	// a revoked connection's tokens.
	// RefreshRetrySeconds is how long a refresh token used again for the
	// same scope answers the same pair; a code has no such time (CodeReuse
	// is "denied"). RevocationSeconds bounds when another enclave stops
	// releasing a revoked connection's mail: no byte of any answer leaves
	// later (ADR 0030, finding 5, the owner's decision 14).
	AccessTokenSeconds    int    `json:"access_token_seconds"`
	RefreshTokenSeconds   int    `json:"refresh_token_seconds"`
	RefreshRetrySeconds   int    `json:"refresh_retry_seconds"`
	CodeSeconds           int    `json:"code_seconds"`
	CodeReuse             string `json:"code_reuse"`
	RevocationSeconds     int    `json:"revocation_seconds"`
	ConnectionDefaultDays int    `json:"connection_default_days"`
	ConnectionMaxDays     int    `json:"connection_max_days"`
	// RevocationCovers is RevocationEveryByte: the bound holds for every
	// byte of an answer, an answer already being sent included, which is
	// cut (the owner's decision 14); permaily-verify fails on any other
	// value.
	RevocationCovers string `json:"revocation_covers"`
	// AssertionReplay is "strict": a client assertion's jti is decided by
	// a strict anchored record, not a mark (ADR 0030, C1); permaily-verify
	// fails on any other value.
	AssertionReplay string `json:"assertion_replay"`
	// VendorTransport is "h2": vendors' documents and key sets are fetched
	// over HTTP/2 only (the owner's decision 11).
	VendorTransport string `json:"vendor_transport"`
	// DevicesPerConnection bounds a connection's active devices (token
	// families, the owner's decisions 10 and 13).
	DevicesPerConnection int `json:"devices_per_connection"`
	// ModelUnavailable is what connections get while the model cannot be
	// asked: ModelUnavailableNothingLeaves (the owner's decision 9);
	// permaily-verify fails on any other value.
	ModelUnavailable string `json:"model_unavailable"`
	// Verified is the image's list of verified vendors.
	Verified []VerifiedVendor `json:"verified"`
	// Rules are the rules every connection's reads apply first: in step 5
	// the default rule, with the canonical identifiers of its formulas.
	Rules []RuleStatus `json:"rules"`
	// Receipts says how the record of what left is kept: sealed, and, from
	// step 8, chained with its head anchored.
	Receipts ReceiptsStatus `json:"receipts"`
	// Since is when this enclave started counting; Refused counts, by fixed
	// reason (a key of the image, never one derived from what was sent),
	// the requests this enclave refused since then (every reason, also at
	// zero): one enclave's observation, not the deployment's.
	Since   time.Time        `json:"since"`
	Refused map[string]int64 `json:"refused"`
}

// ModelUnavailableNothingLeaves: while the model cannot be asked, no
// connection is given any mail.
const ModelUnavailableNothingLeaves = "nothing_leaves"

// RevocationEveryByte: a revocation's bound holds for every byte, not only
// an answer's first.
const RevocationEveryByte = "every_byte"

// VerifiedVendor is a domain of the image's list and whose it is.
type VerifiedVendor struct {
	Domain string `json:"domain"`
	Owner  string `json:"owner"`
}

// RuleStatus is a rule compiled into the image.
type RuleStatus struct {
	ID       string   `json:"id"`
	Version  int      `json:"version"`
	Formulas []string `json:"formulas"`
	On       bool     `json:"on"`
}

// ReceiptsStatus is how receipts are kept, and how the decisions of the
// rules that protect are (ADR 0031): Decisions is "strict" (strict anchored
// records, begun before the model is asked, each final one with the
// evidence it rests on); DecisionsPerDay is the least number of
// model-decided messages a mailbox's day holds before the rest is held,
// undecided (the owner's decision 12).
//
// From the release that chains receipts (ADR 0037), Chained is true and
// Chain is ReceiptsChainStrict: each account's receipts named by numbered
// links, each a strict anchored record made once. Incidents is how many
// entries the deployment's record of incidents holds: every finding of a
// deletion or a change any enclave made, recorded before it was reported,
// which a later clean pass, a restart, a release or the account's removal
// never takes back. Broken counts, by every kind of ReceiptsBrokenKinds
// (also at zero), what this enclave found and has not recorded yet: one
// enclave's observation, never an account, a number or a digest. Audit is
// the deployment's newest finished pass over every chain the catalogue
// names, read strictly from its record, so both enclaves report the same
// one; nil before the first. Check is what permaily-verify requires of
// them.
type ReceiptsStatus struct {
	Sealed          bool               `json:"sealed"`
	Chained         bool               `json:"chained"`
	Chain           string             `json:"chain,omitempty"`
	Incidents       *ReceiptsIncidents `json:"incidents,omitempty"`
	Broken          map[string]int64   `json:"broken,omitempty"`
	Audit           *ReceiptsAudit     `json:"audit,omitempty"`
	Decisions       string             `json:"decisions"`
	DecisionsPerDay int                `json:"decisions_per_day"`
}

// ReceiptsAudit is one finished pass of the audit of receipts' chains (ADR
// 0037, "The audit"): when it began and finished; Catalogue, the head of
// the catalogue of chains it read every entry up to; Chains, the chains it
// checked from their heads to link 1; Removed, those of accounts whose
// removal their record shows, not checked; Broken, what it found, by every
// kind of ReceiptsBrokenKinds (also at zero); Recovered, the receipts it
// found without a link RecoverAfter after they were written and linked
// then (a write that failed after its entry, or an earlier release's), which
// is not a deletion; Unlinked, the receipts it found without a link older
// than RecoverWithin, which it does not recover and which can be deleted
// unseen; Expired, the links whose commits' lock ended and were deleted,
// their bytes authenticated by the link after and a fork there no longer
// shown. None of the three is a deletion the storage made.
type ReceiptsAudit struct {
	Began     time.Time        `json:"began"`
	Finished  time.Time        `json:"finished"`
	Catalogue uint64           `json:"catalogue"`
	Chains    int64            `json:"chains"`
	Removed   int64            `json:"removed"`
	Broken    map[string]int64 `json:"broken"`
	Recovered int64            `json:"recovered"`
	Unlinked  int64            `json:"unlinked"`
	Expired   int64            `json:"expired"`
}

// ReceiptsIncidents is the deployment's record of incidents as an enclave
// last read it: Recorded, its head, found by listing its commits (an entry
// whose value is gone still counts), and Read, when.
type ReceiptsIncidents struct {
	Recorded uint64    `json:"recorded"`
	Read     time.Time `json:"read"`
}

// ReceiptsIncidentsStaleAfter is how long after an enclave read the record
// of incidents permaily-verify still takes what it read: it is read every
// ten minutes.
const ReceiptsIncidentsStaleAfter = 30 * time.Minute

// ReceiptsChainStrict: an account's receipts are chained by links that are
// strict anchored records, each made once (ADR 0037).
const ReceiptsChainStrict = "strict"

// ReceiptsAuditStaleAfter is how long after a pass of the audit finished
// permaily-verify still takes it as the deployment's: a pass begins every
// day, and one not finished in a run begins again an hour later.
const ReceiptsAuditStaleAfter = 48 * time.Hour

// ReceiptsBrokenKinds are the kinds ReceiptsStatus.Broken and
// ReceiptsAudit.Broken count, each a deletion or a change the storage made:
// a receipt a link names that is missing, does not open or is not what the
// link says; a link whose commit is there and whose document is lost,
// forked, does not open, or is not what the next link names; and the same
// four of an entry of the catalogue of chains, which leaves the chains it
// named unknown. A receipt no link names is not one of them (Recovered).
var ReceiptsBrokenKinds = []string{
	"receipt_missing", "receipt_unreadable", "receipt_changed",
	"link_lost", "link_forked", "link_unreadable", "link_prev",
	"catalogue_lost", "catalogue_forked", "catalogue_unreadable", "catalogue_prev",
}

// Check says what permaily-verify requires of how receipts are kept, at
// now by the verifier's clock: sealed and chained by strict links; the
// record of incidents read within ReceiptsIncidentsStaleAfter and empty,
// so a deletion found once keeps failing it whatever a later pass found; a
// finished pass of the audit, of every kind counted, none negative, that
// finished no more than ReceiptsAuditStaleAfter before now and no later
// than now; and nothing broken, by the audit or by the enclave that
// answered. A kind this verifier does not know counts as broken. Unlinked
// and Expired are not deletions: permaily-verify prints them. The first
// error says why it fails.
func (r *ReceiptsStatus) Check(now time.Time) error {
	switch {
	case !r.Sealed:
		return errors.New("receipts of what left are not sealed")
	case !r.Chained || r.Chain != ReceiptsChainStrict:
		return errors.New("receipts of what left are not chained by strict records: whoever writes the storage could delete one unseen")
	case r.Incidents == nil:
		return errors.New("the record of incidents of receipts' chains could not be read")
	case r.Incidents.Read.After(now) || now.Sub(r.Incidents.Read) > ReceiptsIncidentsStaleAfter:
		return fmt.Errorf("the record of incidents of receipts' chains was not read in the %s before now", ReceiptsIncidentsStaleAfter)
	case r.Incidents.Recorded > 0:
		return fmt.Errorf("the record of incidents holds %d entries: the storage deleted or changed receipts or links of the record of what left, and a later clean pass does not take that back", r.Incidents.Recorded)
	case r.Audit == nil:
		return errors.New("no pass of the audit of receipts' chains has finished: nothing says the chains were checked")
	case r.Audit.Finished.Before(r.Audit.Began) || r.Audit.Finished.After(now):
		return errors.New("the audit of receipts' chains reports a pass that finished before it began or after now")
	case now.Sub(r.Audit.Finished) > ReceiptsAuditStaleAfter:
		return fmt.Errorf("the newest finished pass of the audit of receipts' chains is more than %s old", ReceiptsAuditStaleAfter)
	case r.Audit.Chains < 0 || r.Audit.Removed < 0 || r.Audit.Recovered < 0 || r.Audit.Unlinked < 0 || r.Audit.Expired < 0:
		return errors.New("the audit of receipts' chains counts below zero")
	}
	if err := checkBroken("the audit", r.Audit.Broken); err != nil {
		return err
	}
	return checkBroken("the enclave that answered", r.Broken)
}

// checkBroken fails on a count of a kind this verifier does not know, on a
// kind missing, and on any count above zero or below it.
func checkBroken(who string, counts map[string]int64) error {
	for k, n := range counts {
		if !slices.Contains(ReceiptsBrokenKinds, k) {
			return fmt.Errorf("%s reports receipts' chains broken in a way this verifier does not know (%q)", who, k)
		}
		if n < 0 {
			return fmt.Errorf("%s counts %s below zero", who, k)
		}
	}
	var found []string
	for _, k := range ReceiptsBrokenKinds {
		n, ok := counts[k]
		if !ok {
			return fmt.Errorf("%s does not count %s", who, k)
		}
		if n > 0 {
			found = append(found, fmt.Sprintf("%s %d", k, n))
		}
	}
	if len(found) > 0 {
		return fmt.Errorf("%s found receipts or links of the record of what left that the storage deleted or changed: %s", who, strings.Join(found, ", "))
	}
	return nil
}

// ModelStatus is what the enclave reports of the model it asks (ADR 0028,
// "What /status and permaily-verify show"). Provider, Endpoint, Model,
// PromptVersion, the pinned organization and project, the limits and the
// retention the notice states are compiled into the image, so they are what
// the measured code does; the rest is this enclave's state since it started.
// It never holds a key, an amount of money spent, an account, a topic or
// anything of a message.
type ModelStatus struct {
	Provider      string `json:"provider"`       // "openai"
	Endpoint      string `json:"endpoint"`       // "https://api.openai.com/v1/decisions"
	Model         string `json:"model"`          // "gpt-6-luna"
	PromptVersion string `json:"prompt_version"` // "p2-2026-11-15"
	// PromptCache is "off": every request turns the provider's prompt cache
	// off, so no copy of the input is kept as cache state.
	PromptCache  string `json:"prompt_cache"`
	Organization string `json:"organization,omitempty"`
	Project      string `json:"project,omitempty"`
	// ZeroDataRetention is what the image says of the provider's retention,
	// and so what the notice a person agrees to says: false until the
	// provider approves zero data retention and a release says so.
	ZeroDataRetention bool `json:"zero_data_retention"`
	// The spend guard's limits, in US dollars: the month's stop, the day's
	// alarm, and one account's share of a day in one enclave.
	MonthLimitUSD float64 `json:"month_limit_usd"`
	DayAlarmUSD   float64 `json:"day_alarm_usd"`
	AccountDayUSD float64 `json:"account_day_usd"`
	// KeySet says whether the operator gave the enclave a key, and when.
	KeySet   bool      `json:"key_set"`
	KeySetAt time.Time `json:"key_set_at,omitzero"`
	// Guard is ModelGuardOK, or why nothing new is asked.
	Guard  string         `json:"guard"`
	Canary *CanaryStatus  `json:"canary,omitempty"`
	Since  time.Time      `json:"since"`
	Calls  map[string]int `json:"calls"` // by ModelCall kind, since Since; every kind is there, also at zero
}

// What the spend guard says (ModelStatus.Guard).
const (
	ModelGuardOK            = "ok"
	ModelGuardMonthLimit    = "month_limit"    // the month's limit is reached: nothing new is asked until the next month (UTC)
	ModelGuardProviderLimit = "provider_limit" // the provider said its own spend or credit limit is reached
	ModelGuardKeyRefused    = "key_refused"    // no key is set, or the provider refused it
	ModelGuardUnreadable    = "unreadable"     // the month's spend record cannot be read (lost, forked, or does not open): nothing new is asked this month
)

// CanaryStatus is what the daily canary last found: a heuristic sample of
// fixed synthetic statements, compared with the answers measured for this
// prompt version (ADR 0028, "The canary"). It says what a check observed,
// never when the model changed or how it reads other mail.
type CanaryStatus struct {
	// Baseline is the hex SHA-256 of the canary compiled into the image
	// (decide.Canary.Baseline); the state below is for it, this model and
	// this prompt version, kept apart from any other's.
	Baseline string `json:"baseline"`
	// CheckedAt is when the last check completed; zero before one did.
	CheckedAt time.Time `json:"checked_at,omitzero"`
	// State is the first that applies of: CanaryUnreadable when the state's
	// record cannot be read; CanaryStale when no check completed in the
	// last two days (UTC), counted from the last completed check or, before
	// one, from when the first enclave of this baseline started;
	// CanaryChanged when any completed check of this baseline observed a
	// change (it stays until a release takes new answers as expected, a new
	// baseline); CanaryFailed when this enclave's latest run did not
	// complete and no check completed today; CanarySame when the last
	// completed check found none; CanaryNone before any completed.
	State string `json:"state"`
	// ChangeSeenAt is the last day a check observed an answer that moved,
	// or another model answer; it never goes back.
	ChangeSeenAt time.Time `json:"change_seen_at,omitzero"`
}

// What the canary found (CanaryStatus.State).
const (
	CanaryNone       = "none"
	CanarySame       = "same"
	CanaryChanged    = "changed"
	CanaryFailed     = "failed"
	CanaryStale      = "stale"
	CanaryUnreadable = "unreadable"
)

// The kinds of calls the enclave counts (ModelStatus.Calls). A later image
// may count kinds that are not here.
const (
	ModelCallAnswered    = "answered"    // a 200 that checked
	ModelCallKept        = "kept"        // an atom answered from what was kept, with no call
	ModelCallRefusal     = "refusal"     // a statement the model refused
	ModelCallTimeout     = "timeout"     // no answer within the deadline, retries included
	ModelCallUnavailable = "unavailable" // 5xx or 429 slow_down after the retries
	ModelCallKeyRefused  = "key_refused" // 401 or 403
	ModelCallLimit       = "limit"       // the provider's spend or credit limit
	ModelCallRequest     = "request"     // 400, 404, 422 or a redirect, never followed: the enclave's own mistake
	ModelCallInvalid     = "invalid"     // a 200 that did not read as an answer to the call
	ModelCallOtherModel  = "other_model" // a 200 that named another model: none of its answers is used
	ModelCallHeld        = "held"        // not asked: the guard or the account's share held it
	ModelCallCanary      = "canary"      // a call of the canary
)

// ModelCalls lists the kinds this package knows, in the order they are shown.
var ModelCalls = []string{
	ModelCallAnswered, ModelCallKept, ModelCallRefusal, ModelCallTimeout, ModelCallUnavailable,
	ModelCallKeyRefused, ModelCallLimit, ModelCallRequest, ModelCallInvalid, ModelCallOtherModel, ModelCallHeld,
	ModelCallCanary,
}

// AdminStatus is what the enclave reports of what its operator did to
// accounts through it, with the admin token: blocking and unblocking one,
// asking to delete one and cancelling that, signing its devices out, making
// and revoking invitations, and setting or removing a provider's application.
// It says how many of each kind, and never which account: this is public.
//
// The operator cannot do through the enclave what would be a way into an
// account (change its contact address or how it is signed into), so there is
// no count of that. What is done to the storage by hand is not seen here.
type AdminStatus struct {
	// Since is when this enclave started counting: when it started. A restart
	// starts the count again.
	Since time.Time `json:"since"`
	// Actions is how many of each kind the enclave did since then, by the
	// name of the kind. Every kind the enclave knows is there, also at zero.
	Actions map[string]int `json:"actions"`
}

// The kinds of what an operator does to accounts through the enclave
// (AdminStatus.Actions). A later image may count kinds that are not here.
const (
	// AdminBlock: an account was blocked, so nobody signs in to it.
	AdminBlock = "block"
	// AdminUnblock: an account was unblocked.
	AdminUnblock = "unblock"
	// AdminDelete: the operator asked to delete an account, which is removed
	// seven days later unless that is cancelled.
	AdminDelete = "delete"
	// AdminDeleteCancel: the operator cancelled the deletion of an account.
	AdminDeleteCancel = "delete_cancel"
	// AdminSessionsClose: every device of an account was signed out.
	AdminSessionsClose = "sessions_close"
	// AdminInvitationsMade counts invitations, not requests for them.
	AdminInvitationsMade = "invitations_made"
	// AdminInvitationRevoked: an invitation that was not used was revoked.
	AdminInvitationRevoked = "invitation_revoked"
	// AdminProviderAppSet and AdminProviderAppRemoved: the application the
	// enclave signs people in with at Google, GitHub, Meta or Microsoft was
	// given to it, or taken away.
	AdminProviderAppSet     = "provider_app_set"
	AdminProviderAppRemoved = "provider_app_removed"
	// AdminMade and AdminRemoved: the super admin made an account an admin
	// of the service, or made it stop being one.
	AdminMade    = "admin_made"
	AdminRemoved = "admin_removed"
	// AdminMailboxPaused and AdminMailboxResumed: a mailbox was made to
	// refuse new mail, keeping what it holds, or to take it again. Never
	// which mailbox: the operator cannot read, disconnect or delete one.
	AdminMailboxPaused  = "mailbox_paused"
	AdminMailboxResumed = "mailbox_resumed"
	// AdminModelKeySet and AdminModelKeyRemoved: the model's API key was
	// given to the enclave, or taken away (ADR 0028). Each counts the
	// operator's requests that succeeded, not changes of the key: a removal
	// when none was set counts, as a key set again does. Never the key.
	AdminModelKeySet     = "model_key_set"
	AdminModelKeyRemoved = "model_key_removed"
)

// AdminActions lists the kinds this package knows, in the order they are shown.
var AdminActions = []string{
	AdminBlock, AdminUnblock, AdminDelete, AdminDeleteCancel, AdminSessionsClose,
	AdminInvitationsMade, AdminInvitationRevoked, AdminProviderAppSet, AdminProviderAppRemoved,
	AdminMade, AdminRemoved, AdminMailboxPaused, AdminMailboxResumed,
	AdminModelKeySet, AdminModelKeyRemoved,
}

// What SES says about an identity, a domain or an address mail is sent from
// (MailState).
const (
	// MailNoConfigurationSet: the identity may send and has no configuration set.
	MailNoConfigurationSet = "no_configuration_set"
	// MailConfigurationSet: the identity has a default configuration set, which
	// SES applies to what is sent from it even when the sender names none.
	MailConfigurationSet = "configuration_set"
	// MailNotVerified: the identity has no configuration set and SES does not
	// let it send, so mail would go out under another identity if any.
	MailNotVerified = "not_verified"
	// MailAbsent: SES has no such identity.
	MailAbsent = "absent"
)

// MailStatus is what the enclave reports about the mail it sends. A
// configuration set can make SES keep a copy of every message (it can be tied
// to an archive), and whoever administers the account can give an identity
// one by default. The enclave names none when it sends, reads from SES
// whether one applies anyway, and sends nothing unless a reading of the last
// few seconds says none does.
type MailStatus struct {
	// Domain is the SES identity mail is sent from, and Address the address:
	// an identity for the address alone, if there is one, replaces the
	// domain's settings, so both are read.
	Domain  string `json:"domain"`
	Address string `json:"address"`
	// CheckedAt is when the enclave last read both identities from SES; zero
	// until it has. Failing means its latest attempt did not get an answer:
	// until one does it sends nothing.
	CheckedAt time.Time `json:"checked_at,omitzero"`
	Failing   bool      `json:"failing,omitempty"`
	// State is what that reading said; nil until there is one.
	State *MailState `json:"state,omitempty"`
	// WatchedSince is when this enclave started reading, about once a minute
	// and around each message it sends, and History every different state it
	// has read since then, oldest first; the last one is the current state. A
	// configuration set that was set and removed again shows up here.
	WatchedSince time.Time         `json:"watched_since,omitzero"`
	History      []MailObservation `json:"history,omitempty"`
	// HistoryTruncated means older states were dropped to bound the answer.
	HistoryTruncated bool `json:"history_truncated,omitempty"`
}

// MailState is what SES says about the two identities: each is
// MailNoConfigurationSet, MailConfigurationSet, MailNotVerified or MailAbsent.
// The name of a configuration set is not reported: whoever sets one chooses it.
type MailState struct {
	DomainIdentity  string `json:"domain_identity"`
	AddressIdentity string `json:"address_identity"`
}

// Clean reports whether no configuration set applies to mail sent from the
// address without naming one. The domain must be an identity that may send:
// without it SES would send under another identity (a parent domain), which
// nobody reads here. The address need not be an identity, and one that may
// not send changes nothing. A value this code does not know is not clean.
func (s MailState) Clean() bool {
	if s.DomainIdentity != MailNoConfigurationSet {
		return false
	}
	switch s.AddressIdentity {
	case MailNoConfigurationSet, MailNotVerified, MailAbsent:
		return true
	}
	return false
}

// MailObservation is one state of the identities, and the first and last time
// the enclave read it without reading a different one in between.
type MailObservation struct {
	MailState
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// AnchorStatus describes the records that cannot be put back to an earlier
// copy (ADR 0016). Each change of one leaves a commit, with its version, in
// an S3 bucket with Object Lock in compliance mode, where nobody in the
// account can remove it until a date; the enclave reads a record by its
// newest commit.
type AnchorStatus struct {
	// Bucket is where the commits are, and RetentionDays for how long each is
	// kept from when it is written.
	Bucket        string `json:"bucket"`
	RetentionDays int    `json:"retention_days"`
	// Records are the ones this enclave read or wrote since it started.
	Records []AnchoredRecord `json:"records"`
	// Regressions are commits this enclave had seen and the store stopped
	// answering with. There should be none, ever.
	Regressions []AnchorRegression `json:"regressions,omitempty"`
}

// AnchoredRecord is the newest commit of a record.
type AnchoredRecord struct {
	Name        string    `json:"name"`
	Version     uint64    `json:"version"`
	CommittedAt time.Time `json:"committed_at"`
	// LockedUntil is until when S3 keeps that commit whatever anyone asks.
	LockedUntil time.Time `json:"locked_until,omitzero"`
}

// AnchorRegression is a commit that was there and then was not.
type AnchorRegression struct {
	Name string `json:"name"`
	// Had is the version the enclave knew, and Found the newest one the
	// store answered with afterwards (0 for none).
	Had   uint64    `json:"had"`
	Found uint64    `json:"found"`
	At    time.Time `json:"at"`
	// Fork says the record has two commits of different digests at one
	// version (Found): whoever writes to the store of commits hid one while an
	// enclave wrote the other, and the record is refused (ADR 0026, a strict
	// record). Unlocked says a commit (at Found) has no copy that S3 keeps
	// locked: the store did not keep a lock it took. Lost says the newest
	// commit (at Found) names a value the table no longer holds: whoever
	// writes to it deleted or changed it, and the record is refused. Each
	// fails verification, as any regression does.
	Fork     bool `json:"fork,omitempty"`
	Unlocked bool `json:"unlocked,omitempty"`
	Lost     bool `json:"lost,omitempty"`
}

// SigstoreStatus identifies a trust root that Sigstore published through its
// TUF repository (https://tuf-repo-cdn.sigstore.dev). The enclave starts with
// the one compiled into its image, and takes a later one only with the
// signatures of Sigstore's root keyholders; even then who issues signing
// certificates stays as compiled in (ADR 0013).
type SigstoreStatus struct {
	// TrustedRootSHA256 is the hash of trusted_root.json, in hex: the file is
	// at /targets/<hash>.trusted_root.json of the repository.
	TrustedRootSHA256 string `json:"trusted_root_sha256"`
	// TUFRootVersion and TUFTargetsVersion are the versions of the TUF
	// metadata that vouch for it.
	TUFRootVersion    int `json:"tuf_root_version"`
	TUFTargetsVersion int `json:"tuf_targets_version"`
	// UpdatedAt is when the enclave was given this trust root; zero while it
	// uses the one compiled in.
	UpdatedAt time.Time `json:"updated_at,omitzero"`
}

// How an image got the root secret (RootStatus.Origin).
const (
	// RootCreated: an enclave running this same image created the secret.
	RootCreated = "created"
	// RootHandedOver: an enclave running another public release handed it over.
	RootHandedOver = "handed_over"
)

// RootStatus is what the enclave reports about the root secret, the one
// secret that everything it stores is sealed under. The secret is locked by a
// KMS key that belongs to this image alone; a verifier checks that with
// kmspolicy.Sealed, and because nobody can change the key's policy, what was
// true when the enclave read it stays true.
type RootStatus struct {
	// Image is the PCR0 the enclave measured itself as.
	Image  string `json:"image"`
	KeyARN string `json:"key_arn"`
	// Policy is the key policy document, exactly as KMS returned it.
	Policy          string `json:"policy"`
	Grants          int    `json:"grants"`
	GrantsTruncated bool   `json:"grants_truncated,omitempty"`
	// Key is what KMS says about the key itself.
	Key kmspolicy.Key `json:"key"`
	// CheckedAt is when the enclave last read the policy and the grants.
	CheckedAt time.Time `json:"checked_at"`
	// WatchedSince is when this enclave started reading the policy and the
	// grants, about once a minute, and History every different state it has
	// read since then, oldest first; the last one is the current state. Nobody
	// in the account can change the policy, but AWS can for the account's
	// owner; a change would show up here. Gaps are the periods in which the
	// enclave could not read from KMS.
	WatchedSince     time.Time        `json:"watched_since,omitzero"`
	History          []KMSObservation `json:"history,omitempty"`
	HistoryTruncated bool             `json:"history_truncated,omitempty"`
	Gaps             []KMSGap         `json:"gaps,omitempty"`
	GapsTruncated    bool             `json:"gaps_truncated,omitempty"`
	// Origin is RootCreated or RootHandedOver, WrittenAt when the record for
	// this image was written, and From the release that wrote it when it was
	// handed over.
	Origin    string    `json:"origin"`
	WrittenAt time.Time `json:"written_at"`
	From      *RootFrom `json:"from,omitempty"`
}

// RootFrom is the release whose enclave handed the root secret to this image.
// The enclave accepted it only with that release's Sigstore provenance.
type RootFrom struct {
	Image       string    `json:"image"`
	Commit      string    `json:"commit"`
	PublishedAt time.Time `json:"published_at"`
}

// TLSStatus is what the enclave reports about its own TLS identity.
type TLSStatus struct {
	// SPKISHA256 is the hex SHA-256 of the certificate key, the same value
	// bound in attestation documents.
	SPKISHA256 string `json:"spki_sha256"`
	// KeyCreatedAt is when the key was generated. A certificate for the
	// enclave's names issued after this time with another key was not issued
	// to the enclave.
	KeyCreatedAt time.Time `json:"key_created_at"`
	// Sealed is true when the key is stored sealed with KMS and so survives
	// restarts; false means it lives only in this enclave's memory.
	Sealed bool `json:"sealed"`
	// ACMEAccount is the URL of the ACME account that asks for certificates,
	// empty until the first one is issued. CAA records should name it.
	ACMEAccount string `json:"acme_account,omitempty"`
}

// KMSStatus is what the enclave read from AWS KMS about its pinned key.
type KMSStatus struct {
	KeyARN string `json:"key_arn"`
	// Policy is the key policy document, exactly as KMS returned it.
	Policy string `json:"policy"`
	// Grants counts the grants on the key. Grants give access outside the
	// policy, so a verifier expects none.
	Grants          int  `json:"grants"`
	GrantsTruncated bool `json:"grants_truncated"`
	// FetchedAt is when the enclave read the policy and the grants.
	FetchedAt time.Time `json:"fetched_at"`

	// WatchedSince is when this enclave started reading the policy, about
	// once a minute. Zero in images that do not keep a history.
	WatchedSince time.Time `json:"watched_since,omitzero"`
	// History is every different state of the policy and the grants the
	// enclave has read since then, oldest first; the last one is the current
	// state. A policy that was changed and changed back shows up here.
	History []KMSObservation `json:"history,omitempty"`
	// HistoryTruncated means older states were dropped to bound the answer.
	HistoryTruncated bool `json:"history_truncated,omitempty"`
	// Gaps are the periods in which the enclave could not read from KMS. A
	// change made and undone inside a gap is not in History.
	Gaps          []KMSGap `json:"gaps,omitempty"`
	GapsTruncated bool     `json:"gaps_truncated,omitempty"`
}

// KMSObservation is one state of the key policy and grants, and the first and
// last time the enclave read it without reading a different one in between.
type KMSObservation struct {
	Policy          string    `json:"policy"`
	Grants          int       `json:"grants"`
	GrantsTruncated bool      `json:"grants_truncated,omitempty"`
	FirstSeen       time.Time `json:"first_seen"`
	LastSeen        time.Time `json:"last_seen"`
}

// KMSGap is a period between two successful readings with failed ones in between.
type KMSGap struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// FetchStatus gets baseURL/status and checks that the TLS certificate of that
// connection has the key whose SPKI hash is spkiHash, taken from a verified
// attestation (RemoteResult.SPKIHash). The enclave image holds that key, so
// the body was produced by the attested code.
func FetchStatus(ctx context.Context, client *http.Client, baseURL string, spkiHash []byte) (*Status, error) {
	body, err := fetchBound(ctx, client, strings.TrimRight(baseURL, "/")+"/status", spkiHash)
	if err != nil {
		return nil, fmt.Errorf("fetch status: %w", err)
	}
	var st Status
	if err := json.Unmarshal(body, &st); err != nil {
		return nil, fmt.Errorf("%w: status: %w", ErrMalformed, err)
	}
	return &st, nil
}

// fetchBound gets url and returns the body only when the connection's TLS
// certificate has the attested key (spkiHash) and the answer is 200.
func fetchBound(ctx context.Context, client *http.Client, url string, spkiHash []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
		return nil, ErrNotTLS
	}
	if !bytes.Equal(SPKIHash(resp.TLS.PeerCertificates[0]), spkiHash) {
		return nil, ErrNotBound
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body[:min(len(body), 512)])))
	}
	return body, nil
}
