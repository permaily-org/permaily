package differential

import (
	"errors"
	"fmt"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

// FollowTUF answers, with go-tuf, the question provenance's Update answers
// about TUF metadata: starting from the root anchor, does each root in roots
// follow the one before, did the last one's keyholders sign targets, does
// targets name trustedRoot, is nothing expired at now, and is targets at least
// version minTargets? It returns the versions of the last root and of targets.
func FollowTUF(anchor []byte, roots [][]byte, targets, trustedRoot []byte, now time.Time, minTargets int64) (rootVersion, targetsVersion int64, err error) {
	root, err := metadata.Root().FromBytes(anchor)
	if err != nil {
		return 0, 0, fmt.Errorf("anchor: %w", err)
	}
	for _, doc := range roots {
		next, err := metadata.Root().FromBytes(doc)
		if err != nil {
			return 0, 0, fmt.Errorf("root: %w", err)
		}
		if next.Signed.Version != root.Signed.Version+1 {
			return 0, 0, fmt.Errorf("root %d does not follow root %d", next.Signed.Version, root.Signed.Version)
		}
		if err := root.VerifyDelegate(metadata.ROOT, next); err != nil {
			return 0, 0, fmt.Errorf("root %d is not signed by root %d: %w", next.Signed.Version, root.Signed.Version, err)
		}
		if err := next.VerifyDelegate(metadata.ROOT, next); err != nil {
			return 0, 0, fmt.Errorf("root %d is not signed by its own keys: %w", next.Signed.Version, err)
		}
		root = next
	}
	t, err := metadata.Targets().FromBytes(targets)
	if err != nil {
		return 0, 0, fmt.Errorf("targets: %w", err)
	}
	if err := root.VerifyDelegate(metadata.TARGETS, t); err != nil {
		return 0, 0, fmt.Errorf("targets: %w", err)
	}
	if root.Signed.IsExpired(now) || t.Signed.IsExpired(now) {
		return 0, 0, errors.New("expired")
	}
	if t.Signed.Version < minTargets {
		return 0, 0, fmt.Errorf("targets %d is older than %d", t.Signed.Version, minTargets)
	}
	file, ok := t.Signed.Targets["trusted_root.json"]
	if !ok {
		return 0, 0, errors.New("targets does not name trusted_root.json")
	}
	if err := file.VerifyLengthHashes(trustedRoot); err != nil {
		return 0, 0, err
	}
	return root.Signed.Version, t.Signed.Version, nil
}
