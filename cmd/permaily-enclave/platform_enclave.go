//go:build !dev

package main

import (
	"context"
	"log/slog"
	"net"

	"github.com/permaily-org/permaily/internal/enclave"
	"github.com/permaily-org/permaily/internal/nsm"
	"github.com/permaily-org/permaily/internal/router"
	"github.com/permaily-org/permaily/internal/tlsacme"
)

type platform struct {
	mode     string
	listener net.Listener
	attester nsm.Attester
	certs    *tlsacme.Manager
}

func newPlatform(ctx context.Context, cfg config, hosts router.Hosts, log *slog.Logger) (*platform, error) {
	att, err := nsm.Open()
	if err != nil {
		return nil, err
	}
	certs, err := tlsacme.New(hosts.All(), cfg.acmeDirectory, enclave.EgressClient(uint32(cfg.egressPort)), log)
	if err != nil {
		return nil, err
	}
	ln, err := enclave.Listen(uint32(cfg.port))
	if err != nil {
		return nil, err
	}
	go certs.Run(ctx)
	return &platform{mode: "enclave", listener: ln, attester: att, certs: certs}, nil
}
