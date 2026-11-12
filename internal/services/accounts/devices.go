package accounts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/idp"
	"github.com/permaily-org/permaily/internal/store"
)

// Devices (ADR 0017). A browser that signs in keeps a cookie of its own, and
// the account keeps which devices signed in to it, so its person is told by
// mail of a sign-in from one that had not.
//
// What is kept is an entry under the account, named by a hash of the cookie's
// value made with the account's identifier: it says nothing of the browser,
// and is no use for another account. It is signed where it is stored, like a
// session. It is no way into anything: whoever writes to the storage can
// delete one, which sends the person one notice more, or put back one that
// was dropped, which sends one less.

// Browser is what a sign-in knows of the browser it ends in.
type Browser struct {
	// UserAgent is what the browser says of itself.
	UserAgent string
	// Device is the value of the browser's device cookie: the one it had, or
	// the one it is given when this sign-in ends (NewDevice). Anything else
	// is a device nobody knows, and nothing is kept of it.
	Device string
}

// NewDevice returns the value of the cookie of a device that has none.
func NewDevice() string { return newSecret() }

const devicePrefix = "device#"

// deviceDoc is a device in its account's entries. SeenAt is when it last
// signed in: it is known for domain.DeviceLifetime from then.
type deviceDoc struct {
	V       int       `json:"v"`
	Account string    `json:"account"`
	SeenAt  time.Time `json:"seen_at"`
}

func deviceKey(accountID, device string) string {
	sum := sha256.Sum256([]byte("permaily device v1\x00" + accountID + "\x00" + device))
	return devicePrefix + hex.EncodeToString(sum[:])
}

// readDevice returns the device in an entry, or nil when the entry is not a
// device of that account that is still known.
func (s *Service) readDevice(key []byte, e *store.Entry, accountID string) *deviceDoc {
	var doc deviceDoc
	raw, err := envelope.Verified(key, e.Value, entryAAD(e.Partition, e.Key))
	// The time comes from what was signed, not from the entry's own expiry.
	if err != nil || json.Unmarshal(raw, &doc) != nil || doc.Account != accountID || !doc.SeenAt.Add(domain.DeviceLifetime).After(s.now()) {
		return nil
	}
	return &doc
}

// known reports whether a device signed in to the account before, and keeps
// that it does now. A failure of the storage is logged and never fails the
// sign-in: the device is then taken for a new one unless it was read as known.
func (s *Service) known(ctx context.Context, accountID, device string) bool {
	if !domain.DeviceToken(device) {
		return false
	}
	known, err := s.noteDevice(ctx, accountID, device)
	if err != nil {
		s.cfg.Log.Warn("accounts: a device could not be kept", "err", scrub(err))
	}
	return known
}

func (s *Service) noteDevice(ctx context.Context, accountID, device string) (bool, error) {
	key, err := s.signingKey(ctx)
	if err != nil {
		return false, err
	}
	partition, name := accountPartition(accountID), deviceKey(accountID, device)
	known, version := false, uint64(0)
	e, err := s.cfg.Entries.GetEntry(ctx, partition, name)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return false, err
	default:
		known, version = s.readDevice(key, e, accountID) != nil, e.Version
	}
	if !known {
		if err := s.dropDevices(ctx, key, accountID); err != nil {
			return false, err
		}
	}
	now := s.now()
	put := &store.Entry{Partition: partition, Key: name, Version: version, ExpiresAt: now.Add(domain.DeviceLifetime), UpdatedAt: now}
	raw, _ := json.Marshal(deviceDoc{V: 1, Account: accountID, SeenAt: now})
	if put.Value, err = envelope.Sign(key, raw, entryAAD(partition, name)); err != nil {
		return known, err
	}
	// Another sign-in of the same device, at the same time, may have written it.
	if err := s.cfg.Entries.PutEntry(ctx, put); err != nil && !errors.Is(err, store.ErrExists) && !errors.Is(err, store.ErrConflict) {
		return known, err
	}
	return known, nil
}

// dropDevices makes room for one more device: the entries that are no device
// of the account go, and the ones that signed in longest ago while there are
// as many as an account keeps.
func (s *Service) dropDevices(ctx context.Context, key []byte, accountID string) error {
	entries, err := s.cfg.Entries.ListEntries(ctx, accountPartition(accountID), devicePrefix, "", 2*domain.MaxDevices)
	if err != nil {
		return err
	}
	type seen struct {
		key string
		at  time.Time
	}
	var kept []seen
	for i := range entries {
		// One that is not a device is before every device: it goes first.
		at := time.Time{}
		if doc := s.readDevice(key, &entries[i], accountID); doc != nil {
			at = doc.SeenAt
		}
		kept = append(kept, seen{entries[i].Key, at})
	}
	slices.SortFunc(kept, func(a, b seen) int { return a.at.Compare(b.at) })
	for i := 0; i < len(kept) && (kept[i].at.IsZero() || len(kept)-i >= domain.MaxDevices); i++ {
		if err := s.forget(ctx, accountPartition(accountID), kept[i].key); err != nil {
			return err
		}
	}
	return nil
}

// way is how a sign-in proved who the person is, in the words of a notice.
type way string

const (
	wayMail     way = "with a code or a link sent by email"
	wayPasskey  way = "with a passkey"
	wayRecovery way = "with a recovery code"
)

// providerNames are the providers as a person knows them. Meta's sign-in is
// Facebook's (ADR 0017).
var providerNames = map[idp.Provider]string{
	idp.Google: "Google", idp.GitHub: "GitHub", idp.Meta: "Facebook", idp.Microsoft: "Microsoft",
}

func wayProvider(p idp.Provider) way { return way("with " + providerNames[p]) }

// enter signs a browser in to the account of a person who just proved who
// they are, and returns the account and the value of the session's cookie. It
// is the one door: every way of signing in ends here.
//
// An account the operator blocked lets nobody in, by any way. The person is
// told by mail when the device had not signed in to the account before,
// unless the account was made by this very sign-in.
func (s *Service) enter(ctx context.Context, doc *accountDoc, how way, from Browser, made bool) (*contracts.Me, string, error) {
	if doc.blocked() {
		return nil, "", contracts.ErrAccountBlocked
	}
	session, cookie, err := s.newSession(ctx, doc.ID, from.UserAgent)
	if err != nil {
		return nil, "", err
	}
	if !s.known(ctx, doc.ID, from.Device) && !made {
		s.notify(ctx, noticeNewDevice, newDeviceMail(doc.Address, s.rp.ID, s.now(), how, session.Device))
	}
	return &contracts.Me{Account: s.view(doc), Session: *session, Terms: s.terms()}, cookie, nil
}
