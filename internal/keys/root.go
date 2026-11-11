package keys

import (
	"bytes"
	"context"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"

	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/pkg/attestation"
	"github.com/permaily-org/permaily/pkg/kmspolicy"
	"github.com/permaily-org/permaily/pkg/provenance"
)

// Errors of the root secret. Use errors.Is.
var (
	// ErrDebugMode means the enclave runs in debug mode: its measurements are
	// zero, no key is released to it, and it must not create one.
	ErrDebugMode = errors.New("keys: the enclave runs in debug mode and gets no keys")
	// ErrAwaitingHandoff means the deployment already has a root secret and no
	// enclave has handed it to this image yet.
	ErrAwaitingHandoff = errors.New("keys: waiting for the running release to hand over the root secret")
	// ErrRootRecord means the stored root secret was not written for this
	// image by an enclave running a public release.
	ErrRootRecord = errors.New("keys: the stored root secret is not from a public release")
	// ErrNotThisImage means the proof given for the running image is not about it.
	ErrNotThisImage = errors.New("keys: the release given as the running one is not this image")
	// ErrDowngrade means the image asked for was published before the running one.
	ErrDowngrade = errors.New("keys: the release was published before the running one")
	// ErrKeyAccount means a KMS key is outside the account and region compiled into the image.
	ErrKeyAccount = errors.New("keys: the KMS key is not in the pinned account and region")
)

const (
	rootSecretSize = 32
	// wrapMagic starts every data key wrapped under the root secret. A key
	// wrapped by KMS never starts like this.
	wrapMagic = "PRK1"
	// deploymentRecord exists once a deployment has a root secret. An image
	// that finds it and has no root record of its own waits for a handoff
	// instead of starting a second, empty deployment.
	deploymentRecord = "deployment"
	rootRetry        = 5 * time.Second
)

// ReleaseKeyAlias is the alias of the KMS key that belongs to the image whose
// PCR0 is image (lowercase hex). The alias only locates the key: every key is
// checked with kmspolicy.Sealed before it is used.
func ReleaseKeyAlias(image string) string { return "alias/permaily-release-" + image }

// RootRecordName is the name of the record that holds the root secret for an image.
func RootRecordName(image string) string { return "root-" + image }

// RootAPI is the subset of the KMS client used for the root secret.
type RootAPI interface {
	DescribeKey(ctx context.Context, in *kms.DescribeKeyInput, opts ...func(*kms.Options)) (*kms.DescribeKeyOutput, error)
	GetKeyPolicy(ctx context.Context, in *kms.GetKeyPolicyInput, opts ...func(*kms.Options)) (*kms.GetKeyPolicyOutput, error)
	ListGrants(ctx context.Context, in *kms.ListGrantsInput, opts ...func(*kms.Options)) (*kms.ListGrantsOutput, error)
	GenerateDataKey(ctx context.Context, in *kms.GenerateDataKeyInput, opts ...func(*kms.Options)) (*kms.GenerateDataKeyOutput, error)
	Decrypt(ctx context.Context, in *kms.DecryptInput, opts ...func(*kms.Options)) (*kms.DecryptOutput, error)
}

// Attester produces an attestation document that carries publicKey, which
// KMS uses as the recipient of data keys (Recipient parameter).
type Attester interface {
	AttestPublicKey(publicKey []byte) ([]byte, error)
}

// RootAttester is the Nitro Security Module as the root secret uses it.
type RootAttester interface {
	Attester
	// Attest returns an attestation document that carries userData.
	Attest(nonce, userData []byte) ([]byte, error)
	// Measurements returns PCR0 to PCR2 of this enclave's own image.
	Measurements() (map[uint][]byte, error)
}

// RootConfig is what a Root needs. Everything in it that decides whom to
// trust is compiled into the image.
type RootConfig struct {
	API      RootAPI
	Attester RootAttester
	Records  store.Records
	// Account and Region are where release keys must live.
	Account, Region string
	// Releases says which builds are public releases, and Trust pins the
	// certificate authority and the transparency log that prove it.
	Releases provenance.Policy
	Trust    *provenance.TrustRoot
	// AttestationRoot signs attestation documents; nil is the AWS root.
	AttestationRoot *x509.Certificate
	Now             func() time.Time
	Log             *slog.Logger
}

