package contracts

import "time"

// The model's API key, as the operator gives it to the enclave (ADR 0028,
// "The key"): PUT /v1/admin/model-key with the admin token sets it, DELETE
// takes it away, GET says whether one is set. The key is kept sealed and is
// never answered back.

// ModelKeySet is the body of PUT /v1/admin/model-key: the provider's API key.
// It is read from the request and sealed; it is never logged, echoed or
// answered.
type ModelKeySet struct {
	Key string `json:"key"`
}

// ModelKey is what the operator is told of the key: whether one is set,
// since when, and the first 16 hexadecimal digits of the SHA-256 of the key
// as given, which the operator can compute from their own copy
// (printf %s "$key" | sha256sum) to tell which key the enclave holds.
type ModelKey struct {
	Set         bool      `json:"set"`
	SetAt       time.Time `json:"set_at,omitzero"`
	Fingerprint string    `json:"fingerprint,omitempty"`
}
