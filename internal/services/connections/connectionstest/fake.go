// Package connectionstest is the stand-in for the connections service (ADR
// 0030's pull request 3) that the routes (pull request 6) and MCP (pull
// request 7) are built against at the same time: a connections.API whose
// every method is a function a test sets, recording what it was called
// with. A method a test did not set answers errNotSet. Never imported
// outside tests (depguard).
package connectionstest

import (
	"context"
	"errors"
	"net/url"
	"sync"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/connections"
)

// ErrNotSet is the answer of a method the test did not set.
var ErrNotSet = errors.New("connectionstest: the test did not set this method")

// Fake is a connections.API of functions.
type Fake struct {
	StartFunc        func(ctx context.Context, p connections.Person, query string) (*contracts.AuthorizationStarted, error)
	PreviewFunc      func(ctx context.Context, p connections.Person, id string, in contracts.ConsentPreview) (*contracts.ConsentPreviewPage, error)
	DecideFunc       func(ctx context.Context, p connections.Person, id string, in contracts.AuthorizationDecision) (*contracts.AuthorizationAnswer, error)
	ListFunc         func(ctx context.Context, p connections.Person) (*contracts.ConnectionList, error)
	GetFunc          func(ctx context.Context, p connections.Person, id string) (*contracts.Connection, error)
	RevokeFunc       func(ctx context.Context, p connections.Person, id string) (*contracts.Connection, error)
	RevokeDeviceFunc func(ctx context.Context, p connections.Person, id, device string) (*contracts.Connection, error)
	RenewFunc        func(ctx context.Context, p connections.Person, id string, in contracts.ConnectionRenewal) (*contracts.Connection, error)
	PushFunc         func(ctx context.Context, form url.Values) (string, error)
	TokenFunc        func(ctx context.Context, form url.Values) (*connections.TokenAnswer, error)
	RevokeTokenFunc  func(ctx context.Context, form url.Values) error
	CheckFunc        func(ctx context.Context, r contracts.Resource, bearer string) (*oauth.Grant, error)
	InfoFunc         func(ctx context.Context, g *oauth.Grant) (*contracts.VendorConnection, error)
	SearchFunc       func(ctx context.Context, g *oauth.Grant, in contracts.VendorSearch) (*contracts.VendorPage, connections.Release, error)
	ReadFunc         func(ctx context.Context, g *oauth.Grant, id string) (*contracts.VendorMessage, connections.Release, error)
	AttachmentFunc   func(ctx context.Context, g *oauth.Grant, id string, index int) (*inbox.Attachment, connections.Release, error)
	RefusedFunc      func() map[string]int64

	mu    sync.Mutex
	calls []string
}

var _ connections.API = (*Fake)(nil)

func (f *Fake) called(name string) {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	f.mu.Unlock()
}

// Calls are the methods called, in order.
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *Fake) Start(ctx context.Context, p connections.Person, query string) (*contracts.AuthorizationStarted, error) {
	f.called("Start")
	if f.StartFunc == nil {
		return nil, ErrNotSet
	}
	return f.StartFunc(ctx, p, query)
}

func (f *Fake) Preview(ctx context.Context, p connections.Person, id string, in contracts.ConsentPreview) (*contracts.ConsentPreviewPage, error) {
	f.called("Preview")
	if f.PreviewFunc == nil {
		return nil, ErrNotSet
	}
	return f.PreviewFunc(ctx, p, id, in)
}

func (f *Fake) Decide(ctx context.Context, p connections.Person, id string, in contracts.AuthorizationDecision) (*contracts.AuthorizationAnswer, error) {
	f.called("Decide")
	if f.DecideFunc == nil {
		return nil, ErrNotSet
	}
	return f.DecideFunc(ctx, p, id, in)
}

func (f *Fake) List(ctx context.Context, p connections.Person) (*contracts.ConnectionList, error) {
	f.called("List")
	if f.ListFunc == nil {
		return nil, ErrNotSet
	}
	return f.ListFunc(ctx, p)
}

func (f *Fake) Get(ctx context.Context, p connections.Person, id string) (*contracts.Connection, error) {
	f.called("Get")
	if f.GetFunc == nil {
		return nil, ErrNotSet
	}
	return f.GetFunc(ctx, p, id)
}

func (f *Fake) Revoke(ctx context.Context, p connections.Person, id string) (*contracts.Connection, error) {
	f.called("Revoke")
	if f.RevokeFunc == nil {
		return nil, ErrNotSet
	}
	return f.RevokeFunc(ctx, p, id)
}

func (f *Fake) RevokeDevice(ctx context.Context, p connections.Person, id, device string) (*contracts.Connection, error) {
	f.called("RevokeDevice")
	if f.RevokeDeviceFunc == nil {
		return nil, ErrNotSet
	}
	return f.RevokeDeviceFunc(ctx, p, id, device)
}

func (f *Fake) Renew(ctx context.Context, p connections.Person, id string, in contracts.ConnectionRenewal) (*contracts.Connection, error) {
	f.called("Renew")
	if f.RenewFunc == nil {
		return nil, ErrNotSet
	}
	return f.RenewFunc(ctx, p, id, in)
}

func (f *Fake) Push(ctx context.Context, form url.Values) (string, error) {
	f.called("Push")
	if f.PushFunc == nil {
		return "", ErrNotSet
	}
	return f.PushFunc(ctx, form)
}

func (f *Fake) Token(ctx context.Context, form url.Values) (*connections.TokenAnswer, error) {
	f.called("Token")
	if f.TokenFunc == nil {
		return nil, ErrNotSet
	}
	return f.TokenFunc(ctx, form)
}

func (f *Fake) RevokeToken(ctx context.Context, form url.Values) error {
	f.called("RevokeToken")
	if f.RevokeTokenFunc == nil {
		return ErrNotSet
	}
	return f.RevokeTokenFunc(ctx, form)
}

func (f *Fake) Check(ctx context.Context, r contracts.Resource, bearer string) (*oauth.Grant, error) {
	f.called("Check")
	if f.CheckFunc == nil {
		return nil, ErrNotSet
	}
	return f.CheckFunc(ctx, r, bearer)
}

func (f *Fake) Info(ctx context.Context, g *oauth.Grant) (*contracts.VendorConnection, error) {
	f.called("Info")
	if f.InfoFunc == nil {
		return nil, ErrNotSet
	}
	return f.InfoFunc(ctx, g)
}

func (f *Fake) Search(ctx context.Context, g *oauth.Grant, in contracts.VendorSearch) (*contracts.VendorPage, connections.Release, error) {
	f.called("Search")
	if f.SearchFunc == nil {
		return nil, connections.Release{}, ErrNotSet
	}
	return f.SearchFunc(ctx, g, in)
}

func (f *Fake) Read(ctx context.Context, g *oauth.Grant, id string) (*contracts.VendorMessage, connections.Release, error) {
	f.called("Read")
	if f.ReadFunc == nil {
		return nil, connections.Release{}, ErrNotSet
	}
	return f.ReadFunc(ctx, g, id)
}

func (f *Fake) Attachment(ctx context.Context, g *oauth.Grant, id string, index int) (*inbox.Attachment, connections.Release, error) {
	f.called("Attachment")
	if f.AttachmentFunc == nil {
		return nil, connections.Release{}, ErrNotSet
	}
	return f.AttachmentFunc(ctx, g, id, index)
}

func (f *Fake) Refused() map[string]int64 {
	f.called("Refused")
	if f.RefusedFunc == nil {
		return map[string]int64{}
	}
	return f.RefusedFunc()
}
