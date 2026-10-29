// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"

	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/attest"
	"github.com/openartifact/openartifact/internal/dtrack"
	formatdocker "github.com/openartifact/openartifact/internal/format/docker"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/internal/vuln"
)

// The services below are built by the API routes (a web or all process) and by
// a worker process alike, because the background work of a scan, of a push to
// Dependency-Track and of an attestation is theirs. Where this process does not
// run the workers they only queue a request for one that does; where it does
// they start and serve the requests (see role.go).

// scanServices are the supply-chain services shared by the API routes and the
// background work.
type scanServices struct {
	attest   *attest.Service
	scanners *vuln.Registry
}

// buildWorkerServices builds, on a worker process, the services whose
// background work it runs, without the routes an API process mounts for them.
func (a *app) buildWorkerServices() {
	docker := formatdocker.NewHTTPHandler(a.log, a.db, a.store.served)
	docker.SetUploadDir(dockerUploadDir(a.cfg))

	a.buildSupplyChain(docker)
	a.buildDependencyTrack()
	a.buildVulnScanning()
}

// buildSupplyChain builds the SBOM generator, the attestation service and the
// attacher that stores attestations beside the docker images they describe.
func (a *app) buildSupplyChain(docker api.ReferrerRegistry) {
	cfg, db, log := a.cfg, a.db, a.log

	a.feat.sbomGen = sbom.NewGenerator(db, log).WithBlobStore(a.store.blobs).WithToolVersion(version).WithBaseURL(cfg.BaseURL)

	// SBOM / VEX attestations: signed with a configured or generated key; the
	// public half is served unauthenticated so anyone can verify.
	a.workers.scan.attest = attest.NewService(cfg.Attest.KeyPath, db)
	a.feat.attacher = api.NewAttestationAttacher(db, log, a.feat.sbomGen, a.workers.scan.attest, docker)
}

// buildDependencyTrack builds the Dependency-Track pusher, which pushes the BOM
// on upload and scan, then the VEX decisions. Where the workers run it starts;
// else it hands its pushes to a worker process.
func (a *app) buildDependencyTrack() {
	dtPusher := dtrack.NewPusher(a.db, api.NewDTrackSources(a.db, a.feat.sbomGen), a.log)

	if requests := a.workers.requests; requests != nil {
		dtPusher.ServeFrom(requests)
		dtPusher.Start(context.Background())

		a.extraStops = append(a.extraStops, dtPusher.Stop)
	} else {
		dtPusher.ForwardTo(a.workers.queue)
	}

	a.feat.dtPusher = dtPusher
}

// buildVulnScanning builds the scanner registry and the handler that runs the
// scans. Trivy and grype are external binaries; where the workers run, the
// registry downloads and refreshes their databases in the background
// (docs/security/scanning.md). A scan and its scan hook run under the server
// lifecycle; a scan requested on a process that does not run the workers is
// handed to one that does.
func (a *app) buildVulnScanning() *api.VulnHandler {
	scanners := vuln.NewRegistry(a.log, a.cfg.Scanners.VulnOptions()).WithMetrics(a.reg)
	if a.cfg.OSVMirror.Enabled {
		scanners = scanners.WithOSVMirror(vuln.NewMirror(a.db))
	}

	a.workers.scan.scanners = scanners

	handler := api.NewVulnHandler(a.db, a.log, a.web.evaluator, scanners).
		WithTasks(a.tasks).
		WithScanHook(a.onScanned).
		WithExploit(a.workers.exploit).
		WithOSVMirror(a.cfg.OSVMirror)

	if requests := a.workers.requests; requests != nil {
		scanners.LogStatus()
		scanners.Start(context.Background())

		a.extraStops = append(a.extraStops, scanners.Stop)

		handler.ServeScansFrom(requests)
	} else {
		handler.ForwardScansTo(a.workers.queue)
	}

	return handler
}
