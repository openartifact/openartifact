// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/attest"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/backup"
	"github.com/openartifact/openartifact/internal/dtrack"
	formatcargo "github.com/openartifact/openartifact/internal/format/cargo"
	formatconan "github.com/openartifact/openartifact/internal/format/conan"
	formatconda "github.com/openartifact/openartifact/internal/format/conda"
	formatdeb "github.com/openartifact/openartifact/internal/format/deb"
	formatdocker "github.com/openartifact/openartifact/internal/format/docker"
	formatgomod "github.com/openartifact/openartifact/internal/format/gomod"
	formathelm "github.com/openartifact/openartifact/internal/format/helm"
	formativy "github.com/openartifact/openartifact/internal/format/ivy"
	formatmaven "github.com/openartifact/openartifact/internal/format/maven"
	formatnpm "github.com/openartifact/openartifact/internal/format/npm"
	formatnuget "github.com/openartifact/openartifact/internal/format/nuget"
	formatpypi "github.com/openartifact/openartifact/internal/format/pypi"
	formatraw "github.com/openartifact/openartifact/internal/format/raw"
	formatrpm "github.com/openartifact/openartifact/internal/format/rpm"
	formatrubygems "github.com/openartifact/openartifact/internal/format/rubygems"
	formatsbt "github.com/openartifact/openartifact/internal/format/sbt"
	"github.com/openartifact/openartifact/internal/importmeta"
	"github.com/openartifact/openartifact/internal/license"
	aimcp "github.com/openartifact/openartifact/internal/mcp"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/repodispatch"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/internal/search"
	"github.com/openartifact/openartifact/internal/secrets"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/vuln"
	"github.com/openartifact/openartifact/internal/webhook"
)

// formatHandlers are the native protocol handlers of every repository format.
type formatHandlers struct {
	rpm      *formatrpm.HTTPHandler
	deb      *formatdeb.HTTPHandler
	helm     *formathelm.HTTPHandler
	pypi     *formatpypi.HTTPHandler
	nuget    *formatnuget.HTTPHandler
	npm      *formatnpm.HTTPHandler
	cargo    *formatcargo.HTTPHandler
	rubygems *formatrubygems.HTTPHandler
	raw      *formatraw.HTTPHandler
	conda    *formatconda.HTTPHandler
	conan    *formatconan.HTTPHandler
	ivy      *formativy.HTTPHandler
	sbt      *formatsbt.HTTPHandler
	maven    *formatmaven.HTTPHandler
	docker   *formatdocker.HTTPHandler
	gomod    *formatgomod.HTTPHandler
}

// uploadHandlers are the API upload handlers that announce uploads through the
// publisher.
type uploadHandlers struct {
	rpm       *api.RPMUploadHandler
	deb       *api.DebUploadHandler
	presigned *api.PresignedUploadHandler
	npm       *api.NPMUploadHandler
	maven     *api.MavenUploadHandler
}

// publicHandlers are the handlers that register on both the bare router
// (public part) and the authenticated one.
type publicHandlers struct {
	auth    *api.AuthHandler
	sysInfo *api.SystemInfoHandler
	brand   *api.BrandingHandler
}

