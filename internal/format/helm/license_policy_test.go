// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package helm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/licensepolicy/policytest"
)

func createHelmChart(t *testing.T, chartYAML string) []byte {
	t.Helper()

	var buf bytes.Buffer

	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	data := []byte(chartYAML)
	_ = tw.WriteHeader(&tar.Header{Name: "mychart/Chart.yaml", Mode: 0644, Size: int64(len(data))})
	_, _ = tw.Write(data)
	_ = tw.Close()
	_ = gw.Close()

	return buf.Bytes()
}

const sampleChartYAML = `apiVersion: v2
name: mychart
version: 1.0.0
description: A test chart
`

const gplChartYAML = sampleChartYAML + `annotations:
  artifacthub.io/license: GPL-3.0
`

func uploadWithPolicy(h *HTTPHandler, chart []byte) (int, []byte) {
	w := formattest.Do(h.RegisterRepoRoutes,
		formattest.Request(http.MethodPost, "/repos/myrepo/charts", bytes.NewReader(chart)))

	return w.Code, w.Body.Bytes()
}

func TestUploadLicensePolicy(t *testing.T) {
	for _, c := range policytest.Cases("GPL-3.0") {
		t.Run(c.Name, func(t *testing.T) {
			db := newHelmDB()
			bs := formattest.NewBlobStore()
			pub := &formattest.Publisher{}
			h := NewHTTPHandler(formattest.NewLogger(t), c.DB(db), bs, "http://localhost")
			h.SetPublisher(pub)

			code, body := uploadWithPolicy(h, createHelmChart(t, gplChartYAML))
			c.AssertResponse(t, code, body)

			if c.Want == policytest.Admitted {
				if code != http.StatusCreated {
					t.Fatalf("status = %d: %s", code, body)
				}

				if db.charts[chartKey("mychart", "1.0.0")] == nil || len(bs.Blobs) != 1 || len(pub.Events()) != 1 {
					t.Fatalf("chart not stored: rows=%d blobs=%d events=%d", len(db.charts), len(bs.Blobs), len(pub.Events()))
				}

				return
			}

			if len(db.charts) != 0 || len(bs.Blobs) != 0 || len(pub.Events()) != 0 || db.artifacts != 0 {
				t.Fatalf("rejected upload stored something: rows=%d blobs=%d events=%d artifacts=%d",
					len(db.charts), len(bs.Blobs), len(pub.Events()), db.artifacts)
			}
		})
	}
}

func TestUploadWithoutLicenseAdmittedUnderLicensePolicy(t *testing.T) {
	db := newHelmDB()
	policy := policytest.Policy("block", []string{"MIT"}, nil)
	h := NewHTTPHandler(formattest.NewLogger(t), (&policytest.DB{Database: db, Policies: []*models.LicensePolicy{policy}}), formattest.NewBlobStore(), "http://localhost")

	if code, body := uploadWithPolicy(h, createHelmChart(t, sampleChartYAML)); code != http.StatusCreated {
		t.Fatalf("status = %d: %s", code, body)
	}
}
