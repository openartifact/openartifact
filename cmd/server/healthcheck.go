// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
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

	return probeHealth(net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Server.Port)), healthcheckTimeout)
}

// probeHealth GETs http://addr/health and succeeds only on HTTP 200.
func probeHealth(addr string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	//nolint:gosec // G704: addr is loopback plus the operator-configured port, never request input
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/health", http.NoBody)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}

	resp, err := http.DefaultClient.Do(req) //nolint:gosec // G704: see above
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: %w: status %d", errUnhealthy, resp.StatusCode)
	}

	return nil
}
