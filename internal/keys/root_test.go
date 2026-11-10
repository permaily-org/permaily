package keys

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"

	"github.com/permaily-org/permaily/internal/nsm/fakensm"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/pkg/attestation"
	"github.com/permaily-org/permaily/pkg/attestation/attestationtest"
	"github.com/permaily-org/permaily/pkg/kmspolicy"
	"github.com/permaily-org/permaily/pkg/provenance"
	"github.com/permaily-org/permaily/pkg/provenance/provenancetest"
)

const (
	testAccount = "111122223333"
	testRegion  = "us-east-2"
)

// sealedPolicy is the policy of a release key, as deploy/tofu creates it.
func sealedPolicy(image string) string {
	root := `"Principal":{"AWS":"arn:aws:iam::` + testAccount + `:root"}`
	return `{"Version":"2012-10-17","Statement":[
{"Sid":"Read","Effect":"Allow",` + root + `,"Action":["kms:DescribeKey","kms:GetKeyPolicy","kms:ListGrants","kms:ScheduleKeyDeletion"],"Resource":"*"},
{"Sid":"AnyoneLocks","Effect":"Allow",` + root + `,"Action":"kms:GenerateDataKey","Resource":"*"},
{"Sid":"OnlyThisImageOpens","Effect":"Allow",` + root + `,"Action":"kms:Decrypt","Resource":"*",
 "Condition":{"StringEqualsIgnoreCase":{"kms:RecipientAttestation:ImageSha384":"` + image + `"}}}]}`
}

type testKey struct {
	policy string
	grants int
	meta   types.KeyMetadata
}

type blob struct {
	dk  []byte
	arn string
	ctx map[string]string
}

// rootKMS is a KMS that, like the real one, releases data keys only to the
// key an attestation document carries, and decrypts only for the images a
// key's policy names.
type rootKMS struct {
	t  *testing.T
	ca *attestationtest.CA

	mu      sync.Mutex
	keys    map[string]*testKey // by ARN
	aliases map[string]string
	blobs   map[string]blob
	calls   int
	fail    error
	// answerAs makes data key answers claim to come from this key.
	answerAs string
}

func (f *rootKMS) keyID(arn string) *string {
	if f.answerAs != "" {
		return aws.String(f.answerAs)
	}
	return aws.String(arn)
}

func newRootKMS(t *testing.T, ca *attestationtest.CA) *rootKMS {
	return &rootKMS{t: t, ca: ca, keys: map[string]*testKey{}, aliases: map[string]string{}, blobs: map[string]blob{}}
}

// addKey creates a key with the given policy and points the image's alias at it.
func (f *rootKMS) addKey(id, image, policy string) string {
	arn := "arn:aws:kms:" + testRegion + ":" + testAccount + ":key/" + id
	f.keys[arn] = &testKey{policy: policy, meta: types.KeyMetadata{
		Arn: aws.String(arn), KeyId: aws.String(id), Origin: types.OriginTypeAwsKms, KeySpec: types.KeySpecSymmetricDefault,
		KeyUsage: types.KeyUsageTypeEncryptDecrypt, KeyManager: types.KeyManagerTypeCustomer, KeyState: types.KeyStateEnabled,
	}}
	f.aliases[ReleaseKeyAlias(image)] = arn
	return arn
}

func (f *rootKMS) key(id *string) (*testKey, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail != nil {
		return nil, "", f.fail
	}
	arn := aws.ToString(id)
	if a, ok := f.aliases[arn]; ok {
		arn = a
	}
	k, ok := f.keys[arn]
	if !ok {
		return nil, "", &types.NotFoundException{Message: aws.String("no such key")}
	}
	return k, arn, nil
}

func (f *rootKMS) DescribeKey(_ context.Context, in *kms.DescribeKeyInput, _ ...func(*kms.Options)) (*kms.DescribeKeyOutput, error) {
	k, _, err := f.key(in.KeyId)
	if err != nil {
		return nil, err
	}
	m := k.meta
	return &kms.DescribeKeyOutput{KeyMetadata: &m}, nil
}

func (f *rootKMS) GetKeyPolicy(_ context.Context, in *kms.GetKeyPolicyInput, _ ...func(*kms.Options)) (*kms.GetKeyPolicyOutput, error) {
	k, _, err := f.key(in.KeyId)
	if err != nil {
		return nil, err
	}
	return &kms.GetKeyPolicyOutput{Policy: aws.String(k.policy)}, nil
}

func (f *rootKMS) ListGrants(_ context.Context, in *kms.ListGrantsInput, _ ...func(*kms.Options)) (*kms.ListGrantsOutput, error) {
	k, _, err := f.key(in.KeyId)
	if err != nil {
		return nil, err
	}
	return &kms.ListGrantsOutput{Grants: make([]types.GrantListEntry, k.grants)}, nil
}

