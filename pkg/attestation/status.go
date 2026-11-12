package attestation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
)

// AdminActions lists the kinds this package knows, in the order they are shown.
var AdminActions = []string{
	AdminBlock, AdminUnblock, AdminDelete, AdminDeleteCancel, AdminSessionsClose,
	AdminInvitationsMade, AdminInvitationRevoked, AdminProviderAppSet, AdminProviderAppRemoved,
	AdminMade, AdminRemoved,
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
	// locked: the store did not keep a lock it took. Either fails
	// verification, as any regression does.
	Fork     bool `json:"fork,omitempty"`
	Unlocked bool `json:"unlocked,omitempty"`
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
