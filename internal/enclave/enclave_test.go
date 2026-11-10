package enclave

import "testing"

// The parent instance of a Nitro Enclave listens on CID 3. Dialing the generic
// vsock host CID (2) fails with "connection reset by peer", which the first
// deployment hit for both the egress proxy and the credentials endpoint.
func TestHostCIDIsTheNitroParent(t *testing.T) {
	if HostCID != 3 {
		t.Fatalf("HostCID = %d, want 3 (Nitro Enclaves parent instance)", HostCID)
	}
}
