// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package helm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"strings"
	"testing"
)

// createChartTarball creates an in-memory gzip+tar containing Chart.yaml at the given path
func createChartTarball(t *testing.T, chartYAMLPath, chartYAMLContent string) io.Reader {
	t.Helper()

	var buf bytes.Buffer

	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	data := []byte(chartYAMLContent)

	hdr := &tar.Header{
		Name: chartYAMLPath,
		Mode: 0644,
		Size: int64(len(data)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("tar write header: %v", err)
	}

	if _, err := tw.Write(data); err != nil {
		t.Fatalf("tar write data: %v", err)
	}

	_ = tw.Close()
	_ = gw.Close()

	return &buf
}

const minimalChart = `apiVersion: v2
name: mychart
version: 1.2.3
`

const fullChart = `apiVersion: v2
name: fullchart
version: 2.0.0
appVersion: "3.0.0"
description: A full chart
type: application
keywords:
  - web
  - api
home: https://example.com
sources:
  - https://github.com/example/fullchart
maintainers:
  - name: Alice
    email: alice@example.com
icon: https://example.com/icon.png
`

func TestParseChart_Minimal(t *testing.T) {
	r := createChartTarball(t, "mychart/Chart.yaml", minimalChart)

	result, err := ParseChart(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result["name"] != "mychart" {
		t.Errorf("name = %v, want mychart", result["name"])
	}

	if result["version"] != "1.2.3" {
		t.Errorf("version = %v, want 1.2.3", result["version"])
	}

	if result["apiVersion"] != "v2" {
		t.Errorf("apiVersion = %v, want v2", result["apiVersion"])
	}
}

func TestParseChart_Full(t *testing.T) {
	r := createChartTarball(t, "fullchart/Chart.yaml", fullChart)

	result, err := ParseChart(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result["name"] != "fullchart" {
		t.Errorf("name = %v, want fullchart", result["name"])
	}

	if result["description"] != "A full chart" {
		t.Errorf("description = %v, want 'A full chart'", result["description"])
	}

	if result["home"] != "https://example.com" {
		t.Errorf("home = %v, want https://example.com", result["home"])
	}

	keywords, ok := result["keywords"].([]string)
	if !ok || len(keywords) != 2 {
		t.Errorf("keywords = %v, want [web api]", result["keywords"])
	}
}

func TestParseChart_NestedPathIgnored(t *testing.T) {
	// Only <chart>/Chart.yaml counts; deeper (subchart) manifests are ignored.
	r := createChartTarball(t, "deep/nested/dir/Chart.yaml", minimalChart)

	if _, err := ParseChart(r); !errors.Is(err, ErrChartYAMLNotFound) {
		t.Fatalf("err = %v, want ErrChartYAMLNotFound", err)
	}
}

func TestParseChart_NoChartYAML(t *testing.T) {
	// Valid tar but no Chart.yaml
	var buf bytes.Buffer

	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	data := []byte("some content")
	_ = tw.WriteHeader(&tar.Header{Name: "somefile.txt", Mode: 0644, Size: int64(len(data))})
	_, _ = tw.Write(data)
	_ = tw.Close()
	_ = gw.Close()

	_, err := ParseChart(&buf)
	if err == nil {
		t.Fatal("expected error for missing Chart.yaml, got nil")
	}

	if !strings.Contains(err.Error(), "chart.yaml not found") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestParseChart_InvalidGzip(t *testing.T) {
	_, err := ParseChart(strings.NewReader("not gzip data at all"))
	if err == nil {
		t.Fatal("expected error for invalid gzip, got nil")
	}
}