// recipient checks the attestation document and returns its key and image.
func (f *rootKMS) recipient(r *types.RecipientInfo) (*rsa.PublicKey, string, error) {
	if r == nil {
		return nil, "", errors.New("no recipient")
	}
	doc, err := attestation.Verify(r.AttestationDocument, attestation.Options{Root: f.ca.Root})
	if err != nil {
		return nil, "", err
	}
	pub, err := x509.ParsePKIXPublicKey(doc.PublicKey)
	if err != nil {
		return nil, "", err
	}
	return pub.(*rsa.PublicKey), hex.EncodeToString(doc.PCRs[0]), nil
}

func (f *rootKMS) GenerateDataKey(_ context.Context, in *kms.GenerateDataKeyInput, _ ...func(*kms.Options)) (*kms.GenerateDataKeyOutput, error) {
	_, arn, err := f.key(in.KeyId)
	if err != nil {
		return nil, err
	}
	// Any image may lock something under a key; only its own image opens it.
	pub, _, err := f.recipient(in.Recipient)
	if err != nil {
		return nil, err
	}
	dk, wrapped := make([]byte, 32), make([]byte, 40)
	_, _ = rand.Read(dk)
	_, _ = rand.Read(wrapped)
	f.mu.Lock()
	f.blobs[string(wrapped)] = blob{dk: dk, arn: arn, ctx: maps.Clone(in.EncryptionContext)}
	f.mu.Unlock()
	return &kms.GenerateDataKeyOutput{KeyId: f.keyID(arn), CiphertextBlob: wrapped, CiphertextForRecipient: envelop(f.t, dk, pub, false)}, nil
}

func (f *rootKMS) Decrypt(_ context.Context, in *kms.DecryptInput, _ ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	k, arn, err := f.key(in.KeyId)
	if err != nil {
		return nil, err
	}
	pub, image, err := f.recipient(in.Recipient)
	if err != nil {
		return nil, err
	}
	rep, err := kmspolicy.Analyze([]byte(k.policy))
	if err != nil || !strings.Contains(strings.Join(rep.Images, ","), image) {
		return nil, &types.KMSInvalidStateException{Message: aws.String("AccessDenied: the image is not allowed")}
	}
	f.mu.Lock()
	b, ok := f.blobs[string(in.CiphertextBlob)]
	f.mu.Unlock()
	if !ok || b.arn != arn || !maps.Equal(b.ctx, in.EncryptionContext) {
		return nil, &types.InvalidCiphertextException{Message: aws.String("invalid ciphertext")}
	}
	return &kms.DecryptOutput{KeyId: f.keyID(arn), CiphertextForRecipient: envelop(f.t, b.dk, pub, true)}, nil
}

// world is one deployment: a KMS, a store, the hardware that signs
// attestation documents, and the authority that signs releases.
type world struct {
	t        *testing.T
	ca       *attestationtest.CA
	kms      *rootKMS
	store    *store.Memory
	releases *provenancetest.Authority
	policy   provenance.Policy
	now      time.Time
}

func newWorld(t *testing.T) *world {
	t.Helper()
	ca, err := attestationtest.NewCA()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := provenancetest.New()
	if err != nil {
		t.Fatal(err)
	}
	return &world{
		t: t, ca: ca, kms: newRootKMS(t, ca), store: store.NewMemory(), releases: rel, now: time.Now(),
		policy: provenance.Policy{Repository: "permaily-org/permaily", RepositoryID: "1", OwnerID: "2", Workflow: ".github/workflows/release.yml", Ref: "refs/heads/main"},
	}
}

// image is one enclave image: its measurements, its key and its release proof.
type image struct {
	pcrs    map[uint][]byte
	pcr0    string
	keyARN  string
	pcrFile []byte
	bundle  []byte
}

// release publishes an image: a KMS key sealed to it and a signed bundle,
// logged age before now.
func (w *world) release(fill byte, age time.Duration) *image {
	w.t.Helper()
	im := w.private(fill)
	im.keyARN = w.kms.addKey(fmt.Sprintf("key-%02x", fill), im.pcr0, sealedPolicy(im.pcr0))
	im.bundle = w.attest(im, func(b *provenancetest.Build) { b.At = w.now.Add(-age) })
	return im
}

// private is an image nobody published.
func (w *world) private(fill byte) *image {
	pcrs := attestationtest.PCRs(fill)
	im := &image{pcrs: pcrs, pcr0: hex.EncodeToString(pcrs[0])}
	im.pcrFile = fmt.Appendf(nil, `{"PCR0":"%x","PCR1":"%x","PCR2":"%x"}`, pcrs[0], pcrs[1], pcrs[2])
	return im
}

