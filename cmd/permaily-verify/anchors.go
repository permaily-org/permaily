package main

import (
	"fmt"
	"io"
	"time"

	"github.com/permaily-org/permaily/pkg/attestation"
)

// lockMargin is how close to its end the lock of a commit may be before the
// verifier warns: whoever runs the storage can remove a commit the day its
// lock ends, and put an earlier copy of the record back.
const lockMargin = 30 * 24 * time.Hour

// checkAnchors checks what an enclave says about the records it refuses an
// earlier copy of (ADR 0016): the newest commit of each is locked in S3, where
// nobody in the account can remove it, and the store of commits never went
// back while the enclave watched.
func checkAnchors(a *attestation.AnchorStatus, now time.Time) error {
	if a.Bucket == "" || a.RetentionDays < 1 {
		return fmt.Errorf("the enclave names no store of commits, or no time it keeps them")
	}
	for _, r := range a.Regressions {
		return fmt.Errorf("the store of commits went back: the enclave had %s at version %d, and at %s the store answered version %d. Nobody can remove a commit, so the store was replaced or its guarantee failed",
			r.Name, r.Had, r.At.UTC().Format(time.RFC3339), r.Found)
	}
	for _, r := range a.Records {
		if r.Version == 0 || !r.LockedUntil.After(now) {
			return fmt.Errorf("the newest commit of %s (version %d) is not locked: whoever runs the storage could remove it and put an earlier copy of the record back", r.Name, r.Version)
		}
	}
	return nil
}

func printAnchors(w io.Writer, a *attestation.AnchorStatus, now time.Time) {
	if a == nil {
		return
	}
	if len(a.Records) == 0 {
		fmt.Fprintf(w, "      records that cannot be put back to an earlier copy: none read yet; their commits go to the S3 bucket %s, each locked for %d days\n", a.Bucket, a.RetentionDays)
		return
	}
	fmt.Fprintf(w, "      %d record(s) cannot be put back to an earlier copy: each change leaves a commit in the S3 bucket %s, locked where nobody in the account can remove it, and the enclave reads a record by its newest commit\n", len(a.Records), a.Bucket)
	for _, r := range a.Records {
		fmt.Fprintf(w, "      %s is at version %d, committed %s, locked until %s\n", r.Name, r.Version, r.CommittedAt.UTC().Format(time.RFC3339), r.LockedUntil.UTC().Format(time.DateOnly))
		if r.LockedUntil.Before(now.Add(lockMargin)) {
			fmt.Fprintf(w, "      warning: that lock ends within %d days; after it an earlier copy of %s could be put back\n", int(lockMargin.Hours()/24), r.Name)
		}
	}
}
