package mailboxes

import (
	"context"
	"errors"
)

// WithoutBounds writes every disconnect entry again as an image from before
// the entries carried the bound of a mailbox's days (leavingDoc.Since).
func WithoutBounds(ctx context.Context, s *Service) error {
	list, err := s.cfg.Storage.ListEntries(ctx, leavingPartition, "", "", sweepEntries)
	if err != nil {
		return err
	}
	for i := range list {
		var d leavingDoc
		if ok, err := s.readSigned(ctx, &list[i], &d); err != nil || !ok {
			return errors.Join(err, errors.New("mailboxes: a disconnect entry that does not verify"))
		}
		if err := s.cfg.Storage.DeleteEntry(ctx, leavingPartition, list[i].Key); err != nil {
			return err
		}
		if err := s.putSigned(ctx, leavingPartition, list[i].Key, leavingDoc{Mailbox: d.Mailbox}); err != nil {
			return err
		}
	}
	return nil
}
