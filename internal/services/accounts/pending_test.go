package accounts

import "strings"

// pending counts the sign-ins in progress of a kind that the storage holds
// and that have not ended.
func (w *world) pending(kind string) int {
	n := 0
	for _, e := range w.mem.AllEntries() {
		if strings.HasPrefix(e.Partition, pendingPartition(kind, "")) && e.Key == pendingKey && e.ExpiresAt.After(w.now) {
			n++
		}
	}
	return n
}
