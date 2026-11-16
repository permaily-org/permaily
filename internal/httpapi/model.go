package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/services/accounts"
)

// The routes of the model (ADR 0028): asking it from the owner's previews
// (ask_model on the matches route, mailboxes.go), the person's agreement to
// its notice (POST /api/v1/account/model-notice, accounts.go), and the
// operator's key (/v1/admin/model-key, here). None of them logs or answers
// the key, a topic, a subject or a text.

// ModelNotices says whether the account behind a session agreed to the
// model's notice in force; *accounts.Service implements it
// (ModelNoticeAgreed), reading the account's record as it is now, so an
// agreement given through one enclave counts through the other.
type ModelNotices interface {
	ModelNoticeAgreed(ctx context.Context, who *accounts.Authenticated) (bool, error)
}

// noticeAgreed answers model_notice_needed, and false, unless the account
// agreed to the model's notice in force. Without ModelNotices no agreement
// can be read: the model is never asked on an agreement nobody checked.
func (a *mailboxesAPI) noticeAgreed(w http.ResponseWriter, r *http.Request, who *accounts.Authenticated) bool {
	if a.cfg.ModelNotices == nil {
		failed(w, r, a.cfg.Log, contracts.ErrModelNoticeNeeded)
		return false
	}
	agreed, err := a.cfg.ModelNotices.ModelNoticeAgreed(r.Context(), who)
	if err != nil {
		a.fail(w, r, err)
		return false
	}
	if !agreed {
		failed(w, r, a.cfg.Log, contracts.ErrModelNoticeNeeded)
		return false
	}
	return true
}

// asker is the Asker a page that asks for the model uses: the configured
// one, or modelOff when there is none.
func (a *mailboxesAPI) asker() inbox.Asker {
	if a.cfg.Asker == nil {
		return modelOff{}
	}
	return a.cfg.Asker
}

// modelOff is the model with no key: it asks nothing and holds every atom
// (inbox.AskOff), so a page says MatchModelOff and lists the messages that
// need the model in needs_model, as the service does with no key set.
type modelOff struct{}

func (modelOff) Ask(_ context.Context, q *inbox.Question) (*inbox.Asked, error) {
	return &inbox.Asked{Held: q.Atoms, Outcome: inbox.AskOff}, nil
}

// ModelKeyService is the model's key as the operator's routes see it
// (*decisions.Service): set or replace it, remove it, and whether one is
// set, since when and its fingerprint. Nothing here returns the key.
type ModelKeyService interface {
	SetKey(ctx context.Context, in contracts.ModelKeySet) (*contracts.ModelKey, error)
	RemoveKey(ctx context.Context) error
	Key(ctx context.Context) (*contracts.ModelKey, error)
}

// ModelKeyCounter counts what the operator did to the key, by kind, for
// /status (*accounts.Service, ModelKeyChanged: model_key_set and
// model_key_removed in attestation.AdminActions): once for each PUT or
// DELETE that succeeded, so a DELETE when no key is set counts too. They
// count the operator's requests, not changes from a key to none.
type ModelKeyCounter interface {
	ModelKeyChanged(set bool)
}

// maxModelKeyBody bounds the body of PUT /v1/admin/model-key: a key of the
// largest size the service takes (1,024 characters of visible ASCII), every
// character of it escaped, and room. A body past it is invalid_body, and
// nothing of it is read.
const maxModelKeyBody = 8 << 10

// ModelKey returns the operator's routes of the model's key (ADR 0028, "The
// key"): PUT /v1/admin/model-key {key} gives the enclave the provider's API
// key, sealed by the service and never answered back, and answers what GET
// answers; DELETE takes it away (204), and the model is off from then; GET
// says whether one is set, since when, and its fingerprint. Put it behind
// TokenAuth with the operator's token, on a connection the operator verified
// and pinned to the attested key (scripts/model-key.sh): the key is sent to
// whoever answers.
//
// The body is read strictly: one JSON object of the one member key, a
// string, with no member twice and nothing after it, at most
// maxModelKeyBody bytes; anything else is invalid_body. A key the service
// refuses is its error (invalid_model_key), in fixed words. A set or a
// removal that succeeded is counted by its kind (counter; nil counts
// nothing, for a test) and logged by the service by its kind alone. Nothing here logs the key, the body or the
// words of an error: a failure of the service is logged as a fixed line.
func ModelKey(svc ModelKeyService, counter ModelKeyCounter, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	const path = "/v1/admin/model-key"
	mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
		out, err := svc.Key(r.Context())
		if err != nil {
			keyFailed(w, r, log, err)
			return
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("PUT "+path, func(w http.ResponseWriter, r *http.Request) {
		in, ok := readModelKey(w, r)
		if !ok {
			return
		}
		out, err := svc.SetKey(r.Context(), in)
		if err != nil {
			keyFailed(w, r, log, err)
			return
		}
		if counter != nil {
			counter.ModelKeyChanged(true)
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("DELETE "+path, func(w http.ResponseWriter, r *http.Request) {
		if err := svc.RemoveKey(r.Context()); err != nil {
			keyFailed(w, r, log, err)
			return
		}
		if counter != nil {
			counter.ModelKeyChanged(false)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc(path, refuse("GET, PUT, DELETE"))
	mux.HandleFunc("/", noSuchEndpoint)
	return mux
}

// readModelKey reads the body of PUT /v1/admin/model-key strictly, or
// answers invalid_body.
func readModelKey(w http.ResponseWriter, r *http.Request) (contracts.ModelKeySet, bool) {
	members, ok := readObjectUpTo(w, r, maxModelKeyBody, "key")
	if !ok {
		return contracts.ModelKeySet{}, false
	}
	raw, ok := members["key"]
	var in contracts.ModelKeySet
	if !ok || len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &in.Key) != nil {
		writeInvalidBody(w)
		return contracts.ModelKeySet{}, false
	}
	return in, true
}

// keyFailed answers a failure of the key's service: one the operator can
// act on with its code and fixed words, anything else as internal, logged as
// a fixed line without the error's words.
func keyFailed(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	if _, ok := errors.AsType[*contracts.Error](err); ok {
		failed(w, r, log, err)
		return
	}
	if !clientLeft(r, err) {
		log.Error("api: the model's key could not be read or changed")
	}
	writeError(w, http.StatusInternalServerError, "internal", "internal error")
}
