package idp_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/idp"
	"github.com/permaily-org/permaily/internal/idp/idptest"
)

// vector is one answer of a real provider that is public: an example of its
// documentation, or the keys it publishes. testdata/vectors holds them, each
// with where it comes from.
type vector struct {
	Name   string `json:"name"`
	Source struct {
		URL       string `json:"url"`
		File      string `json:"file"` // the file the page is made of, when it is public
		Retrieved string `json:"retrieved"`
		License   string `json:"license"`
	} `json:"source"`
	Provider idp.Provider `json:"provider"`
	Kind     string       `json:"kind"`
	Note     string       `json:"note"`

	// An ID token, as the text of its parts, or its claims alone.
	Header      string `json:"header"`
	Claims      string `json:"claims"`
	Signature   string `json:"signature"`
	SHA256      string `json:"sha256"`
	Application string `json:"application"`
	Nonce       string `json:"nonce"`
	Now         int64  `json:"now"`

	// Any other answer.
	Body string `json:"body"`

	Expect *struct {
		Subject  string   `json:"subject"`
		Address  string   `json:"address"`
		Verified bool     `json:"verified"`
		Name     string   `json:"name"`
		Usable   []string `json:"usable"`
	} `json:"expect"`
	// Refused is why an answer that must be refused is, and for nothing else.
	Refused string `json:"refused"`
}

func vectors(t testing.TB) []vector {
	t.Helper()
	files, err := filepath.Glob("testdata/vectors/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no vectors: %v", err)
	}
	var all []vector
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var v vector
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if v.Name+".json" != filepath.Base(file) || v.Source.URL == "" || v.Source.License == "" || v.Source.Retrieved == "" || v.Note == "" {
			t.Fatalf("%s: a vector says its name, where it comes from, when, under which licence, and what it is", file)
		}
		if (v.Expect == nil) == (v.Refused == "") {
			t.Fatalf("%s: a vector says what is expected of it, or why it is refused", file)
		}
		all = append(all, v)
	}
	return all
}

func issuerOf(p idp.Provider) string {
	if p == idp.Microsoft {
		return idptest.MicrosoftIssuer
	}
	return idptest.GoogleIssuer
}

// The answers real providers show in public are read as they are, or refused
// for the reason their file states.
func TestCapturedAnswers(t *testing.T) {
	for _, v := range vectors(t) {
		t.Run(v.Name, func(t *testing.T) {
			var got *idp.Identity
			var err error
			switch v.Kind {
			case "id-token":
				token := b64([]byte(v.Header)) + "." + b64([]byte(v.Claims)) + "." + v.Signature
				if sum := sha256.Sum256([]byte(token)); hex.EncodeToString(sum[:]) != v.SHA256 {
					t.Fatal("the parts do not make the token that was published")
				}
				// Its key is no longer published: with today's keys it is read
				// all the way to its signature, and refused there.
				for _, set := range vectors(t) {
					if set.Kind != "keys" || set.Provider != v.Provider {
						continue
					}
					id, err := idp.Verify(v.Provider, issuerOf(v.Provider), v.Application, v.Nonce, token, time.Unix(v.Now, 0), []byte(set.Body))
					invalid(t, id, err, "ID token signed with a key the provider does not publish")
				}
				got, err = idp.Claims(v.Provider, issuerOf(v.Provider), v.Application, v.Nonce, []byte(v.Claims), time.Unix(v.Now, 0))
			case "id-token-claims":
				got, err = idp.Claims(v.Provider, issuerOf(v.Provider), v.Application, v.Nonce, []byte(v.Claims), time.Unix(v.Now, 0))
			case "keys":
				usable, kept, err := idp.UsableKeys([]byte(v.Body))
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(usable, v.Expect.Usable) || kept != len(usable) {
					t.Fatalf("%d keys kept, usable %v, want %v", kept, usable, v.Expect.Usable)
				}
				return
			case "github-user":
				got, err = idp.ParseGitHubUser([]byte(v.Body))
			case "github-emails":
				var address string
				address, err = idp.ParseGitHubEmails([]byte(v.Body))
				got = &idp.Identity{Provider: idp.GitHub, Address: address}
			case "meta-user":
				got, err = idp.ParseMetaUser([]byte(v.Body))
			default:
				t.Fatalf("a vector of an unknown kind: %q", v.Kind)
			}
			if v.Refused != "" {
				invalid(t, got, err, v.Refused)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := idp.Identity{Provider: v.Provider, Subject: v.Expect.Subject, Address: v.Expect.Address, Verified: v.Expect.Verified, Name: v.Expect.Name}
			if *got != want {
				t.Fatalf("got %+v, want %+v", *got, want)
			}
		})
	}
}

// The vectors keep covering every kind of answer there is one for.
func TestCapturedAnswersCoverTheKinds(t *testing.T) {
	seen := map[string]bool{}
	for _, v := range vectors(t) {
		seen[string(v.Provider)+" "+v.Kind] = true
	}
	for _, want := range []string{
		"google id-token-claims", "google keys",
		"microsoft id-token", "microsoft keys",
		"github github-user", "github github-emails",
		"meta meta-user",
	} {
		if !seen[want] {
			t.Errorf("no vector of %s", want)
		}
	}
}

// A claim of the published examples is checked like any other: each one
// changed is refused.
func TestCapturedTokensAreChecked(t *testing.T) {
	for _, v := range vectors(t) {
		if v.Kind != "id-token" && v.Kind != "id-token-claims" {
			continue
		}
		t.Run(v.Name, func(t *testing.T) {
			at := time.Unix(v.Now, 0)
			claims := []byte(v.Claims)
			id, err := idp.Claims(v.Provider, issuerOf(v.Provider), "another-application", v.Nonce, claims, at)
			invalid(t, id, err, "ID token for another application")
			id, err = idp.Claims(v.Provider, issuerOf(v.Provider), v.Application, "another-nonce", claims, at)
			invalid(t, id, err, "ID token of another sign-in")
			id, err = idp.Claims(v.Provider, "https://issuer.example", v.Application, v.Nonce, claims, at)
			invalid(t, id, err, "ID token of another issuer")
			// The examples are years old.
			id, err = idp.Claims(v.Provider, issuerOf(v.Provider), v.Application, v.Nonce, claims, start)
			invalid(t, id, err, "ID token expired")
			id, err = idp.Claims(v.Provider, issuerOf(v.Provider), v.Application, v.Nonce, claims, at.Add(-2*time.Minute))
			invalid(t, id, err, "ID token made in the future")
		})
	}
}
