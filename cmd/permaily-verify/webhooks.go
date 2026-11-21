package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/webhook"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// webhooksResult is what the verified enclave reports of connections'
// webhooks (ADR 0039, "What /status and permaily-verify show"), after it
// passed checkWebhooks and its key set, fetched over the attested
// connection, was found to be the one /status reports.
type webhooksResult struct {
	*attestation.WebhooksStatus
	// Published is true once the key set at KeysURL was fetched over the
	// attested key and found equal to Keys.
	Published bool `json:"published"`
}

// webhooksWant is what an enclave of this verifier's release must publish
// of webhooks, from the source this verifier is built from: the revision
// of Standard Webhooks it signs as, the key set on the deployment's api
// host (domain), the bounds and the schedules. The keys and the counts are
// the enclave's own (attestation.WebhooksStatus.Check).
func webhooksWant(domain string) attestation.WebhooksStatus {
	return attestation.WebhooksStatus{
		SpecRevision:          webhook.SpecRevision,
		KeysURL:               "https://api." + domain + webhook.KeysPath,
		MinIntervalSeconds:    int(webhook.MinInterval / time.Second),
		TimeoutSeconds:        int(webhook.AttemptTimeout / time.Second),
		GiveUpSeconds:         int(webhook.GiveUpAfter / time.Second),
		DedupSeconds:          int(webhook.DedupKeepFor / time.Second),
		HostInFlight:          webhook.HostInFlightMax,
		HostAttemptsPerMinute: webhook.HostAttemptsPerMinute,
		Schedules:             []string{string(contracts.EveryDay), string(contracts.EveryWeek)},
	}
}

// checkWebhooks checks what the enclave reports of webhooks against what
// the design promises (attestation.WebhooksStatus.Check, with the
// source's revision, bounds and schedules, and the key set on the
// deployment's api host): it fails when a notice is not signed as
// Standard Webhooks' "v2a" at the revision this verifier pins, may hold
// mail, may go to another host than its vendor's, follows a redirect, is
// sent other than over HTTP/2, tells of new mail before the rules, or may
// still be sent more than five seconds after a revocation.
func checkWebhooks(w *attestation.WebhooksStatus, domain string) (*webhooksResult, error) {
	if domain == "" {
		return nil, errors.New("the deployment's domain is not known, so where its key set is published cannot be checked")
	}
	if err := w.Check(webhooksWant(domain)); err != nil {
		return nil, err
	}
	return &webhooksResult{WebhooksStatus: w}, nil
}

// fetchAttested gets a URL over the attested key: attestation.FetchAttested
// in permaily-verify, a test's in its tests.
type fetchAttested func(ctx context.Context, client *http.Client, url string, spkiHash []byte) ([]byte, error)

// checkPublishedKeys fetches the key set w names (KeysURL) over the
// connection whose TLS key is the attested one (spki), reads it exactly
// (webhook.ParseKeySet) and fails unless it holds exactly the keys /status
// reports: so a notice a vendor verifies with the published keys was
// signed by the attested image.
func checkPublishedKeys(ctx context.Context, fetch fetchAttested, client *http.Client, w *webhooksResult, spki []byte) error {
	b, err := fetch(ctx, client, w.KeysURL, spki)
	if err != nil {
		return fmt.Errorf("the key set at %s: %w", w.KeysURL, err)
	}
	keys, err := webhook.ParseKeySet(b)
	if err != nil {
		return fmt.Errorf("the key set at %s is not a JWK set of Ed25519 keys", w.KeysURL)
	}
	published := make(map[string]string, len(keys))
	for id, k := range keys {
		published[id] = base64.RawURLEncoding.EncodeToString(k)
	}
	if err := w.SameWebhookKeys(published); err != nil {
		return err
	}
	w.Published = true
	return nil
}

// printWebhooks describes how the enclave calls webhooks.
func printWebhooks(w io.Writer, h *webhooksResult) {
	if h == nil {
		return
	}
	var keys []string
	for _, k := range h.Keys {
		keys = append(keys, k.ID+" "+k.X)
	}
	published := "not fetched"
	if h.Published {
		published = "the same keys, fetched over the attested key"
	}
	fmt.Fprintf(w, "      webhooks: notices signed %s (Standard Webhooks at %s) with %s; key set at %s: %s\n",
		h.Signature, h.SpecRevision, strings.Join(keys, ", "), h.KeysURL, published)
	fmt.Fprintf(w, "      a notice holds no mail; only the vendor's own host, no redirect, over %s; new mail %s; stopped within %ds of a revocation\n",
		h.Transport, strings.ReplaceAll(h.NewMail, "_", " "), h.RevocationSeconds)
	fmt.Fprintf(w, "      one notice a connection every %ds, %ds a call, given up after %ds; a vendor keeps identifiers %ds; %d calls at once and %d a minute to one host; schedules %s\n",
		h.MinIntervalSeconds, h.TimeoutSeconds, h.GiveUpSeconds, h.DedupSeconds, h.HostInFlight, h.HostAttemptsPerMinute, strings.Join(h.Schedules, ", "))
	var calls, skipped []string
	for _, k := range attestation.WebhookOutcomes {
		if n := h.Outcomes[k]; n != 0 {
			calls = append(calls, fmt.Sprintf("%s %d", k, n))
		}
	}
	for _, k := range attestation.WebhookSkips {
		if n := h.Skipped[k]; n != 0 {
			skipped = append(skipped, fmt.Sprintf("%s %d", k, n))
		}
	}
	fmt.Fprintf(w, "      this enclave since %s: calls %s; notices not sent %s\n", h.Since.UTC().Format(time.RFC3339), orNone(calls), orNone(skipped))
}

func orNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ", ")
}
