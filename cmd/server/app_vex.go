// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"github.com/openartifact/openartifact/internal/csaf"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/internal/vexsync"
	"github.com/openartifact/openartifact/internal/worker"
)

// startVEXFeeds builds the VEX import service in every role: the API starts
// manual syncs of the vendor CSAF feeds with it and federation exchanges VEX
// decisions through it. The worker that syncs the feeds on their schedule
// runs where the workers run; each feed is synced under its own advisory
// lock, so a manual sync on a web process never overlaps it.
func (a *app) startVEXFeeds() {
	gen := sbom.NewGenerator(a.db, a.log).WithToolVersion(version).WithBaseURL(a.cfg.BaseURL)
	svc := vexsync.NewService(a.db, csaf.NewFetcher(nil), gen, a.log).WithClusterLock(a.db.GetDB())

	a.feat.vexSync = svc
	a.workers.vexFeeds = worker.NewVEXFeedsWorker(svc, a.db, a.log)
	a.start(a.workers.vexFeeds.Start)
}
