package connections

import (
	"encoding/json"
	"slices"
	"strconv"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
)

// An account's list of connections (ADR 0030, "Its record and the account's
// list"; revised after the second design review, C2): a strict anchored
// record in epochs, as a token family's is, so that neither its history nor
// its document grows with the account's life. Epoch n is ListName(account,
// n); the write that reaches ListEpochVersions in it writes it Moved and
// makes epoch n+1 with CreateNew, carrying everything: the members, which
// connection is each client's current one, the requests decided in the last
// oauth.RequestTTL and the closing fence with its generation, so lookup,
// replay of a decision, the fence and the cleanup all read the newest epoch
// alone. A reader follows Moved to the newest epoch (an enclave remembers the
// newest it read; epochs only go forward) and makes an epoch a crash left
// unmade again from the one before. Its document is bounded by count
// (ListActiveMax, ListEndedMax, ListDecidedMax) and bytes (ListBytesMax),
// each checked by every write. The pure transitions are here, committed with
// the design, so pull request 3 and its tests share them.

// Bounds of the list (C2).
const (
	// ListEpochVersions is how many versions one epoch of the list holds:
	// below store.MaxHistoryReads, so a cold strict read never fails.
	ListEpochVersions = 400
	// ListActiveMax bounds the connections an account has active or
	// pending at once: one approval past it is refused
	// (contracts.ErrTooManyConnections) until the person revokes one. A
	// replacement does not count twice.
	ListActiveMax = 64
	// ListEndedMax bounds the ended connections the list keeps to show
	// (contracts.ConnectionList, 30 days at most): past it, the oldest
	// ended one is removed early, its record and families first, then its
	// entry, by the write that ends another (only an ended one, never an
	// active one; its consent receipt keeps its scope, ADR 0031).
	ListEndedMax = 64
	// ListDecidedMax bounds the requests decided in the last
	// oauth.RequestTTL: past it a decision is refused
	// (contracts.ErrTooManyRequests) until older ones leave.
	ListDecidedMax = 64
	// ListBytesMax bounds the list's JSON before it is sealed.
	ListBytesMax = 128 << 10
)

// ListName is the record of epoch of an account's list.
func ListName(account string, epoch uint64) string {
	return RecordList + account + "-" + strconv.FormatUint(epoch, 10)
}

// ListMember is one connection the list names.
type ListMember struct {
	ID       string                    `json:"id"`
	ClientID string                    `json:"client_id"`
	State    contracts.ConnectionState `json:"state"`
	// Pending: approved and being made (finding 6), fenced by the sweep or
	// EndAll if its maker crashed.
	Pending bool      `json:"pending,omitzero"`
	MadeAt  time.Time `json:"made_at"`
	EndedAt time.Time `json:"ended_at,omitzero"`
	// Generation is the closing generation it was made in.
	Generation uint64 `json:"generation"`
}

// ListDecision is one request decided: the hash of its identifier.
type ListDecision struct {
	Request string    `json:"request"`
	At      time.Time `json:"at"`
}

// ListState is one epoch of an account's list, signed in the store.
type ListState struct {
	V       int    `json:"v"`
	Account string `json:"account"`
	Epoch   uint64 `json:"epoch"`
	// Moved: this epoch is closed; the list goes on in Epoch+1.
	Moved bool `json:"moved,omitzero"`
	// Closing is the deletion's fence, and Generation the closing
	// generation EndAll ends through (finding 6).
	Closing    bool              `json:"closing,omitzero"`
	Generation uint64            `json:"generation"`
	Members    []ListMember      `json:"members"`
	Current    map[string]string `json:"current"`
	Decided    []ListDecision    `json:"decided"`
}

// Decide records request as decided at now, once (a request is decided
// once, also when its mark is deleted): contracts.ErrRequestEnded for one
// decided already or a closing list, contracts.ErrTooManyRequests past
// ListDecidedMax.
func (l *ListState) Decide(request string, now time.Time) error {
	l.Decided = slices.DeleteFunc(l.Decided, func(d ListDecision) bool { return !now.Before(d.At.Add(oauth.RequestTTL)) })
	switch {
	case l.Closing:
		return contracts.ErrRequestEnded
	case slices.ContainsFunc(l.Decided, func(d ListDecision) bool { return d.Request == request }):
		return contracts.ErrRequestEnded
	case len(l.Decided) >= ListDecidedMax:
		return contracts.ErrTooManyRequests
	}
	l.Decided = append(l.Decided, ListDecision{Request: request, At: now})
	return nil
}

// AddPending names a connection being made for client: refused when the
// list is closing, or when the account would have more than ListActiveMax
// active or pending (the client's current one, which it replaces, not
// counted).
func (l *ListState) AddPending(id, client string, now time.Time) error {
	if l.Closing {
		return contracts.ErrRequestEnded
	}
	active := 0
	for _, m := range l.Members {
		if m.State == contracts.ConnectionActive && m.ID != l.Current[client] {
			active++
		}
	}
	if active >= ListActiveMax {
		return contracts.ErrTooManyConnections
	}
	l.Members = append(l.Members, ListMember{ID: id, ClientID: client, State: contracts.ConnectionActive, Pending: true, MadeAt: now, Generation: l.Generation})
	return nil
}

// End marks a member ended at now, and answers the ended members to remove
// early (oldest first) past ListEndedMax: their records and families are
// removed before the list is written without them (RemoveEnded).
func (l *ListState) End(id string, now time.Time) (remove []string) {
	for i := range l.Members {
		if l.Members[i].ID == id && l.Members[i].State != contracts.ConnectionEnded {
			l.Members[i].State, l.Members[i].Pending, l.Members[i].EndedAt = contracts.ConnectionEnded, false, now
		}
	}
	for client, cur := range l.Current {
		if cur == id {
			delete(l.Current, client)
		}
	}
	var ended []ListMember
	for _, m := range l.Members {
		if m.State == contracts.ConnectionEnded {
			ended = append(ended, m)
		}
	}
	slices.SortStableFunc(ended, func(a, b ListMember) int { return a.EndedAt.Compare(b.EndedAt) })
	for i := 0; i < len(ended)-ListEndedMax; i++ {
		remove = append(remove, ended[i].ID)
	}
	return remove
}

// RemoveEnded drops ended members whose records were removed.
func (l *ListState) RemoveEnded(ids []string) {
	l.Members = slices.DeleteFunc(l.Members, func(m ListMember) bool {
		return m.State == contracts.ConnectionEnded && slices.Contains(ids, m.ID)
	})
}

// Fits says the list is within its bytes and counts.
func (l *ListState) Fits() bool {
	ended := 0
	for _, m := range l.Members {
		if m.State == contracts.ConnectionEnded {
			ended++
		}
	}
	b, err := json.Marshal(l)
	return err == nil && len(b) <= ListBytesMax && ended <= ListEndedMax && len(l.Members)-ended <= ListActiveMax+1 && len(l.Decided) <= ListDecidedMax
}

// Rotate says whether the write about to be made at versions must close
// this epoch: it is then written Moved, and Next made with CreateNew.
func (l *ListState) Rotate(versions uint64) bool { return versions+1 >= ListEpochVersions }

// Next is the next epoch of a list written Moved: the same document.
func (l *ListState) Next() ListState {
	b, _ := json.Marshal(l)
	var n ListState
	_ = json.Unmarshal(b, &n)
	n.Epoch, n.Moved = l.Epoch+1, false
	return n
}
