package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Credentials live only in the owner's files under ~/.config/permaily/ (mode
// 0600, never in the repository) or in environment variables set for one
// run. Nothing here prints them.

func configDir() (string, error) {
	if d := os.Getenv("PERMAILY_SPIKE_CONFIG"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "permaily"), nil
}

// errNoCredentials says what to create; it never carries a value.
var errNoCredentials = errors.New("credentials not found")

// readSecretJSON reads a credential file, refusing one any other user could
// read or write.
func readSecretJSON(path string, v any) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s", errNoCredentials, path)
	}
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s: not a regular file", path)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s: mode %o, must be 0600 (chmod 600 %s)", path, fi.Mode().Perm(), path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		// Never echo the file's content.
		return fmt.Errorf("%s: not valid JSON", path)
	}
	return nil
}

// writeSecretJSON writes a credential file with mode 0600.
func writeSecretJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// googleClient is the Web client of the owner's `permaily-mail` project.
type googleClient struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

func loadGoogleClient() (googleClient, error) {
	if id, sec := os.Getenv("PERMAILY_SPIKE_GOOGLE_CLIENT_ID"), os.Getenv("PERMAILY_SPIKE_GOOGLE_CLIENT_SECRET"); id != "" && sec != "" {
		return googleClient{ClientID: id, ClientSecret: sec}, nil
	}
	dir, err := configDir()
	if err != nil {
		return googleClient{}, err
	}
	var c googleClient
	if err := readSecretJSON(filepath.Join(dir, "spike-google-client.json"), &c); err != nil {
		return c, err
	}
	if c.ClientID == "" || c.ClientSecret == "" {
		return c, errors.New("spike-google-client.json needs client_id and client_secret")
	}
	return c, nil
}

// googleGrant is what the trip leaves for the later checks.
type googleGrant struct {
	RefreshToken string `json:"refresh_token"`
	AccessToken  string `json:"access_token"`
	AccessExpiry int64  `json:"access_expiry"`
	GrantedAt    int64  `json:"granted_at"`
}

func grantPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "spike-google-grant.json"), nil
}

func loadGoogleGrant() (googleGrant, error) {
	p, err := grantPath()
	if err != nil {
		return googleGrant{}, err
	}
	var g googleGrant
	err = readSecretJSON(p, &g)
	return g, err
}

func saveGoogleGrant(g googleGrant) error {
	p, err := grantPath()
	if err != nil {
		return err
	}
	return writeSecretJSON(p, g)
}

// imapCredentials is one test account's login at one listed host.
type imapCredentials struct {
	Username    string `json:"username"`
	AppPassword string `json:"app_password"`
}

func loadIMAPCredentials(host string) (imapCredentials, error) {
	if u, p := os.Getenv("PERMAILY_SPIKE_IMAP_USERNAME"), os.Getenv("PERMAILY_SPIKE_IMAP_APP_PASSWORD"); u != "" && p != "" {
		return imapCredentials{Username: u, AppPassword: p}, nil
	}
	dir, err := configDir()
	if err != nil {
		return imapCredentials{}, err
	}
	var c imapCredentials
	if err := readSecretJSON(filepath.Join(dir, "spike-imap-"+host+".json"), &c); err != nil {
		return c, err
	}
	if c.Username == "" || c.AppPassword == "" {
		return c, fmt.Errorf("spike-imap-%s.json needs username and app_password", host)
	}
	return c, nil
}

// pseudonym maps an identifier (a message id, a folder name, a subject
// claim) to a stable HMAC under a local key, so results can say "the same
// message" without naming it.
type pseudonym struct{ key []byte }

func loadPseudonym() (pseudonym, error) {
	dir, err := configDir()
	if err != nil {
		return pseudonym{}, err
	}
	path := filepath.Join(dir, "spike-hmac.key")
	var k struct {
		Key string `json:"key"`
	}
	err = readSecretJSON(path, &k)
	if errors.Is(err, errNoCredentials) {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return pseudonym{}, err
		}
		k.Key = hex.EncodeToString(b)
		if err := writeSecretJSON(path, k); err != nil {
			return pseudonym{}, err
		}
	} else if err != nil {
		return pseudonym{}, err
	}
	key, err := hex.DecodeString(k.Key)
	if err != nil || len(key) != 32 {
		return pseudonym{}, fmt.Errorf("%s: bad key", path)
	}
	return pseudonym{key: key}, nil
}

func (p pseudonym) of(kind, v string) string {
	m := hmac.New(sha256.New, p.key)
	m.Write([]byte(kind + "\x00" + v))
	return kind + ":" + hex.EncodeToString(m.Sum(nil))[:16]
}