// registerPublicRoutes mounts the routes that need no authentication: login,
// SSO callbacks, system info, branding, the OpenAPI document and the MCP
// resource metadata.
func (a *app) registerPublicRoutes() error {
	cfg, db, log := a.cfg, a.db, a.log
	w := &a.web
	router, evaluator := w.router, w.evaluator

	w.public.auth = api.NewAuthHandler(db, log, evaluator, w.trustedProxies).WithBaseURL(cfg.BaseURL)
	w.public.auth.RegisterPublic(router)

	hmacKey, err := auth.ResolveHMACKey(context.Background(), db, cfg.Auth.HMACKey, log)
	if err != nil {
		return a.fail("failed to resolve the SSO HMAC key", err)
	}

	api.NewOIDCHandler(db, log, evaluator, hmacKey, cfg.BaseURL).WithTrustedProxies(w.trustedProxies).RegisterPublic(router)
	api.NewSAMLHandler(db, log, evaluator, hmacKey, cfg.BaseURL, cfg).WithTrustedProxies(w.trustedProxies).RegisterPublic(router)

	w.public.sysInfo = api.NewSystemInfoHandler(version, commit, buildTime, db, a.store.blobs, cfg.Storage.Type).WithDocsBaseURL(cfg.Docs.BaseURL).WithAPIReference(cfg.API.Docs.Enabled)
	w.public.sysInfo.RegisterPublic(router)

	// Public branding (no auth required: the login page and the app shell need
	// it before anyone is signed in).
	w.public.brand = api.NewBrandingHandler(w.brandManager, log, evaluator)
	w.public.brand.RegisterPublic(router)

	// Public SSO provider list (no auth required — used by login page)
	api.NewAdminSSOHandler(db, log, evaluator, cfg.BaseURL, cfg).RegisterPublic(router)

	// OpenAPI documentation
	api.NewOpenAPIHandler(version, cfg.API.Docs.Enabled).RegisterRoutes(router)

	// RFC 9728 protected-resource metadata of the MCP endpoint: public, outside
	// the authenticated router so clients can discover how to authenticate.
	if a.feat.mcpServer != nil {
		metadata := aimcp.NewMetadataHandler(db, log, cfg.BaseURL, cfg.MCP.HTTPPath)
		router.Handle("/.well-known/oauth-protected-resource", metadata)
		router.Handle("/.well-known/oauth-protected-resource"+cfg.MCP.HTTPPath, metadata)
	}

	return nil
}

// newAuthedRouter creates the router every authenticated API and repository
// route is registered on, with its middleware chain.
func (a *app) newAuthedRouter() {
	w := &a.web
	authedAPI := chi.NewRouter()
	authedAPI.Use(w.rbacMiddleware)
	// Marks client downloads so the tiered store records blob access.
	authedAPI.Use(middleware.TrackBlobAccess)
	// Admin-only origin timestamps for migrations (X-OA-Import-Created-At).
	authedAPI.Use(importmeta.Middleware(w.evaluator))
	authedAPI.Use(middleware.CSRFMiddleware(a.cfg.BaseURL))
	authedAPI.Use(middleware.AuditMiddleware(a.auditLogger, w.trustedProxies))

	w.authed = authedAPI
}

// registerAdminRoutes registers the account, administration and project
// routes, search and the package upload APIs.
func (a *app) registerAdminRoutes() {
	cfg, db, log := a.cfg, a.db, a.log
	w := &a.web
	authedAPI, evaluator := w.authed, w.evaluator

	w.public.auth.RegisterAuthed(authedAPI)

	api.NewAdminSSOHandler(db, log, evaluator, cfg.BaseURL, cfg).RegisterAuthed(authedAPI)

	api.NewAdminUsersHandler(db, log, evaluator).Register(authedAPI)
	api.NewAdminGroupsHandler(db, log, evaluator).Register(authedAPI)
	api.NewAdminPermissionsHandler(db, log, evaluator).Register(authedAPI)
	api.NewAdminDeclarativeHandler(db, log, evaluator).Register(authedAPI)
	api.NewAdminTokensHandler(db, log, evaluator).Register(authedAPI)
	api.NewSelfTokensHandler(db, log, evaluator).Register(authedAPI)
	api.NewAdminAuditHandler(db, log, evaluator).Register(authedAPI)
	api.NewAdminStorageHandler(db, log, evaluator).Register(authedAPI)
	api.NewAdminTieringHandler(db, a.store.tiers, a.workers.tierEngine, a.workers.tierWorker, log, evaluator).Register(authedAPI)
	api.NewAdminReplicationHandler(a.store.replAdmin, log, evaluator).Register(authedAPI)
	api.NewAdminRecoveryHandler(a.workers.journal, log, evaluator).Register(authedAPI)
	api.NewAdminGPGHandler(db, log, evaluator, a.workers.gpgKeys).Register(authedAPI)
	w.public.sysInfo.RegisterAuthed(authedAPI)
	w.public.brand.RegisterAuthed(authedAPI)

	if cfg.Server.PprofEnabled {
		registerPprof(authedAPI)
		log.Warnw("pprof enabled at /api/v1/admin/pprof/ (admin only); disable outside diagnostics")
	}

	api.NewAdminAIHandler(db, log, evaluator).WithRuntime(a.feat.aiWorker, a.feat.envAI).Register(authedAPI)

	projectsAPI := api.NewProjectsHandler(db, log, evaluator, w.projectSettings)
	projectsAPI.SetCleanupPreviewer(a.workers.cleanup)
	projectsAPI.RegisterRoutes(authedAPI)

	searchHandler := search.NewHandler(db.GetDB(), evaluator, log, search.WithAuthCache(db))
	authedAPI.Get("/api/v1/search", searchHandler.ServeHTTP)

	a.registerUploadRoutes()
}

