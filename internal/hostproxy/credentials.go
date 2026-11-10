package hostproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Credentials serves the instance role's credentials (from IMDSv2) to the
// enclave, which has no network of its own. The enclave cannot use them to
// read mail: KMS releases data keys only to an attested enclave.
type Credentials struct {
	// IMDS is the metadata endpoint, http://169.254.169.254 on EC2.
	IMDS   string
	Client *http.Client
}

func (c *Credentials) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Timeout: 5 * time.Second}
}

func (c *Credentials) get(ctx context.Context, method, path, token string, header map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.IMDS+path, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("X-aws-ec2-metadata-token", token)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("imds %s: HTTP %d", path, res.StatusCode)
	}
	return body, nil
}

// ServeHTTP implements http.Handler for GET /credentials.
func (c *Credentials) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.Path != "/credentials" {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	token, err := c.get(ctx, http.MethodPut, "/latest/api/token", "", map[string]string{"X-aws-ec2-metadata-token-ttl-seconds": "300"})
	if err != nil {
		http.Error(w, "metadata unavailable", http.StatusBadGateway)
		return
	}
	role, err := c.get(ctx, http.MethodGet, "/latest/meta-data/iam/security-credentials/", string(token), nil)
	if err != nil {
		http.Error(w, "no instance role", http.StatusBadGateway)
		return
	}
	name := strings.TrimSpace(strings.SplitN(string(role), "\n", 2)[0])
	creds, err := c.get(ctx, http.MethodGet, "/latest/meta-data/iam/security-credentials/"+name, string(token), nil)
	if err != nil {
		http.Error(w, "credentials unavailable", http.StatusBadGateway)
		return
	}
	var check struct {
		AccessKeyID string `json:"AccessKeyId"`
	}
	if json.Unmarshal(creds, &check) != nil || check.AccessKeyID == "" {
		http.Error(w, "invalid credentials document", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(creds)
}