// Root is the root secret of a deployment: the one secret everything stored
// is sealed under, through per-record data keys (it implements Service).
//
// Outside enclaves the root secret exists only in records, one per image.
// Each is locked by a KMS key that only that image can use and whose policy
// nobody can change, and each carries an attestation document from the
// enclave that wrote it. An image accepts its record only when that enclave
// ran the same image or a public release, and hands the root secret only to a
// public release newer than itself (ADR 0013). So a private image never gets
// it, whoever asks.
type Root struct {
	cfg RootConfig
	rsa *rsa.PrivateKey
	pub []byte
	// retry is how long a failure to load is remembered, and how often Run tries.
	retry time.Duration

	mu     sync.Mutex
	secret []byte
	kek    []byte
	status attestation.RootStatus
	// watch keeps reading the policy and grants of this image's key. Nobody in
	// the account can change them, but AWS can for the account's owner, so the
	// enclave reports every state it sees.
	watch   *PolicyReader
	lastTry time.Time
	lastErr error

	// trust is Sigstore's trust root in use: the one compiled into the image
	// until UpdateTrust is given one that Sigstore published after it.
	trustMu   sync.Mutex
	trust     *provenance.TrustRoot
	trustedAt time.Time
}

// NewRoot creates a Root. Nothing is read until the first use or Run.
func NewRoot(cfg RootConfig) (*Root, error) {
	if cfg.API == nil || cfg.Attester == nil || cfg.Records == nil || cfg.Trust == nil || cfg.Account == "" || cfg.Region == "" {
		return nil, errors.New("keys: incomplete root configuration")
	}
	if err := cfg.Releases.Validate(); err != nil {
		return nil, err
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	pub, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return nil, err
	}
	return &Root{cfg: cfg, rsa: priv, pub: pub, retry: rootRetry, trust: cfg.Trust}, nil
}

func (r *Root) trustRoot() *provenance.TrustRoot {
	r.trustMu.Lock()
	defer r.trustMu.Unlock()
	return r.trust
}

// UpdateTrust makes the enclave check releases against a newer trust root of
// Sigstore's, until it stops: nothing is stored, and a restart goes back to
// the one compiled into the image. It refuses anything that Sigstore's
// keyholders did not publish after the trust root in use, and even then only
// the logs and the timestamp authorities change, never who issues signing
// certificates (provenance.TrustRoot.Update). Who asks does not matter.
func (r *Root) UpdateTrust(u provenance.Update) (*attestation.SigstoreStatus, error) {
	r.trustMu.Lock()
	defer r.trustMu.Unlock()
	next, err := r.trust.Update(u, r.cfg.Now())
	if err != nil {
		return nil, err
	}
	if *next.Publication != *r.trust.Publication {
		r.trust, r.trustedAt = next, r.cfg.Now().UTC().Truncate(time.Second)
		r.cfg.Log.Info("sigstore trust root: updated", "sha256", next.Publication.SHA256, "tuf_root", next.Publication.RootVersion, "tuf_targets", next.Publication.TargetsVersion)
	}
	return r.sigstoreStatus(), nil
}

// Trust says which of Sigstore's trust roots releases are checked against;
// nil for a trust root that did not come from Sigstore's TUF metadata.
func (r *Root) Trust() *attestation.SigstoreStatus {
	r.trustMu.Lock()
	defer r.trustMu.Unlock()
	return r.sigstoreStatus()
}

func (r *Root) sigstoreStatus() *attestation.SigstoreStatus {
	p := r.trust.Publication
	if p == nil {
		return nil
	}
	return &attestation.SigstoreStatus{TrustedRootSHA256: p.SHA256, TUFRootVersion: p.RootVersion, TUFTargetsVersion: p.TargetsVersion, UpdatedAt: r.trustedAt}
}