// registerUploadRoutes registers the rpm, deb and presigned upload APIs.
func (a *app) registerUploadRoutes() {
	db, log, blobStore, reg := a.db, a.log, a.store.blobs, a.reg
	u, wk := &a.web.uploads, &a.workers

	u.rpm = api.NewRPMUploadHandler(log, db, blobStore, reg)
	u.rpm.SetDebWorker(wk.deb)
	u.rpm.SetRPMWorker(wk.rpm)
	u.rpm.SetPackageDeleter(wk.cleanup)
	u.rpm.SetKeyResolver(wk.gpgKeys)
	u.rpm.Register(a.web.authed)

	u.deb = api.NewDebUploadHandler(log, db, blobStore, reg)
	u.deb.SetDebWorker(wk.deb)
	u.deb.Register(a.web.authed)

	u.presigned = api.NewPresignedUploadHandler(log, db, blobStore, a.web.evaluator, a.cfg.Storage.PresignUploadTTL, a.cfg.Storage.PresignMaxPendingUploads, reg)
	u.presigned.Register(a.web.authed)
}

// newFormatHandlers builds the native protocol handler of every format.
func (a *app) newFormatHandlers() {
	log, db, served, baseURL := a.log, a.db, a.store.served, a.cfg.BaseURL
	f := &a.web.formats

	f.rpm = formatrpm.NewHTTPHandler(log, db, served)
	f.deb = formatdeb.NewHTTPHandler(log, db, served)
	f.helm = formathelm.NewHTTPHandler(log, db, served, baseURL)
	f.pypi = formatpypi.NewHTTPHandler(log, db, served, baseURL)
	f.nuget = formatnuget.NewHTTPHandler(log, db, served, baseURL)
	f.npm = formatnpm.NewHTTPHandler(log, db, served, baseURL)
	f.cargo = formatcargo.NewHTTPHandler(log, db, served, baseURL)
	f.rubygems = formatrubygems.NewHTTPHandler(log, db, served, baseURL)
	f.raw = formatraw.NewHTTPHandler(log, db, served, baseURL)
	f.conda = formatconda.NewHTTPHandler(log, db, served, baseURL)
	f.conan = formatconan.NewHTTPHandler(log, db, served, baseURL)
	f.ivy = formativy.NewHTTPHandler(log, db, served, baseURL)
	f.sbt = formatsbt.NewHTTPHandler(log, db, served, baseURL)
	f.maven = formatmaven.NewHTTPHandler(log, db, served)

	// All repo-scoped format handlers, including the npm publish/unpublish
	// catch-all, share the /repos/{name} subrouter. A sibling /repos/{x}/*
	// route outside it is shadowed by the mount and 404s.
	a.web.uploads.npm = api.NewNPMUploadHandler(log, db, a.store.blobs, baseURL, a.reg)
	a.web.uploads.npm.SetNPMWorker(a.workers.npm)
	f.gomod = formatgomod.NewHTTPHandler(log, db, served, baseURL)

	f.docker = formatdocker.NewHTTPHandler(log, db, served, baseURL)
	f.docker.SetUploadDir(dockerUploadDir(a.cfg))
	f.docker.SetAuthorizer(a.web.evaluator)
}

