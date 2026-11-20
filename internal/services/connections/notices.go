package connections

import (
	"context"
	"slices"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/services/notices"
)

// The notices about connections (ADR 0041, pull request 3): a connection
// approved or replacing the client's, a device added, a connection renewed,
// one about to end and a device ended because a token of it was used again.
// Each is queued only after the change it tells of committed, so a write
// that lost, was refused or was undone queues nothing, under the key its
// event names (ADR 0041, "Which notices"): an approval's and a renewal's
// consent receipt, a connection and its end, a connection and its device.
// The words are accounts' (notices.Deliverer); the service gives only the
// facts its kind's words say. A queue that cannot be written fails nothing
// of the change and holds the caller at most notices.EnqueueMax: it is
// counted and logged by its kind alone, as a notice that could not be sent
// today (a crash between the commit and the queue loses it, as ADR 0041's
// "The cut before the queue" accepts).

// notify queues n within the service's enqueueMax, whatever the caller's
// own context still allows (the change committed: its notice is owed even
// when the person's request has gone), and says whether it was queued.
// Without a queue it does nothing.
func (s *Service) notify(ctx context.Context, n notices.Notice) bool {
	if s.cfg.Notices == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.enqueueMax)
	defer cancel()
	if err := s.cfg.Notices.Enqueue(ctx, n); err != nil {
		s.count("notice_not_queued")
		s.cfg.Log.Warn("connections: a notice could not be queued", "kind", string(n.Kind))
		return false
	}
	return true
}

// notifyApproved tells of an approval that committed: a connection made or
// replacing the client's (connection_approved), or a device added to one
// (device_added), keyed by its consent receipt.
func (s *Service) notifyApproved(ctx context.Context, m *made, receipt string) {
	d := m.conn
	n := notices.Notice{Account: d.Account, At: s.now(), Facts: notices.Facts{Vendor: d.Vendor, EndsAt: d.EndsAt}}
	if m.change == contracts.ChangeAddDevice {
		n.Kind = notices.KindDeviceAdded
		n.Facts.Parts = d.Parts
		if dev := d.device(m.device); dev != nil {
			n.Facts.Parts = dev.Parts
		}
	} else {
		n.Kind = notices.KindConnectionApproved
		n.Facts.Resource, n.Facts.Parts, n.Facts.Replaced = d.Resource, d.Parts, m.change == contracts.ChangeReplace
	}
	n.Facts.Parts = slices.Clone(n.Facts.Parts)
	n.Key = notices.Key(d.Account, n.Kind, receipt)
	s.notify(ctx, n)
}

// notifyRenewed tells of a renewal whose write committed, with its new end,
// keyed by its consent receipt.
func (s *Service) notifyRenewed(ctx context.Context, d *connDoc, receipt string) {
	s.notify(ctx, notices.Notice{
		Account: d.Account, Kind: notices.KindConnectionRenewed, Key: notices.Key(d.Account, notices.KindConnectionRenewed, receipt),
		At: s.now(), Facts: notices.Facts{Vendor: d.Vendor, EndsAt: d.EndsAt},
	})
}

// notifyEndedReuse tells of a device this enclave ended because a code or
// a refresh token of it was used again, keyed by the connection and the
// device: once per device, whichever token and enclave saw it.
func (s *Service) notifyEndedReuse(ctx context.Context, d *connDoc, device string, at time.Time) {
	s.notify(ctx, notices.Notice{
		Account: d.Account, Kind: notices.KindDeviceEndedReuse, Key: notices.Key(d.Account, notices.KindDeviceEndedReuse, d.ID, device),
		At: at, Facts: notices.Facts{Vendor: d.Vendor},
	})
}

// endingQueuedMax bounds the keys of endings this enclave remembers it
// queued; past it they are forgotten, and the next pass queues them again,
// which the queue takes once by their key.
const endingQueuedMax = 10000

// notifyEnding queues, from the hourly sweep, the notice that an active
// connection of an account not closing ends within notices.EndingBefore
// (notices.EndingDue), keyed by the connection and its end: a renewal
// moves the end, and the new one is told of in its turn. The keys this
// enclave queued since it started are remembered, so a later pass queues
// nothing for them again.
func (s *Service) notifyEnding(ctx context.Context, d *connDoc, now time.Time) {
	if s.cfg.Notices == nil || d.State != contracts.ConnectionActive || !notices.EndingDue(d.Days, d.EndsAt, now) {
		return
	}
	key := notices.EndingKey(d.Account, d.ID, d.EndsAt)
	s.mu.Lock()
	queued := s.endings[key]
	s.mu.Unlock()
	if queued {
		return
	}
	n := notices.Notice{Account: d.Account, Kind: notices.KindConnectionEnding, Key: key, At: now, Facts: notices.Facts{Vendor: d.Vendor, EndsAt: d.EndsAt}}
	if !s.notify(ctx, n) {
		return
	}
	s.mu.Lock()
	if len(s.endings) >= endingQueuedMax {
		clear(s.endings)
	}
	s.endings[key] = true
	s.mu.Unlock()
}