func (w *world) attest(im *image, change func(b *provenancetest.Build)) []byte {
	w.t.Helper()
	b := provenancetest.Build{
		Repository: w.policy.Repository, RepositoryID: w.policy.RepositoryID, OwnerID: w.policy.OwnerID,
		Workflow: w.policy.Workflow, Ref: w.policy.Ref, Commit: strings.Repeat(im.pcr0[:2], 20),
	}
	change(&b)
	bundle, err := w.releases.Attest(b, im.pcrFile)
	if err != nil {
		w.t.Fatal(err)
	}
	return bundle
}

// boot starts an enclave of the image: a new Root with nothing in memory.
func (w *world) boot(im *image) *Root {
	w.t.Helper()
	r, err := NewRoot(RootConfig{
		API: w.kms, Attester: &fakensm.Fake{CA: w.ca, PCRs: im.pcrs}, Records: w.store,
		Account: testAccount, Region: testRegion, Releases: w.policy, Trust: w.releases.TrustRoot(), AttestationRoot: w.ca.Root,
		Now: func() time.Time { return w.now },
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return r
}

func (w *world) handoff(from *Root, a, b *image) (*HandoffResult, error) {
	return from.HandOff(context.Background(), Handoff{ToPCRs: b.pcrFile, ToBundle: b.bundle, FromPCRs: a.pcrFile, FromBundle: a.bundle})
}

var keyCtx = map[string]string{"purpose": "mailbox", "mailbox": "m1"}

func TestRootCreatesAndReloads(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	a := w.release(0xa1, time.Hour)

	first := w.boot(a)
	dk, err := first.NewDataKey(ctx, keyCtx)
	if err != nil {
		t.Fatal(err)
	}
	if len(dk.Plaintext) != 32 || !bytes.HasPrefix(dk.Wrapped, []byte(wrapMagic)) || bytes.Contains(dk.Wrapped, dk.Plaintext) {
		t.Fatalf("data key %+v", dk)
	}
	st, err := first.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Image != a.pcr0 || st.KeyARN != a.keyARN || st.Origin != attestation.RootCreated || st.From != nil || st.Policy != sealedPolicy(a.pcr0) || st.Key.Origin != "AWS_KMS" || st.WrittenAt.IsZero() {
		t.Fatalf("status %+v", st)
	}
	if _, err := kmspolicy.Sealed([]byte(st.Policy), st.Grants, st.GrantsTruncated, st.Key, st.Image); err != nil {
		t.Fatalf("a verifier would refuse the reported key: %v", err)
	}

	// A restart: nothing in memory, the same secret from the record.
	second := w.boot(a)
	got, err := second.Unwrap(ctx, dk.Wrapped, keyCtx)
	if err != nil || !bytes.Equal(got, dk.Plaintext) {
		t.Fatalf("after a restart: %x, %v", got, err)
	}
	// The wrapping is bound to its context.
	if _, err := second.Unwrap(ctx, dk.Wrapped, map[string]string{"purpose": "mailbox", "mailbox": "m2"}); err == nil {
		t.Fatal("unwrapped under another context")
	}
	// Outside the enclave the root secret is nowhere in the clear.
	rec, err := w.store.GetRecord(ctx, RootRecordName(a.pcr0))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(rec.Value, first.secret) || bytes.Contains(rec.WrappedKey, first.secret) {
		t.Fatal("the record holds the root secret in the clear")
	}
}

func TestRootHandoff(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	a, b := w.release(0xa1, 2*time.Hour), w.release(0xb2, time.Hour)

	old := w.boot(a)
	dk, err := old.NewDataKey(ctx, keyCtx)
	if err != nil {
		t.Fatal(err)
	}

	// The next image, started before the handoff, waits. It must not start a
	// second deployment with a secret of its own.
	early := w.boot(b)
	if _, err := early.NewDataKey(ctx, keyCtx); !errors.Is(err, ErrAwaitingHandoff) {
		t.Fatalf("before the handoff: %v", err)
	}

	res, err := w.handoff(old, a, b)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created || res.Image != b.pcr0 || res.KeyARN != b.keyARN || res.Commit == "" {
		t.Fatalf("result %+v", res)
	}
	// Asking again changes nothing.
	if again, err := w.handoff(old, a, b); err != nil || again.Created || again.KeyARN != b.keyARN {
		t.Fatalf("second handoff: %+v, %v", again, err)
	}

	// The failure is remembered for a few seconds, then the image finds its record.
	if _, err := early.NewDataKey(ctx, keyCtx); !errors.Is(err, ErrAwaitingHandoff) {
		t.Fatalf("within the retry interval: %v", err)
	}
	w.now = w.now.Add(rootRetry)
	got, err := early.Unwrap(ctx, dk.Wrapped, keyCtx)
	if err != nil || !bytes.Equal(got, dk.Plaintext) {
		t.Fatalf("the next image cannot open what the old one stored: %v", err)
	}
	st, err := early.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Origin != attestation.RootHandedOver || st.From == nil || st.From.Image != a.pcr0 || st.KeyARN != b.keyARN || st.Image != b.pcr0 {
		t.Fatalf("status %+v from %+v", st, st.From)
	}
	// The old image still opens its own record: that is the rollback.
	if _, err := w.boot(a).Unwrap(ctx, dk.Wrapped, keyCtx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	// And the chain goes on from the new image.
	c := w.release(0xc3, time.Minute)
	if _, err := w.handoff(early, b, c); err != nil {
		t.Fatal(err)
	}
	if got, err := w.boot(c).Unwrap(ctx, dk.Wrapped, keyCtx); err != nil || !bytes.Equal(got, dk.Plaintext) {
		t.Fatalf("third image: %v", err)
	}
}

// Everything an operator could ask the running enclave for.
func TestHandoffRefuses(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	a := w.release(0xa1, 2*time.Hour)
	running := w.boot(a)
	if err := running.ready(ctx); err != nil {
		t.Fatal(err)
	}
	newer := func(fill byte) *image { return w.release(fill, time.Hour) }

	t.Run("an image nobody published", func(t *testing.T) {
		p := w.private(0xee)
		w.kms.addKey("private", p.pcr0, sealedPolicy(p.pcr0))
		// The operator has no bundle for it; the closest thing is another release's.
		p.bundle = newer(0xb0).bundle
		if _, err := w.handoff(running, a, p); !errors.Is(err, provenance.ErrNotARelease) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("an image built by another workflow of the repository", func(t *testing.T) {
		p := w.private(0xe1)
		w.kms.addKey("ci", p.pcr0, sealedPolicy(p.pcr0))
		p.bundle = w.attest(p, func(b *provenancetest.Build) { b.Workflow = ".github/workflows/ci.yml" })
		if _, err := w.handoff(running, a, p); !errors.Is(err, provenance.ErrNotARelease) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("an image built on another branch", func(t *testing.T) {
		p := w.private(0xe2)
		w.kms.addKey("branch", p.pcr0, sealedPolicy(p.pcr0))
		p.bundle = w.attest(p, func(b *provenancetest.Build) { b.Ref = "refs/heads/backdoor" })
		if _, err := w.handoff(running, a, p); !errors.Is(err, provenance.ErrNotARelease) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("an image signed by someone else", func(t *testing.T) {
		other, err := provenancetest.New()
		if err != nil {
			t.Fatal(err)
		}
		p := w.private(0xe3)
		w.kms.addKey("forged", p.pcr0, sealedPolicy(p.pcr0))
		p.bundle, err = other.Attest(provenancetest.Build{Repository: w.policy.Repository, RepositoryID: "1", OwnerID: "2", Workflow: w.policy.Workflow, Ref: w.policy.Ref}, p.pcrFile)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.handoff(running, a, p); !errors.Is(err, provenance.ErrUntrusted) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("an older release", func(t *testing.T) {
		old := w.release(0x90, 3*time.Hour)
		if _, err := w.handoff(running, a, old); !errors.Is(err, ErrDowngrade) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("itself", func(t *testing.T) {
		if _, err := w.handoff(running, a, a); !errors.Is(err, ErrDowngrade) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a proof of the running image that is about another", func(t *testing.T) {
		b := newer(0xb1)
		if _, err := w.handoff(running, b, newer(0xb2)); !errors.Is(err, ErrNotThisImage) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("no proof of the running image", func(t *testing.T) {
		b := newer(0xb3)
		if _, err := running.HandOff(ctx, Handoff{ToPCRs: b.pcrFile, ToBundle: b.bundle}); !errors.Is(err, provenance.ErrMalformed) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a release without a key", func(t *testing.T) {
		b := newer(0xb4)
		delete(w.kms.aliases, ReleaseKeyAlias(b.pcr0))
		if _, err := w.handoff(running, a, b); err == nil || !strings.Contains(err.Error(), "no such key") {
			t.Fatalf("got %v", err)
		}
	})

	// A key the root secret would not be safe under.
	admin := `{"Sid":"Admin","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::` + testAccount + `:root"},"Action":"kms:PutKeyPolicy","Resource":"*"}`
	door := `{"Sid":"Door","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::` + testAccount + `:user/op"},"Action":"kms:Decrypt","Resource":"*"}`
	with := func(extra string) func(im *image) string {
		return func(im *image) string {
			return strings.Replace(sealedPolicy(im.pcr0), `"Statement":[`, `"Statement":[`+extra+`,`, 1)
		}
	}
	for name, c := range map[string]struct {
		policy func(im *image) string
		change func(k *testKey)
		want   string
	}{
		"a policy that can be replaced":     {with(admin), nil, "can be replaced"},
		"someone decrypts without an image": {with(door), nil, "statement Door"},
		"a key for another image":           {func(*image) string { return sealedPolicy(strings.Repeat("ab", 48)) }, nil, "not released to this image"},
		"a key for two images": {func(im *image) string {
			return strings.Replace(sealedPolicy(im.pcr0), `:"`+im.pcr0+`"`, `:["`+im.pcr0+`","`+strings.Repeat("ab", 48)+`"]`, 1)
		}, nil, "released to other images"},
		"a grant on the key":    {nil, func(k *testKey) { k.grants = 1 }, "grant"},
		"imported key material": {nil, func(k *testKey) { k.meta.Origin = types.OriginTypeExternal }, "outside KMS"},
		"a multi-Region key":    {nil, func(k *testKey) { k.meta.MultiRegion = aws.Bool(true) }, "multi-Region"},
	} {
		t.Run(name, func(t *testing.T) {
			b := newer(byte(0x40 + len(w.kms.keys)))
			k := w.kms.keys[b.keyARN]
			if c.policy != nil {
				k.policy = c.policy(b)
			}
			if c.change != nil {
				c.change(k)
			}
			if _, err := w.handoff(running, a, b); !errors.Is(err, kmspolicy.ErrNotSealed) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v", err)
			}
			if _, err := w.store.GetRecord(ctx, RootRecordName(b.pcr0)); !errors.Is(err, store.ErrNotFound) {
				t.Fatal("a record was written")
			}
		})
	}
	t.Run("a key in another account", func(t *testing.T) {
		b := newer(0xb5)
		arn := "arn:aws:kms:" + testRegion + ":999999999999:key/elsewhere"
		k := *w.kms.keys[b.keyARN]
		k.meta.Arn = aws.String(arn)
		w.kms.keys[arn], w.kms.aliases[ReleaseKeyAlias(b.pcr0)] = &k, arn
		if _, err := w.handoff(running, a, b); !errors.Is(err, ErrKeyAccount) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a key in another region", func(t *testing.T) {
		b := newer(0xb6)
		arn := "arn:aws:kms:eu-west-1:" + testAccount + ":key/elsewhere"
		k := *w.kms.keys[b.keyARN]
		k.meta.Arn = aws.String(arn)
		w.kms.keys[arn], w.kms.aliases[ReleaseKeyAlias(b.pcr0)] = &k, arn
		if _, err := w.handoff(running, a, b); !errors.Is(err, ErrKeyAccount) {
			t.Fatalf("got %v", err)
		}
	})
}

// Everything an operator could put in the store for an image to find.
func TestRootRefusesPlantedRecords(t *testing.T) {
	ctx := context.Background()
	// setup: release A runs and hands over to release B. The operator also
	// runs a private image, on the same hardware, that knows a secret.
	setup := func(t *testing.T) (w *world, a, b, private *image, rogue *Root) {
		w = newWorld(t)
		a, b = w.release(0xa1, 2*time.Hour), w.release(0xb2, time.Hour)
		private = w.private(0xee)
		if err := w.boot(a).ready(ctx); err != nil {
			t.Fatal(err)
		}
		return w, a, b, private, w.boot(private)
	}
	known := bytes.Repeat([]byte{7}, rootSecretSize)
	// plant writes, with the private image, a record for b that holds the known secret.
	plant := func(t *testing.T, w *world, rogue *Root, b *image, fromPCRs, fromBundle []byte) {
		t.Helper()
		key, err := rogue.sealedKey(ctx, b.keyARN, b.pcr0)
		if err != nil {
			t.Fatal(err)
		}
		rec, err := rogue.seal(ctx, known, b.pcr0, key, fromPCRs, fromBundle)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.store.PutRecord(ctx, rec, false); err != nil {
			t.Fatal(err)
		}
	}
	refused := func(t *testing.T, w *world, b *image, want error) {
		t.Helper()
		r := w.boot(b)
		if _, err := r.NewDataKey(ctx, keyCtx); !errors.Is(err, want) {
			t.Fatalf("got %v, want %v", err, want)
		}
		if r.secret != nil {
			t.Fatal("the image took a secret")
		}
	}

	t.Run("a secret the operator knows, written by a private image", func(t *testing.T) {
		w, _, b, _, rogue := setup(t)
		plant(t, w, rogue, b, nil, nil)
		refused(t, w, b, ErrRootRecord)
	})
	t.Run("the same, with the proof of a real release attached", func(t *testing.T) {
		w, a, b, _, rogue := setup(t)
		plant(t, w, rogue, b, a.pcrFile, a.bundle)
		refused(t, w, b, ErrRootRecord)
	})
	t.Run("the same, with a proof made for the private image by another workflow", func(t *testing.T) {
		w, _, b, private, rogue := setup(t)
		plant(t, w, rogue, b, private.pcrFile, w.attest(private, func(pb *provenancetest.Build) { pb.Workflow = ".github/workflows/ci.yml" }))
		refused(t, w, b, ErrRootRecord)
	})
	t.Run("a record written for another image", func(t *testing.T) {
		w, a, b, _, _ := setup(t)
		rec, err := w.store.GetRecord(ctx, RootRecordName(a.pcr0))
		if err != nil {
			t.Fatal(err)
		}
		rec.Name = RootRecordName(b.pcr0)
		if err := w.store.PutRecord(ctx, rec, false); err != nil {
			t.Fatal(err)
		}
		refused(t, w, b, ErrRootRecord)
	})

	// Changes to a real record.
	for name, change := range map[string]func(rec *store.Record, rr *rootRecord, w *world, b *image){
		"the sealed secret":  func(_ *store.Record, rr *rootRecord, _ *world, _ *image) { rr.Sealed[len(rr.Sealed)-1] ^= 1 },
		"the wrapped key":    func(rec *store.Record, _ *rootRecord, _ *world, _ *image) { rec.WrappedKey[0] ^= 1 },
		"the attestation":    func(_ *store.Record, rr *rootRecord, _ *world, _ *image) { rr.Attestation[len(rr.Attestation)-1] ^= 1 },
		"no attestation":     func(_ *store.Record, rr *rootRecord, _ *world, _ *image) { rr.Attestation = nil },
		"no release proof":   func(_ *store.Record, rr *rootRecord, _ *world, _ *image) { rr.FromBundle = nil },
		"an unknown version": func(_ *store.Record, rr *rootRecord, _ *world, _ *image) { rr.Version = 2 },
		"the proof of another release": func(_ *store.Record, rr *rootRecord, w *world, b *image) {
			rr.FromPCRs, rr.FromBundle = b.pcrFile, b.bundle
		},
	} {
		t.Run("a real record with a change: "+name, func(t *testing.T) {
			w, a, b, _, _ := setup(t)
			if _, err := w.handoff(w.boot(a), a, b); err != nil {
				t.Fatal(err)
			}
			rec, err := w.store.GetRecord(ctx, RootRecordName(b.pcr0))
			if err != nil {
				t.Fatal(err)
			}
			var rr rootRecord
			if err := json.Unmarshal(rec.Value, &rr); err != nil {
				t.Fatal(err)
			}
			change(rec, &rr, w, b)
			if rec.Value, err = json.Marshal(rr); err != nil {
				t.Fatal(err)
			}
			if err := w.store.PutRecord(ctx, rec, false); err != nil {
				t.Fatal(err)
			}
			refused(t, w, b, ErrRootRecord)
		})
	}
	t.Run("a real record that names a key the operator can open", func(t *testing.T) {
		w, a, b, _, _ := setup(t)
		if _, err := w.handoff(w.boot(a), a, b); err != nil {
			t.Fatal(err)
		}
		w.kms.keys[b.keyARN].policy = strings.Replace(sealedPolicy(b.pcr0), `"kms:ScheduleKeyDeletion"`, `"kms:ScheduleKeyDeletion","kms:PutKeyPolicy"`, 1)
		refused(t, w, b, kmspolicy.ErrNotSealed)
	})
	t.Run("not a record", func(t *testing.T) {
		w, _, b, _, _ := setup(t)
		if err := w.store.PutRecord(ctx, &store.Record{Name: RootRecordName(b.pcr0), WrappedKey: []byte{1}, Value: []byte("nope")}, false); err != nil {
			t.Fatal(err)
		}
		refused(t, w, b, ErrRootRecord)
	})
}

func TestRootInDebugMode(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	debug := &image{pcrs: attestationtest.PCRs(0)}
	r := w.boot(debug)
	if _, err := r.NewDataKey(ctx, keyCtx); !errors.Is(err, ErrDebugMode) {
		t.Fatalf("got %v", err)
	}
	if _, err := r.Status(ctx); !errors.Is(err, ErrDebugMode) {
		t.Fatalf("status: %v", err)
	}
	if _, err := r.HandOff(ctx, Handoff{}); !errors.Is(err, ErrDebugMode) {
		t.Fatalf("handoff: %v", err)
	}
	if w.kms.calls != 0 {
		t.Fatal("a debug enclave asked KMS for something")
	}
}

// Nobody in the account can change the policy of the key, but AWS can for the
// account's owner. The enclave keeps reading it and reports every state.
func TestRootWatchesItsKey(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	a := w.release(0xa1, time.Hour)
	r := w.boot(a)
	st, err := r.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !st.WatchedSince.Equal(w.now.UTC().Truncate(time.Second)) || len(st.History) != 1 || st.History[0].Policy != sealedPolicy(a.pcr0) || len(st.Gaps) != 0 {
		t.Fatalf("status %+v", st)
	}

	// The policy is opened: the account's administrators may now decrypt.
	opened := strings.Replace(sealedPolicy(a.pcr0), `"Statement":[`, `"Statement":[{"Sid":"Door","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::`+testAccount+`:root"},"Action":"kms:*","Resource":"*"},`, 1)
	w.kms.keys[a.keyARN].policy = opened
	w.now = w.now.Add(policyInterval)
	st, err = r.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Policy != opened || len(st.History) != 2 || st.History[0].Policy != sealedPolicy(a.pcr0) || st.History[1].Policy != opened || !st.CheckedAt.Equal(w.now.UTC().Truncate(time.Second)) {
		t.Fatalf("after the change: %+v", st)
	}
	// And closed again: the open state stays in the history, with its times.
	w.kms.keys[a.keyARN].policy = sealedPolicy(a.pcr0)
	w.now = w.now.Add(policyInterval)
	st, err = r.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Policy != sealedPolicy(a.pcr0) || len(st.History) != 3 || st.History[1].Policy != opened {
		t.Fatalf("after closing it: %+v", st.History)
	}
	if _, err := kmspolicy.Sealed([]byte(st.History[1].Policy), st.History[1].Grants, st.History[1].GrantsTruncated, st.Key, st.Image); !errors.Is(err, kmspolicy.ErrNotSealed) {
		t.Fatalf("a verifier would accept the open state: %v", err)
	}

	// A grant shows too.
	w.kms.keys[a.keyARN].grants = 1
	w.now = w.now.Add(policyInterval)
	if st, err = r.Status(ctx); err != nil || st.Grants != 1 || len(st.History) != 4 {
		t.Fatalf("after a grant: %+v, %v", st, err)
	}

	// While KMS cannot be reached the enclave does not vouch for its key.
	w.kms.fail = errors.New("kms unreachable")
	w.now = w.now.Add(policyInterval)
	if _, err := r.Status(ctx); err == nil {
		t.Fatal("reported a status without reading the key")
	}
	w.kms.fail = nil
	w.now = w.now.Add(policyInterval)
	if st, err = r.Status(ctx); err != nil || len(st.Gaps) != 1 {
		t.Fatalf("after the outage: %+v, %v", st, err)
	}
	// The caller cannot change what the enclave remembers.
	st.History[0].Policy = "changed"
	if again, _ := r.Status(ctx); again.History[0].Policy == "changed" {
		t.Fatal("the history is shared with the caller")
	}
}

// A data key that was not wrapped under the root secret does not open.
func TestRootRefusesOtherWrappings(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	r := w.boot(w.release(0xa1, time.Hour))
	for _, wrapped := range [][]byte{nil, []byte("not a wrapped key"), []byte(wrapMagic), append([]byte(wrapMagic), make([]byte, 80)...)} {
		if _, err := r.Unwrap(ctx, wrapped, keyCtx); err == nil {
			t.Fatalf("unwrapped %q", wrapped)
		}
	}
}

func TestRootRunWaitsForKMS(t *testing.T) {
	w := newWorld(t)
	a := w.release(0xa1, time.Hour)
	w.kms.fail = errors.New("kms unreachable")
	r := w.boot(a)
	// The clock stands still in the test world; use the real one, and retry fast.
	r.cfg.Now, r.retry = time.Now, 10*time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond)
	if r.ready(ctx) == nil {
		t.Fatal("loaded while KMS failed")
	}
	w.kms.mu.Lock()
	w.kms.fail = nil
	w.kms.mu.Unlock()
	for r.ready(ctx) != nil {
		if ctx.Err() != nil {
			t.Fatal("Run did not load the root secret after KMS came back")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := r.NewDataKey(ctx, keyCtx); err != nil {
		t.Fatal(err)
	}
	// Once loaded, Run keeps watching the key until its context ends.
	select {
	case <-done:
		t.Fatal("Run stopped watching the key")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	<-done

	// Run gives up when its context ends.
	stuck := w.boot(&image{pcrs: attestationtest.PCRs(0)})
	short, stop := context.WithCancel(context.Background())
	stop()
	stuck.Run(short)
}

// The host supplies the AWS credentials, so every answer names its key.
func TestRootChecksWhichKeyAnswered(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	a, b := w.release(0xa1, 2*time.Hour), w.release(0xb2, time.Hour)
	other := "arn:aws:kms:" + testRegion + ":" + testAccount + ":key/other"

	w.kms.answerAs = other
	if _, err := w.boot(a).NewDataKey(ctx, keyCtx); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("creating: %v", err)
	}
	w.kms.answerAs = ""
	running := w.boot(a)
	if err := running.ready(ctx); err != nil {
		t.Fatal(err)
	}
	w.kms.answerAs = other
	if _, err := w.handoff(running, a, b); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("handing over: %v", err)
	}
	if _, err := w.boot(a).NewDataKey(ctx, keyCtx); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("opening: %v", err)
	}

	// A record that names a key other than the one that wrapped it does not open.
	w.kms.answerAs = ""
	if _, err := w.handoff(running, a, b); err != nil {
		t.Fatal(err)
	}
	rec, err := w.store.GetRecord(ctx, RootRecordName(b.pcr0))
	if err != nil {
		t.Fatal(err)
	}
	c := w.release(0xc3, time.Minute)
	w.kms.keys[c.keyARN].policy = sealedPolicy(b.pcr0) // another key of image b
	rec.Value = bytes.Replace(rec.Value, []byte(b.keyARN), []byte(c.keyARN), 1)
	if err := w.store.PutRecord(ctx, rec, false); err != nil {
		t.Fatal(err)
	}
	if _, err := w.boot(b).NewDataKey(ctx, keyCtx); !errors.Is(err, ErrRootRecord) {
		t.Fatalf("a record moved to another key: %v", err)
	}
}

// failingRecords fails every read, or every write, with an error of the store.
type failingRecords struct {
	store.Records
	get, put error
}

func (f failingRecords) GetRecord(ctx context.Context, name string) (*store.Record, error) {
	if f.get != nil {
		return nil, f.get
	}
	return f.Records.GetRecord(ctx, name)
}

func (f failingRecords) PutRecord(ctx context.Context, r *store.Record, create bool) error {
	if f.put != nil {
		return f.put
	}
	return f.Records.PutRecord(ctx, r, create)
}

// A store that fails must never look like an empty one: the image would start
// a new deployment over the real one.
func TestRootDoesNotCreateWhenTheStoreFails(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	a, b := w.release(0xa1, 2*time.Hour), w.release(0xb2, time.Hour)
	down := errors.New("dynamodb unreachable")

	r := w.boot(a)
	r.cfg.Records = failingRecords{Records: w.store, get: down}
	if _, err := r.NewDataKey(ctx, keyCtx); !errors.Is(err, down) {
		t.Fatalf("got %v", err)
	}
	r = w.boot(a)
	r.cfg.Records = failingRecords{Records: w.store, put: down}
	if _, err := r.NewDataKey(ctx, keyCtx); !errors.Is(err, down) || r.secret != nil {
		t.Fatalf("got %v", err)
	}
	if w.kms.calls == 0 {
		t.Fatal("expected the key to be read")
	}

	running := w.boot(a)
	if err := running.ready(ctx); err != nil {
		t.Fatal(err)
	}
	running.cfg.Records = failingRecords{Records: w.store, put: down}
	if _, err := w.handoff(running, a, b); !errors.Is(err, down) {
		t.Fatalf("handoff: %v", err)
	}
	running.cfg.Records = failingRecords{Records: w.store, get: down}
	if _, err := w.handoff(running, a, b); !errors.Is(err, down) {
		t.Fatalf("handoff: %v", err)
	}
}

// An image whose key refuses it (a record copied to another image's name
// would look like this) gets nothing.
func TestRootNeedsItsOwnKey(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	a, b := w.release(0xa1, 2*time.Hour), w.release(0xb2, time.Hour)
	running := w.boot(a)
	if _, err := w.handoff(running, a, b); err != nil {
		t.Fatal(err)
	}
	// The record for b, offered to a private image under b's name.
	private := w.private(0xee)
	rec, err := w.store.GetRecord(ctx, RootRecordName(b.pcr0))
	if err != nil {
		t.Fatal(err)
	}
	rec.Name = RootRecordName(private.pcr0)
	rec.Value = bytes.ReplaceAll(rec.Value, []byte(b.pcr0), []byte(private.pcr0))
	if err := w.store.PutRecord(ctx, rec, false); err != nil {
		t.Fatal(err)
	}
	r := w.boot(private)
	if _, err := r.NewDataKey(ctx, keyCtx); !errors.Is(err, kmspolicy.ErrNotSealed) || r.secret != nil {
		t.Fatalf("got %v", err)
	}
}

func TestNewRootNeedsItsConfiguration(t *testing.T) {
	if _, err := NewRoot(RootConfig{}); err == nil {
		t.Fatal("accepted an empty configuration")
	}
}
