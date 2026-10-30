// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/config"
)

const healthcheckTimeout = 3 * time.Second

var errUnhealthy = errors.New("unhealthy")

// runHealthcheck implements -healthcheck for shell-less images: the port comes
// from the same config/env the server reads.
func runHealthcheck(configFile string) error {
	cfg, err := config.Load(configFile)
	if err != nil {
		return fmt.Errorf("healthcheck: load configuration: %w", err)
	}

	scheme, addr := healthTarget(cfg.Server)

	return probeHealth(scheme, addr, cfg.Server.TLSCert, healthcheckTimeout)
}

// healthTarget picks the URL scheme and host:port of the self-probe: https when
// TLS is enabled, and server.host when it is a concrete address, else loopback.
func healthTarget(s config.ServerConfig) (string, string) {
	scheme := "http"
	if s.TLSEnabled {
		scheme = "https"
	}

	host := strings.Trim(strings.TrimSpace(s.Host), "[]")
	if ip, err := netip.ParseAddr(host); host == "" || (err == nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}

	return scheme, net.JoinHostPort(host, strconv.Itoa(s.Port))
}

// probeHealth GETs scheme://addr/health and succeeds only on HTTP 200. An
// https probe verifies the certificate: against the server certificate at
// certFile when given (the probe reaches this very process, whose certificate
// rarely names the address dialled), else against the system roots.
func probeHealth(scheme, addr, certFile string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	//nolint:gosec // G704: addr is the operator-configured listen address, never request input
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+addr+"/health", http.NoBody)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}

	client := http.DefaultClient

	if scheme == "https" && certFile != "" {
		tlsCfg, err := pinnedTLSConfig(certFile)
		if err != nil {
			return err
		}

		tr := http.DefaultTransport.(*http.Transport).Clone() //nolint:forcetypeassert // stdlib default is *http.Transport
		tr.TLSClientConfig = tlsCfg
		client = &http.Client{Transport: tr}

		defer tr.CloseIdleConnections()
	}

	resp, err := client.Do(req) //nolint:gosec // G704: see above
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: %w: status %d", errUnhealthy, resp.StatusCode)
	}

	return nil
}

// pinnedTLSConfig trusts exactly the certificates in certFile (the server's own
// certificate, leaf first) and verifies the name the leaf was issued for, so a
// different certificate on the port fails the probe.
func pinnedTLSConfig(certFile string) (*tls.Config, error) {
	pemData, err := os.ReadFile(certFile) //nolint:gosec // G304: operator-configured server.tlsCert path
	if err != nil {
		return nil, fmt.Errorf("healthcheck: read server certificate: %w", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemData) {
		return nil, fmt.Errorf("healthcheck: %w: no certificate in %s", errUnhealthy, certFile)
	}

	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, fmt.Errorf("healthcheck: %w: no PEM block in %s", errUnhealthy, certFile)
	}

	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("healthcheck: parse server certificate: %w", err)
	}

	return &tls.Config{RootCAs: pool, ServerName: certServerName(leaf), MinVersion: tls.VersionTLS12}, nil
}

// certServerName returns a name the certificate is valid for: its first DNS
// SAN, else its first IP SAN, else the subject common name.
func certServerName(c *x509.Certificate) string {
	switch {
	case len(c.DNSNames) > 0:
		return c.DNSNames[0]
	case len(c.IPAddresses) > 0:
		return c.IPAddresses[0].String()
	default:
		return c.Subject.CommonName
	}
}