// hostedRouters returns in-memory routers of the hosted handlers per format.
// Proxy and group repositories are served by the dispatcher in front of the
// per-format routes; hosted repositories fall through to them, and groups read
// their hosted members through these routers.
func (a *app) hostedRouters() map[string]http.Handler {
	f := &a.web.formats

	routers := map[string]*chi.Mux{}
	for _, name := range []string{"maven", "deb", "rpm", "pypi", "raw", "npm", "cargo", "go", "nuget", "docker", "helm"} {
		routers[name] = chi.NewRouter()
	}

	f.maven.RegisterRepoRoutes(routers["maven"])
	f.raw.RegisterRepoRoutes(routers["raw"])
	f.npm.RegisterRepoRoutes(routers["npm"])
	f.nuget.RegisterRepoRoutes(routers["nuget"])
	f.cargo.RegisterRepoRoutes(routers["cargo"])
	f.pypi.RegisterRepoRoutes(routers["pypi"])
	f.helm.RegisterRepoRoutes(routers["helm"])
	// Read-only view of the GOPROXY handler: group members are only ever read.
	f.gomod.RegisterRepoRoutes(routers["go"])
	f.docker.RegisterRoutes(routers["docker"])
	f.deb.RegisterRepoRoutes(routers["deb"])
	f.rpm.RegisterRepoRoutes(routers["rpm"])

	hosted := make(map[string]http.Handler, len(routers))
	for name, r := range routers {
		hosted[name] = r
	}

	return hosted
}

// registerFormatRoutes mounts the repository protocol handlers behind the
// proxy/group dispatcher.
func (a *app) registerFormatRoutes() {
	a.newFormatHandlers()

	w := &a.workers
	repoDispatch := repodispatch.New(a.log, a.db, w.proxySvc, a.web.evaluator, a.hostedRouters()).
		WithBaseURL(a.cfg.BaseURL).WithGroupMetadata(a.store.blobs, w.groupMeta)

	a.mountRepoRoutes(repoDispatch)
	a.mountIsolatedRepoRoutes(repoDispatch)
}

// mountRepoRoutes registers every repo-scoped format handler on the single
// /repos/{name} subrouter and dispatches the shared PUT /* catch-all.
func (a *app) mountRepoRoutes(repoDispatch *repodispatch.Dispatcher) {
	f, npmUpload := &a.web.formats, a.web.uploads.npm
	reg := a.reg

	a.web.authed.Route("/repos/{name}", func(r chi.Router) {
		r.Use(repoDispatch.Middleware)
		r.Use(f.npm.ShadowGuard)
		f.rpm.RegisterRepoRoutes(r)
		f.deb.RegisterRepoRoutes(r)
		f.helm.RegisterRepoRoutes(r)
		f.pypi.RegisterRepoRoutes(r)
		f.nuget.RegisterRepoRoutes(r)
		f.npm.RegisterRepoRoutes(r)
		f.cargo.RegisterRepoRoutes(r)
		f.rubygems.RegisterRepoRoutes(r)
		f.conda.RegisterRepoRoutes(r)
		f.conan.RegisterRepoRoutes(r)
		f.gomod.RegisterRepoRoutes(r)
		npmUpload.RegisterRepoRoutes(r)
		// npm publish and GOPROXY upload both need PUT "/*"; dispatch by tail.
		// npmUpload counts its own uploads; the GOPROXY branch is counted here.
		goUpload := middleware.CountUploads(reg, metrics.FormatGo, f.gomod.HandlePut)
		r.Put("/*", func(w http.ResponseWriter, req *http.Request) {
			if formatgomod.IsUploadPath(chi.URLParam(req, "*")) {
				goUpload(w, req)
				return
			}

			npmUpload.HandlePublish(w, req)
		})
	})
	a.log.Infow("Repository HTTP handlers registered", "prefix", "/repos/{name}")
}

