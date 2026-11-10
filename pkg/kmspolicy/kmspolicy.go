// Package kmspolicy reads an AWS KMS key policy and answers one question: which
// enclave images can use the key, and can anyone use it without proving an image?
//
// Each release of Permaily has its own KMS key, which wraps the root secret
// that everything stored is sealed under. Its policy lets only a Nitro Enclave
// that proves that release's image (PCR0) decrypt, and gives nobody a way to
// change that. The enclave checks it before trusting a key (Sealed), and
// publishes the live policy over its attested TLS key so a verifier can run the
// same check.
//
// The analysis is conservative: a statement it does not fully understand is
// reported as a problem. Principals are ignored on purpose, because an IAM
// policy can only grant what the key policy allows to the account.
package kmspolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrMalformed means the policy is not a JSON policy document this package understands.
var ErrMalformed = errors.New("kmspolicy: malformed policy")

// Report is the result of Analyze.
type Report struct {
	// Images are the image hashes (PCR0, lowercase hex) that may use the key for
	// anything beyond administration, sorted.
	Images []string
	// Problems are statements that let someone use the key without proving an
	// image. A policy with problems does not protect the data.
	Problems []string
	// AdminCanChangePolicy is true when some principal may replace the policy
	// (kms:PutKeyPolicy). The report then only describes the policy as it was read.
	AdminCanChangePolicy bool
}

// Condition keys that pin the enclave image. Both hold PCR0.
var imageKeys = []string{"kms:recipientattestation:imagesha384", "kms:recipientattestation:pcr0"}

// Operators that compare the whole value. Anything else (StringLike, ...IfExists,
// ForAnyValue:...) could match a request without the expected image.
var exactOperators = []string{"stringequals", "stringequalsignorecase"}

// Administrative actions that reveal no key material and cannot be used to
// read, replace or re-wrap data keys. Everything else needs an image condition.
var safeActions = []string{
	"kms:describe*", "kms:get*", "kms:list*",
	"kms:describekey", "kms:getkeypolicy", "kms:getkeyrotationstatus",
	"kms:listgrants", "kms:listkeypolicies", "kms:listresourcetags", "kms:listkeyrotations",
	"kms:putkeypolicy",
	"kms:enablekeyrotation", "kms:disablekeyrotation", "kms:rotatekeyondemand",
	"kms:tagresource", "kms:untagresource",
	"kms:schedulekeydeletion", "kms:cancelkeydeletion", "kms:updatekeydescription",
	"kms:createalias", "kms:deletealias", "kms:updatealias",
	"kms:enablekey", "kms:disablekey",
	"kms:revokegrant", "kms:retiregrant",
}

// Analyze parses a key policy document and reports who can use the key.
func Analyze(policy []byte) (Report, error) {
	return analyze(policy, safeActions)
}

// analyze is Analyze with the list of actions that need no image condition.
func analyze(policy []byte, safe []string) (Report, error) {
	var doc struct {
		Version   string
		ID        string `json:"Id"`
		Statement oneOrMany[statement]
	}
	dec := json.NewDecoder(bytes.NewReader(policy))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return Report{}, fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	if dec.More() {
		return Report{}, fmt.Errorf("%w: trailing data", ErrMalformed)
	}
	if len(doc.Statement) == 0 {
		return Report{}, fmt.Errorf("%w: no statements", ErrMalformed)
	}

	var r Report
	denied := false // an unconditional Deny of kms:PutKeyPolicy for everyone
	for i, s := range doc.Statement {
		name := s.Sid
		if name == "" {
			name = fmt.Sprintf("#%d", i+1)
		}
		switch strings.ToLower(s.Effect) {
		case "deny":
			if len(s.Condition) == 0 && len(s.NotAction) == 0 && isEveryone(s.Principal) && matchesAny(s.Action, "kms:putkeypolicy") {
				denied = true
			}
			continue
		case "allow":
		default:
			return Report{}, fmt.Errorf("%w: statement %s: effect %q", ErrMalformed, name, s.Effect)
		}
		if len(s.NotAction) > 0 || len(s.NotPrincipal) > 0 || len(s.NotResource) > 0 {
			r.Problems = append(r.Problems, fmt.Sprintf("statement %s uses NotAction, NotPrincipal or NotResource", name))
			continue
		}
		if len(s.Action) == 0 {
			return Report{}, fmt.Errorf("%w: statement %s: no actions", ErrMalformed, name)
		}
		if matchesAny(s.Action, "kms:putkeypolicy") {
			r.AdminCanChangePolicy = true
		}
		var unsafe []string
		for _, a := range s.Action {
			if !slices.Contains(safe, strings.ToLower(a)) {
				unsafe = append(unsafe, a)
			}
		}
		if len(unsafe) == 0 {
			continue
		}
		images, why := pinnedImages(s.Condition)
		if why != "" {
			r.Problems = append(r.Problems, fmt.Sprintf("statement %s allows %s %s", name, strings.Join(unsafe, ", "), why))
			continue
		}
		r.Images = append(r.Images, images...)
	}
	if denied {
		r.AdminCanChangePolicy = false
	}
	slices.Sort(r.Images)
	r.Images = slices.Compact(r.Images)
	return r, nil
}

