// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/vuln"
	"github.com/openartifact/openartifact/internal/workq"
)

// VulnScanRequestKind is the kind of the worker request that asks for a scan.
const VulnScanRequestKind = "vuln:scan"

// scanForwardTimeout bounds writing one scan request to the database.
const scanForwardTimeout = 5 * time.Second

// vulnScanRequest is the payload of a scan request: the stored result, which is
// in the "scanning" state, and the package it is for.
type vulnScanRequest struct {
	RepoID   string       `json:"repo_id"`
	ResultID string       `json:"result_id"`
	Package  vuln.Package `json:"package"`
}

// ForwardScansTo makes the handler hand its scans to a worker process
// (server.role=web): the scan is stored as "scanning" and a request is written
// to s, for a process running ServeScansFrom to run. A web process has no
// scanner installed and reserves no scan slot.
func (h *VulnHandler) ForwardScansTo(s *workq.Store) *VulnHandler {
	h.fwd = s
	return h
}

// ServeScansFrom makes the handler run the scans the requests claimed by c ask
// for, with the same concurrency bound as the scans of its own API.
func (h *VulnHandler) ServeScansFrom(c *workq.Consumer) {
	c.Handle(VulnScanRequestKind, func(ctx context.Context, r workq.Request) workq.Result {
		return h.handleScanRequest(ctx, r, func(done bool) { c.Settle(r.Kind, r.Key, done) })
	})
}

// forwardScan writes the request for the scan stored as result.
func (h *VulnHandler) forwardScan(ctx context.Context, result *models.VulnScanResult, pkg vuln.Package) error {
	payload, err := json.Marshal(vulnScanRequest{RepoID: result.RepositoryID, ResultID: result.ID, Package: pkg})
	if err != nil {
		return fmt.Errorf("encode scan request: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), scanForwardTimeout)
	defer cancel()

	return h.fwd.Put(ctx, VulnScanRequestKind, result.ID, payload) //nolint:wrapcheck // workq.Put names the request
}

// handleScanRequest starts the scan a request asks for. settle reports that the
// scan it started is over.
func (h *VulnHandler) handleScanRequest(ctx context.Context, r workq.Request, settle func(done bool)) workq.Result {
	var req vulnScanRequest
	if err := json.Unmarshal(r.Payload, &req); err != nil {
		h.log.Errorw("vuln scan: dropping a malformed scan request", "key", r.Key, "error", err)
		return workq.Done
	}

	select {
	case h.scanSem <- struct{}{}:
	default:
		return workq.Retry
	}

	result, err := h.db.GetVulnScanResultByID(ctx, req.RepoID, req.ResultID)

	switch {
	case err != nil:
		<-h.scanSem

		h.log.Errorw("vuln scan: reading the stored scan of a request", "result_id", req.ResultID, "error", err)

		return workq.Retry
	case result == nil || result.ScanStatus != "scanning":
		// Deleted meanwhile, or already scanned by an earlier claim.
		<-h.scanSem
		return workq.Done
	}

	h.tasks.Go(func(ctx context.Context) {
		defer func() { <-h.scanSem }()

		h.performScan(ctx, result, req.Package)
		settle(true)
	})

	return workq.Later
}
