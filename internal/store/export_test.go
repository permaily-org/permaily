package store

import (
	"fmt"
	"sync/atomic"
)

// What the tests outside the package see of S3Commits' memory of what it read.

// MaxKnownObjects is how many object versions an S3Commits keeps in memory.
const MaxKnownObjects = maxKnownObjects

// KnownObjects is how many object versions s keeps in memory now.
func KnownObjects(s *S3Commits) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.known)
}

var fillers atomic.Int64

// FillKnown keeps n entries that belong to no record, as the reads of other
// records do: the oldest are dropped past MaxKnownObjects.
func FillKnown(s *S3Commits, n int) {
	for range n {
		s.keep(fmt.Sprintf("another record\x00filler\x00%d", fillers.Add(1)), nil)
	}
}