// Key is what KMS says about the key itself (DescribeKey).
type Key struct {
	// Origin is where the key material comes from. Only AWS_KMS material was
	// generated inside KMS and never existed outside it.
	Origin string `json:"origin"`
	// Spec and Usage are the key type: SYMMETRIC_DEFAULT and ENCRYPT_DECRYPT.
	Spec  string `json:"spec"`
	Usage string `json:"usage"`
	// Manager is CUSTOMER for a key the account created.
	Manager string `json:"manager"`
	// State is Enabled for a usable key, PendingDeletion once it is scheduled
	// for deletion.
	State string `json:"state"`
	// MultiRegion keys have replicas in other regions, each with its own policy.
	MultiRegion bool `json:"multi_region"`
	// CustomKeyStore is set when the material lives in a key store the account
	// operates.
	CustomKeyStore string `json:"custom_key_store,omitempty"`
}

// problems lists what makes the key material reachable outside this key's policy.
func (k Key) problems() []string {
	var out []string
	if k.Origin != "AWS_KMS" || k.CustomKeyStore != "" {
		out = append(out, fmt.Sprintf("the key material has origin %q, so it exists or existed outside KMS", k.Origin))
	}
	if k.MultiRegion {
		out = append(out, "the key is multi-Region: a replica has a policy of its own")
	}
	if k.Spec != "SYMMETRIC_DEFAULT" || k.Usage != "ENCRYPT_DECRYPT" || k.Manager != "CUSTOMER" {
		out = append(out, fmt.Sprintf("the key is %s %s managed by %s, not a symmetric encryption key of the account", k.Spec, k.Usage, k.Manager))
	}
	return out
}

// ErrNotSealed means a key does not belong to one image alone, for good.
var ErrNotSealed = errors.New("kmspolicy: the key is not sealed to the image")

