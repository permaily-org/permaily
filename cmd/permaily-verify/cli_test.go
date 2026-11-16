package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/buildinfo"
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/nsm/fakensm"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// playedEnclave is an enclave on the test machine: the real /attestation
// handler over a fake NSM, bound to its TLS key, and a /status whose model
// says the prompt cache is cache. reads counts the /status reads.
type playedEnclave struct {
	fake  *fakensm.Fake
	srv   *httptest.Server
	cache atomic.Value
	reads atomic.Int32
	dir   string
	// canary is the SHA-256 of the canary file written in dir, which the
	// model's baseline is.
	canary string
}

func newPlayedEnclave(t *testing.T) *playedEnclave {
	t.Helper()
	fake, err := fakensm.New()
	if err != nil {
		t.Fatal(err)
	}
	old := buildinfo.Commit
	buildinfo.Commit = strings.Repeat("c", 40)
	t.Cleanup(func() { buildinfo.Commit = old })
	e := &playedEnclave{fake: fake, dir: t.TempDir()}
	e.cache.Store("off")
	canary := []byte(`{"cases":[]}`)
	e.canary = sha(canary)
	mux := http.NewServeMux()
	e.srv = httptest.NewTLSServer(mux)
	t.Cleanup(e.srv.Close)
	mux.Handle("/attestation", httpapi.Attestation(fake, func() []byte { return attestation.SPKIHash(e.srv.Certificate()) }))
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		e.reads.Add(1)
		_ = json.NewEncoder(w).Encode(attestation.Status{Commit: buildinfo.Commit, Model: modelStatus(func(m *attestation.ModelStatus) {
			m.PromptCache = e.cache.Load().(string)
			m.Canary.Baseline = e.canary
		})})
	})
	pcrs := map[string]string{}
	for i, v := range fake.PCRs {
		pcrs[fmt.Sprintf("PCR%d", i)] = hex.EncodeToString(v)
	}
	data, err := json.Marshal(pcrs)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, b []byte) {
		if err := os.WriteFile(filepath.Join(e.dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("pcrs.json", data)
	write("ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: e.srv.Certificate().Raw}))
	write("canary.json", canary)
	return e
}

// cli runs the command against the played enclave with the flags given and
// returns its exit status and what it printed as JSON.
func (e *playedEnclave) cli(t *testing.T, flags ...string) (int, struct {
	OK    bool         `json:"ok"`
	Hosts []hostResult `json:"hosts"`
}) {
	t.Helper()
	args := append([]string{"-json", "-pcrs", filepath.Join(e.dir, "pcrs.json"), "-ca-file", filepath.Join(e.dir, "ca.pem"), "-canary", filepath.Join(e.dir, "canary.json")}, flags...)
	var stdout, stderr bytes.Buffer
	code := run(append(args, e.srv.URL), &stdout, &stderr, e.fake.CA.Root)
	var out struct {
		OK    bool         `json:"ok"`
		Hosts []hostResult `json:"hosts"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil || len(out.Hosts) != 1 {
		t.Fatalf("exit %d, output %q, errors %q: %v", code, stdout.String(), stderr.String(), err)
	}
	return code, out
}

// The model is checked whatever -skip-kms-policy and -skip-certificates
// say, together or alone: they skip what their names say, never the
// prompt cache, the model's section, its baseline or its pinning.
func TestTheModelIsCheckedWhateverIsSkipped(t *testing.T) {
	e := newPlayedEnclave(t)
	for _, flags := range [][]string{nil, {"-skip-kms-policy"}, {"-skip-certificates"}, {"-skip-kms-policy", "-skip-certificates"}} {
		name := strings.Join(flags, " ")
		if name == "" {
			name = "no skip"
		}
		t.Run(name, func(t *testing.T) {
			e.cache.Store("on")
			before := e.reads.Load()
			code, out := e.cli(t, flags...)
			if code != 1 || out.OK || out.Hosts[0].OK || !strings.HasPrefix(out.Hosts[0].Error, "model: ") || !strings.Contains(out.Hosts[0].Error, "prompt cache") {
				t.Errorf("a prompt cache on: exit %d, %+v", code, out)
			}
			if n := e.reads.Load() - before; n != 1 {
				t.Errorf("a prompt cache on: /status read %d times, want 1", n)
			}

			e.cache.Store("off")
			before = e.reads.Load()
			code, out = e.cli(t, flags...)
			if code != 0 || !out.OK || out.Hosts[0].Model == nil || out.Hosts[0].Model.PromptCache != "off" || len(out.Hosts[0].Model.Warnings) != 0 {
				t.Errorf("a prompt cache off: exit %d, %+v, model %+v", code, out, out.Hosts[0].Model)
			}
			if n := e.reads.Load() - before; n != 1 {
				t.Errorf("a prompt cache off: /status read %d times, want 1", n)
			}
		})
	}
}

// The canary is read at the commit the attested enclave runs, and a
// baseline that is not its canary is warned of, not failed.
func TestTheCanaryIsReadAtTheAttestedCommit(t *testing.T) {
	e := newPlayedEnclave(t)
	var commit string
	r := verify(context.Background(), e.srv.Client(), e.srv.URL, e.fake.PCRs, kmsOptions{skip: true, now: time.Now(), root: e.fake.CA.Root, canary: func(_ context.Context, c string) (string, error) {
		commit = c
		return "not the baseline", nil
	}})
	if !r.OK || commit != buildinfo.Commit || r.Model == nil || len(r.Model.Warnings) != 1 || !strings.Contains(r.Model.Warnings[0], "not the baseline") {
		t.Fatalf("commit %q, %+v, model %+v", commit, r, r.Model)
	}
}

// Asking for help prints it and exits 0, as flag.ExitOnError did; a flag
// that does not exist still exits 2.
func TestHelpExitsZero(t *testing.T) {
	for _, c := range []struct {
		args []string
		want int
	}{{[]string{"-h"}, 0}, {[]string{"--help"}, 0}, {[]string{"-no-such-flag"}, 2}} {
		var stdout, stderr bytes.Buffer
		if got := run(c.args, &stdout, &stderr, nil); got != c.want {
			t.Errorf("%v exits %d, want %d", c.args, got, c.want)
		}
		if c.want == 0 && !strings.Contains(stderr.String()+stdout.String(), "usage: permaily-verify") {
			t.Errorf("%v printed no usage", c.args)
		}
	}
}
