// Command permaily-host runs on the parent EC2 instance. It is untrusted by
// design: it forwards public TCP ports to the enclave over vsock and exposes
// an allowlisted CONNECT proxy on vsock for the enclave's outbound traffic.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mdlayher/vsock"

	"github.com/permaily-org/permaily/internal/hostproxy"
)

// defaultEgress is everything the enclave may reach (docs/threat-model.md).
var defaultEgress = []string{
	"acme-v02.api.letsencrypt.org:443",
	"acme-staging-v02.api.letsencrypt.org:443",
	"kms.us-east-2.amazonaws.com:443",
	"dynamodb.us-east-2.amazonaws.com:443",
	"s3.us-east-2.amazonaws.com:443",
	"cloudflare-dns.com:443",
}

func uint32Flag(dst *uint32) func(string) error {
	return func(s string) error {
		v, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			return err
		}
		*dst = uint32(v)
		return nil
	}
}

func main() {
	var (
		cid        = uint32(16)
		forwards   = flag.String("forward", ":443=443,:25=25", "comma-separated tcp-listen-addr=enclave-vsock-port pairs")
		egressPort = uint32(8000)
		allow      = flag.String("egress-allow", strings.Join(defaultEgress, ","), "comma-separated host:port targets the enclave may reach")
		credsPort  = uint32(8002)
		imds       = flag.String("imds", "http://169.254.169.254", "instance metadata endpoint")
	)
	flag.Func("credentials-port", "vsock port serving the instance role credentials to the enclave (default 8002)", uint32Flag(&credsPort))
	flag.Func("enclave-cid", "vsock context ID of the enclave (default 16)", uint32Flag(&cid))
	flag.Func("egress-port", "vsock port for the enclave's CONNECT proxy (default 8000)", uint32Flag(&egressPort))
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 8)
	for _, pair := range strings.Split(*forwards, ",") {
		addr, portStr, ok := strings.Cut(pair, "=")
		var port uint32
		if _, err := fmt.Sscan(portStr, &port); !ok || err != nil {
			log.Error("invalid -forward entry", "entry", pair)
			os.Exit(2)
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			log.Error("listen", "addr", addr, "err", err)
			os.Exit(1)
		}
		log.Info("forwarding", "tcp", addr, "vsock_cid", cid, "vsock_port", port)
		go func() {
			errc <- hostproxy.Forward(ctx, ln, func(ctx context.Context) (net.Conn, error) {
				return vsock.DialContext(ctx, cid, port, nil)
			}, log)
		}()
	}

	vl, err := vsock.Listen(egressPort, nil)
	if err != nil {
		log.Error("egress listen", "err", err)
		os.Exit(1)
	}
	allowList := strings.Split(*allow, ",")
	log.Info("egress proxy", "vsock_port", egressPort, "allow", allowList)
	srv := &http.Server{
		Handler:           &hostproxy.ConnectProxy{Allow: allowList, Log: log},
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { errc <- srv.Serve(vl) }()

	cl, err := vsock.Listen(credsPort, nil)
	if err != nil {
		log.Error("credentials listen", "err", err)
		os.Exit(1)
	}
	log.Info("credentials endpoint", "vsock_port", credsPort)
	credSrv := &http.Server{Handler: &hostproxy.Credentials{IMDS: *imds}, ReadHeaderTimeout: 10 * time.Second}
	go func() { errc <- credSrv.Serve(cl) }()

	select {
	case <-ctx.Done():
		_ = srv.Close()
		_ = credSrv.Close()
	case err := <-errc:
		log.Error("stopped", "err", err)
		os.Exit(1)
	}
}