// mountIsolatedRepoRoutes registers the wildcard handlers that would shadow
// other routes on /repos/{name}, each on a prefix of its own, and the OCI
// registry.
func (a *app) mountIsolatedRepoRoutes(repoDispatch *repodispatch.Dispatcher) {
	f, authedAPI := &a.web.formats, a.web.authed

	// Wildcard handlers isolated to avoid shadowing specific routes.
	authedAPI.Route("/repos/raw/{name}", func(r chi.Router) {
		r.Use(repoDispatch.Middleware)
		f.raw.RegisterRepoRoutes(r)
	})
	a.log.Infow("Raw handler registered", "prefix", "/repos/raw/{name}")

	// Maven, SBT, Ivy use wildcard paths that shadow other handlers — isolate them.
	authedAPI.Route("/repos/maven/{name}", func(r chi.Router) {
		r.Use(repoDispatch.Middleware)
		f.maven.RegisterRepoRoutes(r)
	})
	authedAPI.Route("/repos/sbt/{name}", func(r chi.Router) { f.sbt.RegisterRepoRoutes(r) })
	authedAPI.Route("/repos/ivy/{name}", func(r chi.Router) { f.ivy.RegisterRepoRoutes(r) })
	a.log.Infow("Maven/SBT/Ivy handlers registered (isolated prefixes)")
	// The registry (/v2/...) sits behind the dispatcher too: docker proxy and
	// group repositories are served there, hosted ones by dockerHTTP.
	authedAPI.Group(func(r chi.Router) {
		r.Use(repoDispatch.OCIMiddleware)
		f.docker.RegisterRoutes(r)
	})
}

// registerRepositoryRoutes registers repository management: the repositories
// API, federation, cleanup, docker GC, trash and release bundles.
func (a *app) registerRepositoryRoutes() {
	db, log, authedAPI, evaluator := a.db, a.log, a.web.authed, a.web.evaluator
	w := &a.workers
	regenerator := func() api.TrashRegenerator { return api.NewWorkerRegenerator(w.deb, w.rpm, w.maven, w.npm) }

	repoHandler := api.NewRepositoriesHandler(db, log, evaluator).WithBlobStore(a.store.blobs).WithPublisher(w.webhook).WithGroupMetadata(w.groupMeta).WithTasks(a.tasks)
	if a.cfg.Federation.Enabled {
		repoHandler.WithFederation(a.feat.fed.store)
	}

	repoHandler.RegisterAuthed(authedAPI)

	api.NewFederationHandler(db, log, evaluator, a.feat.fed.store, a.cfg.Federation.Enabled, a.feat.fed.kick).RegisterAuthed(authedAPI)

	api.NewCleanupHandler(db, log, evaluator, w.cleanup).RegisterAuthed(authedAPI)

	api.NewDockerGCHandler(db, log, evaluator, w.dockerGC).RegisterAuthed(authedAPI)

	api.NewTrashHandler(db, log, evaluator).
		WithBlobStore(a.store.blobs).
		WithPublisher(w.webhook).
		WithRegenerator(regenerator()).
		WithGroupMetadata(w.groupMeta).
		WithTasks(a.tasks).
		RegisterAuthed(authedAPI)
	// The retention purge worker is started above; the manual purge lives in
	// TrashHandler.

	api.NewBundlesHandler(db, log, evaluator).
		WithGPGKeys(w.gpgKeys).
		WithPublisher(w.webhook).
		WithRegenerator(regenerator()).
		RegisterAuthed(authedAPI)
}

