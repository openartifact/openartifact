// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package otelcoltest runs a real OpenTelemetry Collector (contrib) in a
// container for tests: OTLP in over gRPC and HTTP, every signal written by the
// file exporter as OTLP JSON, one export request per line, readable by the
// test. It is imported by tests only.
package otelcoltest

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Image is the collector image, the one docker/docker-compose.yml runs.
// Dependabot bumps the compose file only: keep the two in step.
const Image = "docker.io/otel/opentelemetry-collector-contrib:0.163.2" +
	"@sha256:7f22d2a6748110ffb6a59bc0df448bccbf501cdb667d610ede4c61d3863d5849"

const (
	grpcPort   = "4317/tcp"
	httpPort   = "4318/tcp"
	exportPath = "/otel/export.jsonl"
	readyLine  = "Everything is ready. Begin running and processing data."
	pollEvery  = 200 * time.Millisecond
)

// config receives OTLP on both protocols and writes every signal to
// exportPath, flushed often so a test does not wait for the default 1 s.
const config = `receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
      http:
        endpoint: 0.0.0.0:4318
exporters:
  file:
    path: ` + exportPath + `
    flush_interval: 100ms
service:
  pipelines:
    traces:
      receivers: [otlp]
      exporters: [file]
    metrics:
      receivers: [otlp]
      exporters: [file]
    logs:
      receivers: [otlp]
      exporters: [file]
`

// Collector is a running collector container.
type Collector struct {
	ctr *testcontainers.DockerContainer

	// GRPCEndpoint is host:port of the OTLP/gRPC receiver (plaintext).
	GRPCEndpoint string
	// HTTPEndpoint is http://host:port of the OTLP/HTTP receiver.
	HTTPEndpoint string
}

// Start runs a collector for the test and terminates it at cleanup. Without
// a container runtime the test is skipped, or fails when CI is set.
func Start(t *testing.T) *Collector {
	t.Helper()

	ctx := t.Context()
	requireRuntime(t)

	ctr, err := testcontainers.Run(ctx, Image,
		testcontainers.WithFiles(
			testcontainers.ContainerFile{
				Reader:            strings.NewReader(config),
				ContainerFilePath: "/etc/otelcol-contrib/config.yaml",
				FileMode:          0o644,
			},
			// The image runs as an unprivileged user and has no writable
			// directory: provide the export file, writable by anyone.
			testcontainers.ContainerFile{
				Reader:            strings.NewReader(""),
				ContainerFilePath: exportPath,
				FileMode:          0o666,
			},
		),
		testcontainers.WithExposedPorts(grpcPort, httpPort),
		// The collector logs readyLine once every receiver listens. A port
		// wait would also probe inside the container, which has no shell.
		testcontainers.WithWaitStrategy(wait.ForLog(readyLine)),
	)
	testcontainers.CleanupContainer(t, ctr)

	if err != nil {
		t.Fatalf("start collector %s: %v", Image, err)
	}

	grpcEP, err := ctr.PortEndpoint(ctx, grpcPort, "")
	if err != nil {
		t.Fatalf("collector gRPC endpoint: %v", err)
	}

	httpEP, err := ctr.PortEndpoint(ctx, httpPort, "http")
	if err != nil {
		t.Fatalf("collector HTTP endpoint: %v", err)
	}

	return &Collector{ctr: ctr, GRPCEndpoint: grpcEP, HTTPEndpoint: httpEP}
}

// Stop stops the collector, as an outage would: its ports refuse connections
// from then on. Lines still reads what it exported before.
func (c *Collector) Stop(t *testing.T) {
	t.Helper()

	if err := c.ctr.Stop(t.Context(), nil); err != nil {
		t.Fatalf("stop collector: %v", err)
	}
}

// requireRuntime skips the test without a reachable container runtime, or
// fails it under CI, where the runtime is part of the job.
func requireRuntime(t *testing.T) {
	t.Helper()

	p, err := testcontainers.NewDockerProvider()
	if err == nil {
		err = p.Health(t.Context())
		_ = p.Close()
	}

	if err == nil {
		return
	}

	if os.Getenv("CI") != "" {
		t.Fatalf("container runtime unavailable in CI: %v", err)
	}

	t.Skipf("container runtime unavailable: %v (podman: see AGENTS.md)", err)
}

// Lines returns the export requests written so far, one OTLP JSON document
// (an Export{Traces,Metrics,Logs}ServiceRequest) per line.
func (c *Collector) Lines(ctx context.Context) ([]string, error) {
	rc, err := c.ctr.CopyFileFromContainer(ctx, exportPath)
	if err != nil {
		return nil, err //nolint:wrapcheck // test helper, the caller names the step
	}
	defer rc.Close()

	b, err := io.ReadAll(rc)
	if err != nil {
		return nil, err //nolint:wrapcheck // test helper, the caller names the step
	}

	var lines []string

	for line := range strings.SplitSeq(string(b), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}

	return lines, nil
}

// WaitForLine polls the exported lines until one satisfies match and returns
// it; the test fails after timeout.
func (c *Collector) WaitForLine(t *testing.T, timeout time.Duration, match func(line string) bool) string {
	t.Helper()

	deadline := time.Now().Add(timeout)

	for {
		lines, err := c.Lines(t.Context())
		if err != nil {
			t.Fatalf("read collector export: %v", err)
		}

		for _, l := range lines {
			if match(l) {
				return l
			}
		}

		if time.Now().After(deadline) {
			t.Fatalf("no matching line exported within %s; got %d line(s):\n%s", timeout, len(lines), strings.Join(lines, "\n"))
		}

		time.Sleep(pollEvery)
	}
}