// Sealed checks that a key can only ever be used by the enclave image whose
// PCR0 is image (hex): the policy releases it to that image and no other, lets
// nobody use it without proving an image, gives nobody a way to change the
// policy, no grant exists, and the key material never left KMS. With no way to
// change the policy or add a grant, what is true when this is checked stays true.
//
// Creating ciphertexts (kms:GenerateDataKey, kms:Encrypt) needs no image
// here, unlike in Analyze: the running enclave must put the root secret under
// the key of the next release, whose image is not its own. What the key
// protects is accepted for the attestation document stored with it, never
// because it opens under the key, so a ciphertext made by the operator is
// refused. Only decrypting needs the image.
//
// grants and truncated are the number of grants on the key and whether the
// list was cut.
func Sealed(policy []byte, grants int, truncated bool, key Key, image string) (Report, error) {
	rep, err := analyze(policy, append([]string{"kms:generatedatakey", "kms:encrypt"}, safeActions...))
	if err != nil {
		return rep, err
	}
	problems := slices.Clone(rep.Problems)
	if rep.AdminCanChangePolicy {
		problems = append(problems, "the policy can be replaced (kms:PutKeyPolicy)")
	}
	if grants > 0 || truncated {
		problems = append(problems, fmt.Sprintf("the key has %d grant(s); grants give access outside the policy", grants))
	}
	problems = append(problems, key.problems()...)
	image = strings.ToLower(image)
	switch {
	case !isImageHash(image):
		problems = append(problems, "the image to check is not a SHA-384 hash")
	case len(rep.Images) == 0:
		problems = append(problems, "no image can use the key")
	case !slices.Equal(rep.Images, []string{image}):
		problems = append(problems, "the key is released to other images: "+strings.Join(slices.DeleteFunc(slices.Clone(rep.Images), func(i string) bool { return i == image }), ", "))
		if !slices.Contains(rep.Images, image) {
			problems = append(problems, "the key is not released to this image")
		}
	}
	if len(problems) > 0 {
		return rep, fmt.Errorf("%w: %s", ErrNotSealed, strings.Join(problems, "; "))
	}
	return rep, nil
}

// pinnedImages returns the image hashes a statement's condition requires, or
// why the condition does not pin an image.
func pinnedImages(cond map[string]map[string]json.RawMessage) ([]string, string) {
	var images []string
	found := false
	for op, keys := range cond {
		for key, raw := range keys {
			if !slices.Contains(imageKeys, strings.ToLower(key)) {
				continue // other conditions only narrow the statement further
			}
			var values oneOrMany[string]
			if err := json.Unmarshal(raw, &values); err != nil {
				return nil, "with an image condition that is not text"
			}
			if !slices.Contains(exactOperators, strings.ToLower(op)) {
				return nil, fmt.Sprintf("with the image under %s, which does not compare the whole value", op)
			}
			if len(values) == 0 {
				return nil, "with an empty image list"
			}
			for _, v := range values {
				v = strings.ToLower(v)
				if !isImageHash(v) {
					return nil, "with a value that is not a SHA-384 image hash"
				}
				images = append(images, v)
			}
			found = true
		}
	}
	if !found {
		return nil, "without requiring an enclave image"
	}
	return images, ""
}

func isImageHash(s string) bool {
	if len(s) != 96 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// matchesAny reports whether an action pattern in the list covers action
// (lowercase). Only a trailing "*" and the bare "*" are understood as wildcards;
// that is enough to recognise kms:PutKeyPolicy, kms:Put*, kms:* and *.
func matchesAny(patterns []string, action string) bool {
	for _, p := range patterns {
		p = strings.ToLower(p)
		if p == action || p == "*" {
			return true
		}
		if prefix, ok := strings.CutSuffix(p, "*"); ok && strings.HasPrefix(action, prefix) {
			return true
		}
	}
	return false
}

func isEveryone(principal json.RawMessage) bool {
	var s string
	if json.Unmarshal(principal, &s) == nil {
		return s == "*"
	}
	var m map[string]oneOrMany[string]
	if json.Unmarshal(principal, &m) != nil {
		return false
	}
	return len(m) == 1 && slices.Equal(m["AWS"], []string{"*"})
}

type statement struct {
	Sid          string
	Effect       string
	Principal    json.RawMessage
	NotPrincipal json.RawMessage
	Action       oneOrMany[string]
	NotAction    oneOrMany[string]
	Resource     json.RawMessage
	NotResource  json.RawMessage
	Condition    map[string]map[string]json.RawMessage
}

// oneOrMany decodes a JSON value that policies write either alone or as a list.
type oneOrMany[T any] []T

func (o *oneOrMany[T]) UnmarshalJSON(b []byte) error {
	if b = bytes.TrimSpace(b); len(b) > 0 && b[0] == '[' {
		var many []T
		if err := strictUnmarshal(b, &many); err != nil {
			return err
		}
		*o = many
		return nil
	}
	var one T
	if err := strictUnmarshal(b, &one); err != nil {
		return err
	}
	*o = []T{one}
	return nil
}

func strictUnmarshal(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
