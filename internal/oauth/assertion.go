package oauth

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

// A client assertion's jti is used once (ADR 0030, "private_key_jwt";
// revised after the second design review, C1). What decides it is not a
// mark whoever writes the table can delete but a strict anchored record
// (ADR 0016, 0026), which cannot be put back or hidden: the record of the
// client's assertions accepted until one hour (the hour of
// AssertionAcceptedUntil(exp), UTC), one of AssertionShards by the jti. A
// replay of an assertion has the same client, jti and exp, so the same
// record, wherever and whenever it is presented while it could still be
// accepted; one record holds at most AssertionsPerRecord uses and
// AssertionVersionsMax versions (one a use), so a strict read of it never
// fails for its history. The service reads it strictly and now, and writes
// the use at the version read (store.ErrChanged: read again, at most
// RecordTries times) before the request the assertion authenticates goes
// on: the token endpoint, revocation and pushed requests alike.
//
// A record is dead once its hour has passed (no assertion in it can be
// accepted again); the hourly sweep removes the records of the hour before
// last, found through a sealed list it keeps of the records it made, which
// decides nothing: deleting it leaves dead records in the store, never an
// assertion accepted twice.

// Bounds of the records of assertions.
const (
	// AssertionShards is how many records share one client's hour.
	AssertionShards = 16
	// AssertionsPerRecord bounds the uses one record holds: a client
	// authenticates at most AssertionShards × AssertionsPerRecord times an
	// hour (32,000) before its assertions are refused
	// temporarily_unavailable, which only the vendor's own volume can
	// reach (a replay writes nothing).
	AssertionsPerRecord = 2000
	// AssertionVersionsMax is below the strict read's 10,000 objects.
	AssertionVersionsMax = 9000
)

// RecordAssertions + the client's hash + "-" + the hour (YYYYMMDDHH, UTC)
// of the assertion's acceptance's end + "-" + its shard (decimal) names a
// record of assertions.
const RecordAssertions = "oauth-assertions-"

// AssertionRecordName is the record that decides an assertion of clientID
// with jti and exp, and the hash its use is kept as (domain-separated: the
// table holds no jti).
func AssertionRecordName(clientID, jti string, exp time.Time) (name, use string) {
	client := sha256.Sum256([]byte("permaily oauth assertion client v1\x00" + clientID))
	u := sha256.Sum256([]byte("permaily oauth assertion jti v1\x00" + clientID + "\x00" + jti))
	shard := int(u[0]) % AssertionShards
	hour := AssertionAcceptedUntil(exp).UTC().Format("2006010215")
	return RecordAssertions + hex.EncodeToString(client[:16]) + "-" + hour + "-" + strconv.Itoa(shard), hex.EncodeToString(u[:])
}

// AssertionRecord is one record's document, sealed in the store.
type AssertionRecord struct {
	V    int            `json:"v"`
	Uses []AssertionUse `json:"uses"`
}

// AssertionUse is one assertion accepted: its hash and until when it could
// be accepted.
type AssertionUse struct {
	Hash  string    `json:"h"`
	Until time.Time `json:"u"`
}

// AssertionResult is what presenting one assertion decides.
type AssertionResult int8

const (
	// AssertionAccepted: write the record at the version read, then go on.
	AssertionAccepted AssertionResult = iota + 1
	// AssertionReplayed: its jti was accepted before: invalid_client
	// (counted as assertion), nothing written.
	AssertionReplayed
	// AssertionExpired: no longer accepted (AssertionLive): invalid_client.
	AssertionExpired
	// AssertionFull: the record has no room: temporarily_unavailable,
	// nothing written, the assertion not accepted.
	AssertionFull
)

// Accept is the transition of one assertion on its record read at
// versions: use is AssertionRecordName's, exp the assertion's. It changes r
// only for AssertionAccepted.
func (r *AssertionRecord) Accept(use string, exp, now time.Time, versions uint64) AssertionResult {
	if !AssertionLive(exp, now) {
		return AssertionExpired
	}
	for _, u := range r.Uses {
		if u.Hash == use {
			return AssertionReplayed
		}
	}
	if len(r.Uses) >= AssertionsPerRecord || versions+1 > AssertionVersionsMax {
		return AssertionFull
	}
	r.Uses = append(r.Uses, AssertionUse{Hash: use, Until: AssertionAcceptedUntil(exp)})
	return AssertionAccepted
}