// registerSupplyChainRoutes registers SBOM, VEX and attestation routes.
func (a *app) registerSupplyChainRoutes() {
	cfg, db, log, authedAPI, evaluator := a.cfg, a.db, a.log, a.web.authed, a.web.evaluator

	sbomGen := sbom.NewGenerator(db, log).WithBlobStore(a.store.blobs).WithToolVersion(version).WithBaseURL(cfg.BaseURL)
	a.feat.sbomGen = sbomGen

	// SBOM / VEX attestations: signed with a configured or generated key; the
	// public half is served unauthenticated so anyone can verify.
	attestSvc := attest.NewService(cfg.Attest.KeyPath, db)
	attestHandler := api.NewAttestationHandler(attestSvc, log, evaluator)
	attestHandler.RegisterPublic(a.web.router)
	attestHandler.RegisterAuthed(authedAPI)

	sbomHandler := api.NewSBOMHandler(db, log, evaluator, sbomGen, a.store.blobs).WithAttestation(attestSvc)
	a.feat.attacher = api.NewAttestationAttacher(db, log, sbomGen, attestSvc, a.web.formats.docker)
	sbomHandler.WithAttacher(a.feat.attacher).RegisterAuthed(authedAPI)
	api.NewAttestationAttachHandler(db, log, evaluator, a.feat.attacher, a.store.blobs).RegisterAuthed(authedAPI)
	api.NewVEXHandler(db, log, evaluator, sbomGen).WithAttestation(attestSvc).RegisterAuthed(authedAPI)
}

// registerDependencyTrack starts the Dependency-Track pusher, which pushes the
// BOM on upload and scan, then the VEX decisions, and registers its API.
func (a *app) registerDependencyTrack() {
	db, log := a.db, a.log

	dtPusher := dtrack.NewPusher(db, api.NewDTrackSources(db, a.feat.sbomGen), log)
	dtPusher.Start(context.Background())

	a.extraStops = append(a.extraStops, dtPusher.Stop)

	api.NewDependencyTrackHandler(db, log, a.web.evaluator, dtPusher).RegisterAuthed(a.web.authed)

	a.feat.dtPusher = dtPusher
	a.feat.dtPublisher = dtrack.NewUploadPublisher(a.feat.uploadPublisher, dtPusher)
}

// registerWebhooksAndBackup registers the webhooks and backup APIs.
func (a *app) registerWebhooksAndBackup() {
	db, log, authedAPI, evaluator := a.db, a.log, a.web.authed, a.web.evaluator
	w := &a.workers

	api.NewWebhooksHandler(db, log, evaluator, w.webhook).RegisterAuthed(authedAPI)

	backupEngine := backup.NewEngine(db, a.store.blobs, log, a.cfg.Backup.BackupDir)
	backupEngine.SetLeaseTTL(a.cfg.Backup.LeaseTTL)
	backupEngine.SetRegenerator(api.NewWorkerRegenerator(w.deb, w.rpm, w.maven, w.npm))
	api.NewBackupHandler(db, log, evaluator, backupEngine).WithTasks(a.tasks).RegisterAuthed(authedAPI)
}

// registerScanningRoutes registers license, secret and vulnerability scanning,
// quotas, properties, AI and delta routes, and the maven upload API.
func (a *app) registerScanningRoutes() {
	db, log, authedAPI, evaluator := a.db, a.log, a.web.authed, a.web.evaluator
	blobStore := a.store.blobs

	licenseHandler := api.NewLicenseHandler(db, log, evaluator, license.NewDetector(log), blobStore)
	licenseHandler.RegisterRoutes(authedAPI)

	secretsHandler := api.NewSecretsHandler(db, log, secrets.NewScanner(log))
	secretsHandler.RegisterRoutes(authedAPI, "/api/v1/repositories/{name}")

	a.registerVulnRoutes()

	quotasHandler := api.NewQuotasHandler(log, db, evaluator)
	quotasHandler.RegisterRoutes(authedAPI, "/api/v1/repositories/{name}")

	propertiesHandler := api.NewPropertiesHandler(log, db, blobStore)
	propertiesHandler.RegisterRoutes(authedAPI, "/api/v1/repositories/{name}")

	aiHandler := api.NewAIHandler(db, log, evaluator, a.feat.aiWorker)
	aiHandler.RegisterRoutes(authedAPI, "/api/v1/repositories/{name}")
	deltaHandler := api.NewDeltaHandler(log, db, blobStore)
	deltaHandler.Register(authedAPI)

	a.web.uploads.maven = api.NewMavenUploadHandler(log, db, blobStore, a.reg)
	a.web.uploads.maven.SetMavenWorker(a.workers.maven)
	a.web.uploads.maven.Register(authedAPI)
}

