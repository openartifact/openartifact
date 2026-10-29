// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
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

	return probeHealth(scheme, addr, healthcheckTimeout)
}

// healthTarget picks the URL scheme and host:port of the self-probe: https when
// TLS is enabled, and server.host when it is a concrete address, else loopback.
func healthTarget(s config.ServerConfig) (scheme, addr string) {
	scheme = "http"
	if s.TLSEnabled {
		scheme = "https"
	}

	host := strings.Trim(strings.TrimSpace(s.Host), "[]")
	if ip, err := netip.ParseAddr(host); host == "" || (err == nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}

	return scheme, net.JoinHostPort(host, strconv.Itoa(s.Port))
}

// isLoopbackHost reports whether host is localhost or a loopback address.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}

	ip, err := netip.ParseAddr(host)

	return err == nil && ip.IsLoopback()
}

// probeHealth GETs scheme://addr/health and succeeds only on HTTP 200. The
// certificate of an https probe is verified unless addr is a loopback address:
// the probe then reaches this very process, whose certificate rarely names an
// IP, so skipping verification there exposes nothing.
func probeHealth(scheme, addr string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	//nolint:gosec // G704: addr is the operator-configured listen address, never request input
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+addr+"/health", http.NoBody)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}

	client := http.DefaultClient

	if host, _, splitErr := net.SplitHostPort(addr); scheme == "https" && splitErr == nil && isLoopbackHost(host) {
		tr := http.DefaultTransport.(*http.Transport).Clone()                                    //nolint:forcetypeassert // stdlib default is *http.Transport
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} //nolint:gosec // loopback self-probe, see above
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
