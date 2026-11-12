package inbox

import (
	"errors"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/store"
)

// The times a message identifier carries, and a meta that opens but is not
// one: what the reader makes of what whoever writes to the table put there.
func TestWhatTheTableCanHold(t *testing.T) {
	for _, id := range []string{"", "0123", strings.Repeat("z", 32)} {
		if !timeOfMessageID(id).IsZero() {
			t.Errorf("%q has a time", id)
		}
	}
	b := Box{MailboxID: "mbx_a", Key: make([]byte, envelope.KeySize)}
	id := strings.Repeat("0", 31) + "1"
	sealed, err := envelope.Seal(b.Key, []byte("not json"), envelope.AAD(PurposeMeta, b.MailboxID, id))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openMeta(b, &store.Message{ID: id, Meta: sealed}); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("%v", err)
	}
}