// registerVulnRoutes starts the scanner registry and registers the
// vulnerability scanning routes. The scan hook and the scans themselves run
// under the server lifecycle.
func (a *app) registerVulnRoutes() {
	// Trivy and grype are external binaries; the registry downloads and
	// refreshes their databases in the background (docs/security/scanning.md).
	scannerRegistry := vuln.NewRegistry(a.log, a.cfg.Scanners.VulnOptions()).WithMetrics(a.reg)
	scannerRegistry.LogStatus()
	scannerRegistry.Start(context.Background())

	a.extraStops = append(a.extraStops, scannerRegistry.Stop)

	vulnHandler := api.NewVulnHandler(a.db, a.log, a.web.evaluator, scannerRegistry).WithTasks(a.tasks).WithScanHook(a.onScanned)
	vulnHandler.RegisterRoutes(a.web.authed, "/api/v1/repositories/{name}")
	vulnHandler.RegisterOverviewRoutes(a.web.authed)
}

// onScanned pushes a stored scan to Dependency-Track and attaches its
// attestation, both bound to the server lifecycle.
func (a *app) onScanned(repoID, artifactID string) {
	a.feat.dtPusher.EnqueueIfEnabled(a.tasks.Context(), dtrack.Job{RepoID: repoID, ArtifactID: artifactID, Trigger: dtrack.TriggerScan})

	a.tasks.Go(func(ctx context.Context) { a.feat.attacher.OnScan(ctx, repoID, artifactID) })
}

// finishRoutes completes the router: the upload announcements, the MCP
// endpoint, the SPA fallback, the blob relay and federation endpoints, and the
// mount of the authenticated router.
func (a *app) finishRoutes() {
	cfg, w := a.cfg, &a.web
	router, authedAPI, f, u := w.router, w.authed, &w.formats, &w.uploads

	// Every handler that stores or removes artifacts announces artifact.uploaded /
	// artifact.deleted through the webhook dispatcher. The download-only rpm and
	// deb HTTP handlers do not publish events.
	for _, h := range []interface{ SetPublisher(webhook.Publisher) }{
		u.rpm, u.deb, u.presigned, u.npm, u.maven,
		f.helm, f.pypi, f.nuget, f.npm, f.cargo, f.rubygems, f.raw,
		f.conda, f.conan, f.ivy, f.sbt, f.maven, f.docker, f.gomod,
	} {
		h.SetPublisher(a.feat.dtPublisher)
	}

	// Mount the MCP server if enabled (HTTP is the only transport). Mounted under authedAPI
	// (not the bare router) so it inherits the RBAC and audit chain: RBAC accepts
	// only bearer credentials here (never the session cookie) and refuses
	// anonymous callers; each tool then checks the caller's own permissions.
	if a.feat.mcpServer != nil {
		authedAPI.Mount(cfg.MCP.HTTPPath, a.feat.mcpServer.HTTPHandler())
		a.log.Infow("MCP HTTP endpoint registered", "path", cfg.MCP.HTTPPath)
	}

	// SPA fallback on the authed sub-router: paths not matched by any API or
	// repo route serve the Vue SPA (client-side routing). RBAC is bypassed for
	// these paths by the early-exit in RBACMiddleware.
	authedAPI.NotFound(w.spaHandler.ServeHTTP)

	// Filesystem and OCI registry backends cannot hand out presigned URLs, so
	// they serve their own: short-lived HMAC-signed PUT URLs for presigned
	// uploads. The signature in the URL is the credential, so the route sits
	// outside the authenticated router.
	if relay, ok := a.store.raw.(storage.RelayServer); ok {
		router.Put(storage.RelayRoute, relay.RelayHandler().ServeHTTP)
	}

	// Peer endpoints authenticate with their own per-repository bearer token,
	// so they sit outside the RBAC/CSRF chain of authedAPI.
	if a.feat.fed.server != nil {
		router.Mount("/federation/v1", a.feat.fed.server.Handler())
	}

	router.Mount("/", authedAPI)
}
