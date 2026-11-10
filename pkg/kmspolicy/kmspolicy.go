// Package kmspolicy reads an AWS KMS key policy and answers one question: which
// enclave images can use the key, and can anyone use it without proving an image?
//
// Permaily's mailbox data keys are wrapped by a KMS key whose policy releases
// them only to a Nitro Enclave that proves its image (PCR0) with an attestation
// document. The enclave publishes the live policy over its attested TLS key, so
// a verifier can run Analyze on it and compare the images with its own build.
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
			if !slices.Contains(safeActions, strings.ToLower(a)) {
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
