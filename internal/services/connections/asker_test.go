package connections

import (
	"context"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/store"
)

// modelAsker is an Asker the fake Discloser never calls: the test checks
// only which one each call's context carried.
type modelAsker struct{}

func (modelAsker) Ask(context.Context, *inbox.Question) (*inbox.Asked, error) {
	return nil, contracts.ErrModelUnavailable
}

// The Discloser asks the model only through the Asker its context carries
// (inbox.WithAsker), so the service hands it Config.Asker, for the read's
// account, on every call whose disclosure may ask the model: a search, a
// read, an attachment and a preview that asks for the model. Without it
// every message the default rule needs the model for is held and every
// read is model_unavailable (PR #147, ruling 1). A preview that does not
// ask for the model, and a service with no Asker, carry none.
func TestTheDiscloserIsGivenTheServicesAsker(t *testing.T) {
	ctx := context.Background()
	for _, withAsker := range []bool{true, false} {
		name := "without an asker"
		if withAsker {
			name = "with an asker"
		}
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			cfg := w.config(&store.Anchored{Records: w.mem, Commits: w.commits, Strict: true, Now: w.clock.now})
			asker := modelAsker{}
			if withAsker {
				cfg.Asker = asker
			}
			s, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			x := w.ask(claudeCode)
			x.scope = "mail.sender mail.subject mail.body mail.attachments"
			tok, _ := w.connect(s, w.person(accountA), x)
			g := w.grant(s, x, tok.AccessToken)

			w.disc.mu.Lock()
			w.disc.askings = nil
			w.disc.mu.Unlock()
			if _, rel, err := s.Search(ctx, g, contracts.VendorSearch{}); err != nil {
				t.Fatal(err)
			} else {
				rel.Finish(false)
			}
			if _, rel, _ := s.Read(ctx, g, mailboxID(1)+".m001"); rel != nil {
				rel.Finish(false)
			}
			if _, rel, _ := s.Attachment(ctx, g, mailboxID(1)+".m001", 0); rel != nil {
				rel.Finish(false)
			}
			p := w.person(accountA)
			req := w.start(s, p, w.ask(claudeCode))
			if _, err := s.Preview(ctx, p, req.ID, contracts.ConsentPreview{Draft: req.Draft, Mailbox: mailboxID(1), AskModel: true}); err != nil {
				t.Fatal(err)
			}
			w.disc.mu.Lock()
			asked := append([]askingSeen(nil), w.disc.askings...)
			w.disc.askings = nil
			w.disc.mu.Unlock()
			if len(asked) < 4 {
				t.Fatalf("the Discloser was called %d times, want a search's pages, a read, an attachment and a preview", len(asked))
			}
			for i, a := range asked {
				if !withAsker {
					if a.ok {
						t.Fatalf("call %d carried an Asker the service was not given", i)
					}
					continue
				}
				if !a.ok || a.Asker != asker || a.Account != accountA {
					t.Fatalf("call %d carried %+v, want the service's Asker for the read's account", i, a)
				}
			}

			if _, err := s.Preview(ctx, p, req.ID, contracts.ConsentPreview{Draft: req.Draft, Mailbox: mailboxID(1)}); err != nil {
				t.Fatal(err)
			}
			w.disc.mu.Lock()
			off := append([]askingSeen(nil), w.disc.askings...)
			w.disc.mu.Unlock()
			if len(off) != 1 || off[0].ok {
				t.Fatalf("a preview that does not ask for the model carried an Asker: %+v", off)
			}
		})
	}
}