// Run loads the root secret, retrying until it succeeds, and then keeps
// reading the policy of its key once a minute, until ctx is done.
func (r *Root) Run(ctx context.Context) {
	t := time.NewTicker(r.retry)
	defer t.Stop()
	var last string
	for {
		err := r.ready(ctx)
		if err == nil {
			r.mu.Lock()
			watch := r.watch
			r.mu.Unlock()
			watch.Run(ctx)
			return
		}
		// The same failure is logged once: an image that waits for a handoff
		// would otherwise repeat itself for as long as it waits.
		if msg := err.Error(); msg != last {
			r.cfg.Log.Warn("root secret: not available", "err", msg)
			last = msg
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ready makes sure the root secret is loaded. A failure is remembered for a
// few seconds so callers that retry do not flood KMS.
func (r *Root) ready(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.secret != nil {
		return nil
	}
	if !r.lastTry.IsZero() && r.cfg.Now().Sub(r.lastTry) < r.retry {
		return r.lastErr
	}
	r.lastErr = r.load(ctx)
	r.lastTry = r.cfg.Now()
	return r.lastErr
}

type measured struct {
	pcrs  map[uint][]byte
	image string // PCR0, lowercase hex
}

func (r *Root) measure() (measured, error) {
	pcrs, err := r.cfg.Attester.Measurements()
	if err != nil {
		return measured{}, err
	}
	for _, i := range attestation.MeasuredPCRs {
		if len(pcrs[i]) != 48 {
			return measured{}, fmt.Errorf("keys: PCR%d missing or not SHA-384", i)
		}
		if bytes.Equal(pcrs[i], make([]byte, 48)) {
			return measured{}, ErrDebugMode
		}
	}
	return measured{pcrs: pcrs, image: hex.EncodeToString(pcrs[0])}, nil
}

func samePCRs(a, b map[uint][]byte) bool {
	for _, i := range attestation.MeasuredPCRs {
		if len(a[i]) == 0 || !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

func (r *Root) load(ctx context.Context) error {
	me, err := r.measure()
	if err != nil {
		return err
	}
	rec, err := r.cfg.Records.GetRecord(ctx, RootRecordName(me.image))
	switch {
	case err == nil:
	case !errors.Is(err, store.ErrNotFound):
		return err
	default:
		if _, err := r.cfg.Records.GetRecord(ctx, deploymentRecord); err == nil {
			return ErrAwaitingHandoff
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if rec, err = r.create(ctx, me); err != nil {
			return err
		}
	}
	secret, st, err := r.open(ctx, me, rec)
	if err != nil {
		return err
	}
	kek, err := hkdf.Key(sha256.New, secret, nil, "permaily data key wrapping v1", envelope.KeySize)
	if err != nil {
		return err
	}
	marker := &store.Record{Name: deploymentRecord, WrappedKey: []byte{1}, Value: []byte(`{"version":1}`), UpdatedAt: r.cfg.Now().UTC()}
	if err := r.cfg.Records.PutRecord(ctx, marker, true); err != nil && !errors.Is(err, store.ErrExists) {
		return err
	}
	r.secret, r.kek, r.status = secret, kek, st
	r.watch = NewPolicyReader(r.cfg.API, st.KeyARN)
	r.watch.now = r.cfg.Now
	r.cfg.Log.Info("root secret: loaded", "origin", st.Origin, "key", st.KeyARN)
	return nil
}

// releaseKey is a KMS key that was checked to be sealed to one image.
type releaseKey struct {
	arn     string
	policy  string
	grants  int
	cut     bool
	facts   kmspolicy.Key
	checked time.Time
}

// sealedKey reads a key (by ARN or alias) and checks that only image can
// ever use it. The answers come from KMS over TLS from inside the enclave.
func (r *Root) sealedKey(ctx context.Context, id, image string) (*releaseKey, error) {
	desc, err := r.cfg.API.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(id)})
	if err != nil {
		return nil, fmt.Errorf("keys: describe %s: %w", id, err)
	}
	m := desc.KeyMetadata
	if m == nil {
		return nil, fmt.Errorf("keys: describe %s: empty answer", id)
	}
	arn := aws.ToString(m.Arn)
	if !strings.HasPrefix(arn, "arn:aws:kms:"+r.cfg.Region+":"+r.cfg.Account+":key/") || (strings.HasPrefix(id, "arn:") && arn != id) {
		return nil, fmt.Errorf("%w: %s", ErrKeyAccount, arn)
	}
	pol, err := r.cfg.API.GetKeyPolicy(ctx, &kms.GetKeyPolicyInput{KeyId: aws.String(arn), PolicyName: aws.String("default")})
	if err != nil {
		return nil, fmt.Errorf("keys: get key policy: %w", err)
	}
	grants, err := r.cfg.API.ListGrants(ctx, &kms.ListGrantsInput{KeyId: aws.String(arn), Limit: aws.Int32(100)})
	if err != nil {
		return nil, fmt.Errorf("keys: list grants: %w", err)
	}
	k := &releaseKey{
		arn: arn, policy: aws.ToString(pol.Policy), grants: len(grants.Grants), cut: grants.Truncated,
		facts: kmspolicy.Key{
			Origin: string(m.Origin), Spec: string(m.KeySpec), Usage: string(m.KeyUsage), Manager: string(m.KeyManager),
			State: string(m.KeyState), MultiRegion: aws.ToBool(m.MultiRegion), CustomKeyStore: aws.ToString(m.CustomKeyStoreId),
		},
		checked: r.cfg.Now().UTC().Truncate(time.Second),
	}
	if _, err := kmspolicy.Sealed([]byte(k.policy), k.grants, k.cut, k.facts, image); err != nil {
		return nil, fmt.Errorf("key %s: %w", arn, err)
	}
	return k, nil
}

func (r *Root) recipient() (*types.RecipientInfo, error) {
	doc, err := r.cfg.Attester.AttestPublicKey(r.pub)
	if err != nil {
		return nil, fmt.Errorf("keys: attestation: %w", err)
	}
	return &types.RecipientInfo{AttestationDocument: doc, KeyEncryptionAlgorithm: types.KeyEncryptionMechanismRsaesOaepSha256}, nil
}

func rootContext(image string) map[string]string {
	return map[string]string{"purpose": "root-secret", "image": image}
}

func rootAAD(image, keyARN string) []byte { return envelope.AAD("root-secret", image, keyARN) }

// rootRecord is the stored form of the root secret for one image.
type rootRecord struct {
	Version int `json:"version"`
	// Image is the PCR0 the record is for, and KeyARN that image's KMS key.
	Image  string `json:"image"`
	KeyARN string `json:"key_arn"`
	// Sealed is the root secret, sealed with a data key that KMS wrapped under
	// KeyARN (the record's wrapped key) and gave to the writer encrypted to
	// its attested key.
	Sealed []byte `json:"sealed"`
	// Attestation is the Nitro attestation document of the enclave that wrote
	// the record; its user data is the record's digest.
	Attestation []byte `json:"attestation"`
	// FromPCRs and FromBundle prove that the writer ran a public release: its
	// pcrs.json and the Sigstore bundle for it. Empty when the writer is the
	// image the record is for.
	FromPCRs   []byte `json:"from_pcrs,omitempty"`
	FromBundle []byte `json:"from_bundle,omitempty"`
}

// digest binds everything the reader relies on into the attestation document.
func (rr *rootRecord) digest(wrapped []byte) []byte {
	w, s := sha256.Sum256(wrapped), sha256.Sum256(rr.Sealed)
	d := sha256.Sum256(envelope.AAD("permaily root secret v1", rr.Image, rr.KeyARN, hex.EncodeToString(w[:]), hex.EncodeToString(s[:])))
	return d[:]
}

// seal writes the record that gives secret to image under key. KMS creates
// the data key under the image's key and returns it encrypted to this
// enclave, so neither the root secret nor the data key leaves the enclave in
// the clear.
func (r *Root) seal(ctx context.Context, secret []byte, image string, key *releaseKey, fromPCRs, fromBundle []byte) (*store.Record, error) {
	rec, err := r.recipient()
	if err != nil {
		return nil, err
	}
	out, err := r.cfg.API.GenerateDataKey(ctx, &kms.GenerateDataKeyInput{
		KeyId: aws.String(key.arn), KeySpec: types.DataKeySpecAes256, EncryptionContext: rootContext(image), Recipient: rec,
	})
	if err != nil {
		return nil, fmt.Errorf("keys: generate data key: %w", err)
	}
	if aws.ToString(out.KeyId) != key.arn {
		return nil, ErrKeyMismatch
	}
	dk, err := decryptEnvelopedData(out.CiphertextForRecipient, r.rsa)
	if err != nil {
		return nil, err
	}
	sealed, err := envelope.Seal(dk, secret, rootAAD(image, key.arn))
	if err != nil {
		return nil, err
	}
	rr := rootRecord{Version: 1, Image: image, KeyARN: key.arn, Sealed: sealed, FromPCRs: fromPCRs, FromBundle: fromBundle}
	if rr.Attestation, err = r.cfg.Attester.Attest(nil, rr.digest(out.CiphertextBlob)); err != nil {
		return nil, fmt.Errorf("keys: attestation: %w", err)
	}
	value, err := json.Marshal(rr)
	if err != nil {
		return nil, err
	}
	return &store.Record{Name: RootRecordName(image), WrappedKey: out.CiphertextBlob, Value: value, UpdatedAt: r.cfg.Now().UTC()}, nil
}

// create starts a deployment: a new root secret for this image.
func (r *Root) create(ctx context.Context, me measured) (*store.Record, error) {
	key, err := r.sealedKey(ctx, ReleaseKeyAlias(me.image), me.image)
	if err != nil {
		return nil, err
	}
	secret := make([]byte, rootSecretSize)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	rec, err := r.seal(ctx, secret, me.image, key, nil, nil)
	if err != nil {
		return nil, err
	}
	switch err := r.cfg.Records.PutRecord(ctx, rec, true); {
	case err == nil:
		r.cfg.Log.Info("root secret: created")
		return rec, nil
	case errors.Is(err, store.ErrExists):
		// Another enclave of this image created it first: use that one.
		return r.cfg.Records.GetRecord(ctx, rec.Name)
	default:
		return nil, err
	}
}

// open checks a root record and returns the root secret in it.
func (r *Root) open(ctx context.Context, me measured, rec *store.Record) ([]byte, attestation.RootStatus, error) {
	var none attestation.RootStatus
	var rr rootRecord
	if err := json.Unmarshal(rec.Value, &rr); err != nil || rr.Version != 1 {
		return nil, none, fmt.Errorf("%w: unreadable record", ErrRootRecord)
	}
	if rr.Image != me.image {
		return nil, none, fmt.Errorf("%w: written for image %s", ErrRootRecord, rr.Image)
	}
	// The record names the key; the key is only believed after it is checked.
	key, err := r.sealedKey(ctx, rr.KeyARN, me.image)
	if err != nil {
		return nil, none, err
	}

	// Who wrote it? The attestation document says which image, and binds the record.
	doc, err := attestation.Verify(rr.Attestation, attestation.Options{
		Root: r.cfg.AttestationRoot, Now: r.cfg.Now(), MaxAge: math.MaxInt64, UserData: rr.digest(rec.WrappedKey),
	})
	if err != nil {
		return nil, none, fmt.Errorf("%w: %w", ErrRootRecord, err)
	}
	st := attestation.RootStatus{
		Image: me.image, KeyARN: key.arn, Policy: key.policy, Grants: key.grants, GrantsTruncated: key.cut, Key: key.facts,
		CheckedAt: key.checked, WrittenAt: doc.Timestamp.UTC().Truncate(time.Second), Origin: attestation.RootCreated,
	}
	if !samePCRs(doc.PCRs, me.pcrs) {
		rel, err := provenance.Verify(r.cfg.Releases, r.trustRoot(), rr.FromBundle, rr.FromPCRs)
		if err != nil {
			return nil, none, fmt.Errorf("%w: %w", ErrRootRecord, err)
		}
		if !samePCRs(doc.PCRs, rel.PCRs) {
			return nil, none, fmt.Errorf("%w: written by image %x, not by the release it names", ErrRootRecord, doc.PCRs[0])
		}
		st.Origin = attestation.RootHandedOver
		st.From = &attestation.RootFrom{Image: rel.PCR0, Commit: rel.Commit, PublishedAt: rel.LoggedAt}
	}

	rcp, err := r.recipient()
	if err != nil {
		return nil, none, err
	}
	out, err := r.cfg.API.Decrypt(ctx, &kms.DecryptInput{
		CiphertextBlob: rec.WrappedKey, KeyId: aws.String(key.arn), EncryptionContext: rootContext(me.image), Recipient: rcp,
	})
	if err != nil {
		return nil, none, fmt.Errorf("keys: decrypt root data key: %w", err)
	}
	if aws.ToString(out.KeyId) != key.arn {
		return nil, none, ErrKeyMismatch
	}
	dk, err := decryptEnvelopedData(out.CiphertextForRecipient, r.rsa)
	if err != nil {
		return nil, none, err
	}
	secret, err := envelope.Open(dk, rr.Sealed, rootAAD(me.image, key.arn))
	if err != nil || len(secret) != rootSecretSize {
		return nil, none, fmt.Errorf("%w: the secret does not open", ErrRootRecord)
	}
	return secret, st, nil
}

// Status reports the key that holds the root secret for this image and where
// the secret came from, for /status.
func (r *Root) Status(ctx context.Context) (*attestation.RootStatus, error) {
	if err := r.ready(ctx); err != nil {
		return nil, err
	}
	r.mu.Lock()
	st, watch := r.status, r.watch
	r.mu.Unlock()
	if st.From != nil {
		from := *st.From
		st.From = &from
	}
	// The policy and the grants as they are now, and every state seen since
	// this enclave started.
	now, err := watch.Status(ctx)
	if err != nil {
		return nil, err
	}
	st.Policy, st.Grants, st.GrantsTruncated, st.CheckedAt = now.Policy, now.Grants, now.GrantsTruncated, now.FetchedAt
	st.WatchedSince, st.History, st.HistoryTruncated = now.WatchedSince, now.History, now.HistoryTruncated
	st.Gaps, st.GapsTruncated = now.Gaps, now.GapsTruncated
	return &st, nil
}

// Handoff is a request to give the root secret to another release.
type Handoff struct {
	// ToPCRs and ToBundle are the pcrs.json of the release that will run next
	// and the Sigstore bundle published with it.
	ToPCRs, ToBundle []byte
	// FromPCRs and FromBundle are the same for the release that is running.
	// The next image needs them to check who handed it the secret.
	FromPCRs, FromBundle []byte
}

// HandoffResult describes the release the root secret was given to.
type HandoffResult struct {
	Image       string    `json:"image"`
	Commit      string    `json:"commit"`
	PublishedAt time.Time `json:"published_at"`
	KeyARN      string    `json:"key_arn"`
	// Created is false when that release already had its record.
	Created bool `json:"created"`
}

// HandOff gives the root secret to the release in h: it writes the record
// that only that release's image can open. It refuses anything that is not a
// public release newer than the running one, and any key that is not sealed
// to that release's image for good. Who asks does not matter: the checks do
// not depend on the caller.
func (r *Root) HandOff(ctx context.Context, h Handoff) (*HandoffResult, error) {
	if err := r.ready(ctx); err != nil {
		return nil, err
	}
	me, err := r.measure()
	if err != nil {
		return nil, err
	}
	// This image must be able to prove itself to the next one.
	// One trust root for the whole decision, even if it is updated meanwhile.
	trust := r.trustRoot()
	own, err := provenance.Verify(r.cfg.Releases, trust, h.FromBundle, h.FromPCRs)
	if err != nil {
		return nil, fmt.Errorf("the running release: %w", err)
	}
	if !samePCRs(own.PCRs, me.pcrs) {
		return nil, ErrNotThisImage
	}
	next, err := provenance.Verify(r.cfg.Releases, trust, h.ToBundle, h.ToPCRs)
	if err != nil {
		return nil, fmt.Errorf("the next release: %w", err)
	}
	// Never back to an older release: one with a known flaw would be a way in.
	if !next.LoggedAt.After(own.LoggedAt) {
		return nil, fmt.Errorf("%w: %s, running %s", ErrDowngrade, next.LoggedAt.Format(time.RFC3339), own.LoggedAt.Format(time.RFC3339))
	}
	res := &HandoffResult{Image: next.PCR0, Commit: next.Commit, PublishedAt: next.LoggedAt}

	name := RootRecordName(next.PCR0)
	if rec, err := r.cfg.Records.GetRecord(ctx, name); err == nil {
		var rr rootRecord
		_ = json.Unmarshal(rec.Value, &rr)
		res.KeyARN = rr.KeyARN
		return res, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	key, err := r.sealedKey(ctx, ReleaseKeyAlias(next.PCR0), next.PCR0)
	if err != nil {
		return nil, err
	}
	res.KeyARN = key.arn
	r.mu.Lock()
	secret := bytes.Clone(r.secret)
	r.mu.Unlock()
	rec, err := r.seal(ctx, secret, next.PCR0, key, h.FromPCRs, h.FromBundle)
	if err != nil {
		return nil, err
	}
	switch err := r.cfg.Records.PutRecord(ctx, rec, true); {
	case err == nil:
		res.Created = true
		r.cfg.Log.Info("root secret: handed over", "image", next.PCR0, "commit", next.Commit, "key", key.arn)
	case !errors.Is(err, store.ErrExists):
		return nil, err
	}
	return res, nil
}

func (r *Root) wrappingKey(ctx context.Context) ([]byte, error) {
	if err := r.ready(ctx); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.kek, nil
}

// CommitKey returns the key that authenticates the commits of the records
// that must not be put back to an earlier copy (store.S3Commits). It is
// derived from the root secret, so every image of the deployment has the same
// one and nothing outside an enclave has it.
func (r *Root) CommitKey(ctx context.Context) ([]byte, error) {
	if err := r.ready(ctx); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return hkdf.Key(sha256.New, r.secret, nil, "permaily record commits v1", sha256.Size)
}

func (r *Root) wrap(kek, dk []byte, c map[string]string) ([]byte, error) {
	sealed, err := envelope.Seal(kek, dk, contextAAD(c))
	if err != nil {
		return nil, err
	}
	return append([]byte(wrapMagic), sealed...), nil
}

// NewDataKey implements Service: a random data key, wrapped under the root secret.
func (r *Root) NewDataKey(ctx context.Context, c map[string]string) (DataKey, error) {
	kek, err := r.wrappingKey(ctx)
	if err != nil {
		return DataKey{}, err
	}
	dk := make([]byte, envelope.KeySize)
	if _, err := rand.Read(dk); err != nil {
		return DataKey{}, err
	}
	wrapped, err := r.wrap(kek, dk, c)
	if err != nil {
		return DataKey{}, err
	}
	return DataKey{Plaintext: dk, Wrapped: wrapped}, nil
}

// Unwrap implements Service.
func (r *Root) Unwrap(ctx context.Context, wrapped []byte, c map[string]string) ([]byte, error) {
	kek, err := r.wrappingKey(ctx)
	if err != nil {
		return nil, err
	}
	sealed, ok := bytes.CutPrefix(wrapped, []byte(wrapMagic))
	if !ok {
		return nil, errors.New("keys: a data key that is not wrapped under the root secret")
	}
	return envelope.Open(kek, sealed, contextAAD(c))
}
