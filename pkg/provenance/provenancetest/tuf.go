package provenancetest

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/permaily-org/permaily/pkg/provenance"
)

// The test authority publishes its trust root the way Sigstore does: through
// TUF metadata that five keyholders sign, three at least.

// keyholder is one of the keys of the TUF root.
type keyholder struct {
	key *ecdsa.PrivateKey
	id  string
	pem string
}

func newKeyholder() (keyholder, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return keyholder{}, err
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return keyholder{}, err
	}
	id := sha256.Sum256(der)
	return keyholder{key, hex.EncodeToString(id[:]), string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))}, nil
}

func (a *Authority) initTUF() error {
	for range 5 {
		k, err := newKeyholder()
		if err != nil {
			return err
		}
		a.keyholders = append(a.keyholders, k)
	}
	a.RootVersion = 1
	root, err := a.SignTUF(a.RootDoc())
	a.tufRoot = root
	return err
}

// RootDoc returns what the TUF root metadata says now: the five keyholders,
// three of whom must sign root and targets metadata. Tests change it before
// signing it with SignTUF.
func (a *Authority) RootDoc() map[string]any {
	keys := map[string]any{}
	var ids []any
	for _, k := range a.keyholders {
		keys[k.id] = map[string]any{"keytype": "ecdsa", "scheme": "ecdsa-sha2-nistp256", "keyval": map[string]any{"public": k.pem}}
		ids = append(ids, k.id)
	}
	role := map[string]any{"keyids": ids, "threshold": 3}
	return map[string]any{
		"_type": "root", "spec_version": "1.0", "version": a.RootVersion, "consistent_snapshot": true,
		"expires": time.Now().AddDate(0, 6, 0).UTC().Format(time.RFC3339),
		"keys":    keys,
		"roles":   map[string]any{"root": role, "targets": role, "snapshot": map[string]any{"keyids": ids[:1], "threshold": 1}, "timestamp": map[string]any{"keyids": ids[:1], "threshold": 1}},
	}
}

// TargetsDoc returns TUF targets metadata of the next version that names
// trustedRoot as trusted_root.json.
func (a *Authority) TargetsDoc(trustedRoot []byte) map[string]any {
	a.TargetsVersion++
	sum := sha256.Sum256(trustedRoot)
	return map[string]any{
		"_type": "targets", "spec_version": "1.0", "version": a.TargetsVersion,
		"expires": time.Now().AddDate(1, 0, 0).UTC().Format(time.RFC3339),
		"targets": map[string]any{
			"trusted_root.json": map[string]any{"length": len(trustedRoot), "hashes": map[string]any{"sha256": hex.EncodeToString(sum[:])}},
			"another.file":      map[string]any{"length": 3, "hashes": map[string]any{"sha256": strings.Repeat("ab", 32)}},
		},
	}
}

// SignTUF returns TUF metadata: signed, with the signatures of the keyholders
// at the positions signers, or of the first three without any. A position
// given twice signs twice.
func (a *Authority) SignTUF(signed map[string]any, signers ...int) ([]byte, error) {
	if signers == nil {
		signers = []int{0, 1, 2}
	}
	canonical, err := canonicalJSON(signed)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(canonical)
	signatures := []map[string]string{}
	for _, i := range signers {
		sig, err := ecdsa.SignASN1(rand.Reader, a.keyholders[i].key, digest[:])
		if err != nil {
			return nil, err
		}
		signatures = append(signatures, map[string]string{"keyid": a.keyholders[i].id, "sig": hex.EncodeToString(sig)})
	}
	return json.Marshal(map[string]any{"signatures": signatures, "signed": signed})
}

// TUFRoot returns the TUF root metadata in force.
func (a *Authority) TUFRoot() []byte { return a.tufRoot }

// Rotate replaces the keyholder at position i with a new one and returns the
// next TUF root metadata, signed by the five keyholders it names: four of them
// are also keyholders of the root before, which is what makes it follow.
func (a *Authority) Rotate(i int) ([]byte, error) {
	k, err := newKeyholder()
	if err != nil {
		return nil, err
	}
	a.keyholders[i] = k
	a.RootVersion++
	root, err := a.SignTUF(a.RootDoc(), 0, 1, 2, 3, 4)
	a.tufRoot = root
	return root, err
}

// Publish returns targets metadata of the next version for the trust root as
// it is now, and that trust root.
func (a *Authority) Publish() (targets, trustedRoot []byte, err error) {
	if trustedRoot, err = a.TrustedRootJSON(); err != nil {
		return nil, nil, err
	}
	targets, err = a.SignTUF(a.TargetsDoc(trustedRoot))
	return targets, trustedRoot, err
}

// NextLog replaces the log shaped like Rekor v2 with a new instance, the way
// Sigstore rotates them: bundles made from now on are recorded there, and
// only a trust root published afterwards names it.
func (a *Authority) NextLog() error {
	a.log2Generation++
	return a.newLog2(fmt.Sprintf("log%d-1.rekor.test", 2099+a.log2Generation))
}

// canonicalJSON encodes v the way TUF signs it: keys in order, no space, and
// strings with nothing escaped but the quote and the backslash.
func canonicalJSON(v any) ([]byte, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(plain))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	var write func(v any)
	write = func(v any) {
		switch v := v.(type) {
		case string:
			out.WriteString(`"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`)
		case []any:
			out.WriteByte('[')
			for i, e := range v {
				if i > 0 {
					out.WriteByte(',')
				}
				write(e)
			}
			out.WriteByte(']')
		case map[string]any:
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			out.WriteByte('{')
			for i, k := range keys {
				if i > 0 {
					out.WriteByte(',')
				}
				write(k)
				out.WriteByte(':')
				write(v[k])
			}
			out.WriteByte('}')
		case nil:
			out.WriteString("null")
		default:
			fmt.Fprint(&out, v)
		}
	}
	write(doc)
	return out.Bytes(), nil
}

// TrustRoot returns the trust root that accepts this authority's bundles, as
// its TUF repository publishes it now.
func (a *Authority) TrustRoot() *provenance.TrustRoot {
	targets, trustedRoot, err := a.Publish()
	if err != nil {
		panic(err)
	}
	tr, err := provenance.FromTUF(a.tufRoot, targets, trustedRoot)
	if err != nil {
		panic(err)
	}
	return tr
}
