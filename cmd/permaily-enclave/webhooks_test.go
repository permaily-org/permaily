package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/router"
	"github.com/permaily-org/permaily/internal/services/webhooks"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/webhook"
	"github.com/permaily-org/permaily/internal/webhook/webhooktest"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// rootKeys derives every purpose's key from one root secret, as keys.Root
// does: two enclaves, or two releases, given the same secret derive the
// same keys.
type rootKeys struct{ secret string }

func (k rootKeys) Derive(_ context.Context, purpose string) ([]byte, error) {
	sum := sha256.Sum256([]byte(k.secret + "\x00" + purpose))
	return sum[:], nil
}

// testWebhooks composes the webhooks over mem with the root secret secret.
func testWebhooks(t *testing.T, mem store.Entries, secret string, sender webhook.Sender) *composedWebhooks {
	t.Helper()
	c, err := composeWebhooks(webhooksParts{
		entries: mem, keys: rootKeys{secret}, sender: sender, keysURL: webhookKeysURL(router.HostsForDomain("permaily.com"), 443),
		now: func() time.Time { return time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// K57 (ADR 0039, "The image"): the composition needs the table, the root
// secret's keys and a sender; the signing key is the root secret's
// (webhook.KeyPurpose), the same in two enclaves and in a later release
// that receives the secret, another under another secret; the key set is
// published on the api host; and the queue reads no connection before the
// connections service is given to it.
func TestK57TheWebhooksComposition(t *testing.T) {
	sender := &webhooktest.Sender{}
	for name, p := range map[string]webhooksParts{
		"no table":  {keys: rootKeys{"r"}, sender: sender},
		"no keys":   {entries: store.NewMemory(), sender: sender},
		"no sender": {entries: store.NewMemory(), keys: rootKeys{"r"}},
	} {
		if _, err := composeWebhooks(p); !errors.Is(err, errWebhooksParts) {
			t.Errorf("%s: %v", name, err)
		}
	}
	mem := store.NewMemory()
	a, b := testWebhooks(t, mem, "the root secret", sender), testWebhooks(t, mem, "the root secret", sender)
	seed, _ := rootKeys{"the root secret"}.Derive(context.Background(), webhook.KeyPurpose)
	sign, err := webhook.KeyFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	setA, errA := a.svc.KeySet(context.Background())
	setB, errB := b.svc.KeySet(context.Background())
	keys, err := webhook.ParseKeySet(setA)
	if errA != nil || errB != nil || err != nil || !bytes.Equal(setA, setB) || len(keys) != 1 || !keys[webhook.KeyID].Equal(sign.Public().(ed25519.PublicKey)) {
		t.Fatalf("the key set %s, %s: %v %v %v", setA, setB, errA, errB, err)
	}
	other, _ := testWebhooks(t, mem, "another root secret", sender).svc.KeySet(context.Background())
	if bytes.Equal(other, setA) {
		t.Fatal("another root secret signs with the same key")
	}
	if got := webhookKeysURL(router.HostsForDomain("permaily.com"), 443); got != "https://api.permaily.com/.well-known/permaily-webhook-keys" {
		t.Fatalf("the key set's address: %s", got)
	}
	if got := webhookKeysURL(router.HostsForDomain("permaily.test"), 8443); got != "https://api.permaily.test:8443/.well-known/permaily-webhook-keys" {
		t.Fatalf("dev mode's: %s", got)
	}
	// Before the connections are given, nothing is read, so nothing is
	// called.
	ctx := context.Background()
	if _, err := a.conns.Subscription(ctx, "con_x"); !errors.Is(err, errNoConnections) {
		t.Fatal(err)
	}
	if _, p, err := a.conns.Admit(ctx, "con_x"); !errors.Is(err, errNoConnections) || p != nil {
		t.Fatal(err)
	}
	if _, err := a.conns.Pending(ctx, "con_x", webhooks.Progress{}); !errors.Is(err, errNoConnections) {
		t.Fatal(err)
	}
	if ok, err := a.conns.Recheck(ctx, "con_x", webhooks.Match{}); !errors.Is(err, errNoConnections) || ok {
		t.Fatal(err)
	}
	if _, err := a.conns.Webhooks(ctx, "", 10); !errors.Is(err, errNoConnections) {
		t.Fatal(err)
	}
	if err := a.conns.Stop(ctx, "con_x"); !errors.Is(err, errNoConnections) {
		t.Fatal(err)
	}
}

// K57: the composition gives the queue to the connections and the
// connections to the queue; the person's routes and the key set are
// mounted with them.
func TestK57TheConnectionsAndTheQueueAreGivenToEachOther(t *testing.T) {
	w := newConnectionsWorld(t)
	w.parts.webhooks = testWebhooks(t, w.mem, "the root secret", &webhooktest.Sender{})
	c, err := composeConnections(w.parts)
	if err != nil {
		t.Fatal(err)
	}
	if c.webhooks != w.parts.webhooks || c.webhooks.conns.c.Load() == nil {
		t.Fatal("the queue was not given the connections")
	}
	// Through the connections now: a connection that is none has no
	// webhook.
	if _, err := c.webhooks.conns.Subscription(context.Background(), "con_"+strings.Repeat("a", 26)); errors.Is(err, errNoConnections) || err == nil {
		t.Fatalf("a connection that is none: %v", err)
	}
	src, err := os.ReadFile("connections.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"cfg.Webhooks, cfg.WebhookStates = c.webhooks.svc, c.webhooks.svc",
		"c.webhooks.readFrom(svc)",
		"cfg.Webhooks, cfg.WebhookKeys = c.svc, c.webhooks.svc",
	} {
		if strings.Count(string(src), want) != 1 {
			t.Errorf("connections.go does not say %q once", want)
		}
	}
	main, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"runWebhooks(ctx, p.connections.webhooks)", "stopWebhooks(p.connections.webhooks, log)", "status.Webhooks = p.connections.webhooks.status"} {
		if strings.Count(string(main), want) != 1 {
			t.Errorf("main.go does not say %q once", want)
		}
	}
	// Both platforms tell the queue of every message stored, and compose
	// the connections with it.
	for _, f := range []string{"platform_enclave.go", "platform_dev.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"Arrivals: p.webhooks.svc,", "webhooks: p.webhooks,", "p.webhooks, err = composeWebhooks(webhooksParts{"} {
			if strings.Count(string(src), want) != 1 {
				t.Errorf("%s does not say %q once", f, want)
			}
		}
		if strings.Index(string(src), "composeWebhooks(") > strings.Index(string(src), "mailboxes.New(") {
			t.Errorf("%s composes the webhooks after the mailboxes they are told by", f)
		}
	}
}

// K57: the runner is started and, at a stop, waited for within its bound;
// a runner that does not return says so.
func TestK57TheWebhooksRunAndAreWaitedForAtTheStop(t *testing.T) {
	c := testWebhooks(t, store.NewMemory(), "the root secret", &webhooktest.Sender{})
	var logs lockedLog
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	if err := stopWebhooksWithin(c, log, 10*time.Millisecond); err == nil || !strings.Contains(logs.String(), `"kind":"webhooks"`) {
		t.Fatalf("a runner never started is not waited for in vain: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runWebhooks(ctx, c)
	select {
	case <-c.done:
		t.Fatal("the runner returned before the stop")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	if err := stopWebhooks(c, log); err != nil {
		t.Fatal(err)
	}
	if webhooksStopMax >= stopMax || webhooksStopMax <= webhook.AttemptTimeout {
		t.Fatalf("the stop's bound %v", webhooksStopMax)
	}
}

// K57: the zones a schedule follows are compiled into the image
// (time/tzdata, the enclave has no zoneinfo on disk), in both builds, and
// every zone the consent screen's picker offers when the browser lists
// none loads.
func TestK57TheZonesAreInTheImage(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go is needed to list what is built: %v", err)
	}
	for _, tags := range []string{"", "dev"} {
		out, err := exec.Command(gobin, "list", "-tags", tags, "-deps", ".").Output()
		if err != nil {
			t.Fatalf("go list: %v", err)
		}
		if !regexp.MustCompile(`(?m)^time/tzdata$`).Match(out) {
			t.Fatalf("the image (tags %q) does not link time/tzdata", tags)
		}
	}
	src, err := os.ReadFile("../../web/src/pages/connections/words.tsx")
	if err != nil {
		t.Fatal(err)
	}
	_, list, ok := strings.Cut(string(src), "export const knownTimeZones: readonly string[] = [")
	list, _, ok2 := strings.Cut(list, "]")
	zones := regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(list, -1)
	if !ok || !ok2 || len(zones) < 20 {
		t.Fatalf("the picker's zones: %d", len(zones))
	}
	for _, z := range zones {
		if _, err := webhook.Zone(z[1]); err != nil {
			t.Errorf("%s: %v", z[1], err)
		}
	}
}

// K58 (ADR 0039, "What /status and permaily-verify show"): /status's
// webhooks, golden: signed as Standard Webhooks' "v2a" at the pinned
// revision, the root secret's key, the key set on the api host, no mail,
// the vendor's own host, no redirect, HTTP/2, new mail after the rules,
// five seconds, the bounds, the schedules, and every outcome and reason at
// zero; never a URL, a vendor, a subscription or a count of webhooks. It
// is permaily-verify's testdata, which passes its check (K58's other
// half).
func TestK58TheWebhooksOnStatus(t *testing.T) {
	c := testWebhooks(t, store.NewMemory(), "the root secret", &webhooktest.Sender{})
	st := c.status()
	got, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	seed, _ := rootKeys{"the root secret"}.Derive(context.Background(), webhook.KeyPurpose)
	sign, _ := webhook.KeyFromSeed(seed)
	x := base64.RawURLEncoding.EncodeToString(sign.Public().(ed25519.PublicKey))
	var outcomes, skipped []string
	for _, k := range sortedCopy(attestation.WebhookOutcomes) {
		outcomes = append(outcomes, fmt.Sprintf("%q:0", k))
	}
	for _, k := range sortedCopy(attestation.WebhookSkips) {
		skipped = append(skipped, fmt.Sprintf("%q:0", k))
	}
	want := `{"signature":"standard-webhooks-v2a","spec_revision":"3d65b1824b6697f05a5a4584e7062bd23512fc8e",` +
		`"keys":[{"id":"wk1","x":"` + x + `"}],"keys_url":"https://api.permaily.com/.well-known/permaily-webhook-keys",` +
		`"notice_holds_mail":false,"same_host":true,"redirects":"none","transport":"h2","new_mail":"after_rules","revocation_seconds":5,` +
		`"min_interval_seconds":60,"timeout_seconds":10,"give_up_seconds":259200,"dedup_seconds":259800,"host_in_flight":2,"host_attempts_per_minute":60,` +
		`"schedules":["day","week"],"since":"2026-11-20T12:00:00Z","outcomes":{` + strings.Join(outcomes, ",") + `},"skipped":{` + strings.Join(skipped, ",") + `}}`
	if string(got) != want {
		t.Fatalf("/status's webhooks:\n got %s\nwant %s", got, want)
	}
	golden, err := os.ReadFile("../permaily-verify/testdata/webhooks-status.json")
	var compact bytes.Buffer
	if err == nil {
		err = json.Compact(&compact, golden)
	}
	if err != nil || compact.String() != string(got) {
		t.Fatalf("permaily-verify's testdata/webhooks-status.json is not what the image says (%v): write it as\n%s", err, got)
	}
	// The key set the api host publishes is /status's.
	set, err := c.svc.KeySet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	keys, err := webhook.ParseKeySet(set)
	published := map[string]string{}
	for id, k := range keys {
		published[id] = base64.RawURLEncoding.EncodeToString(k)
	}
	if err != nil || st.SameWebhookKeys(published) != nil {
		t.Fatalf("the published set %s is not /status's: %v", set, err)
	}
}

func sortedCopy(s []string) []string {
	c := slices.Clone(s)
	slices.Sort(c)
	return c
}
