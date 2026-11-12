package store

import (
	"errors"
	"fmt"
)

// A mailbox's and a message's identifiers are parts of keys: of the table's
// items (mailbox#<id>, msg#<id>) and of the bucket's raw objects
// (raw/<mailbox>/<id>), where ("a/b", "c") and ("a", "b/c") would name one
// object. The service makes them of letters, digits, _ and - only (mbx_…,
// a message's hexadecimal identifier, F1's mailbox "test"), so the store
// takes nothing else: no separator, no dot, nothing empty.

// errInvalidID refuses a write under an identifier that makes no key. Like
// errNoSuchID it does not hold the identifier: errors reach logs.
var errInvalidID = errors.New("store: an identifier that is not only letters, digits, _ and -")

// errNoSuchID answers a read or a delete under an identifier that makes no
// key: nothing can be stored there, so it is not found.
var errNoSuchID = fmt.Errorf("%w: an identifier that is not only letters, digits, _ and -", ErrNotFound)

// validID reports whether id can be part of a key.
func validID(id string) bool {
	if id == "" {
		return false
	}
	for i := range len(id) {
		switch c := id[i]; {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// checkIDs is checked before any request that builds a key from a mailbox's
// or a message's identifier: errNoSuchID for a read or a delete (read),
// errInvalidID for a write.
func checkIDs(read bool, ids ...string) error {
	for _, id := range ids {
		if !validID(id) {
			if read {
				return errNoSuchID
			}
			return errInvalidID
		}
	}
	return nil
}

// checkRawBound is ReadRaw's check of its bound, the same in every store.
func checkRawBound(maxBytes int64) error {
	if maxBytes < 0 || maxBytes > maxRawRead {
		return fmt.Errorf("store: a raw message is read with a bound of 0 to %d bytes", int64(maxRawRead))
	}
	return nil
}
