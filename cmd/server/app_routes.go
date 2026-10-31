// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/backup"
	"github.com/openartifact/openartifact/internal/dtrack"
	"github.com/openartifact/openartifact/internal/format/plugin"
	"github.com/openartifact/openartifact/internal/importmeta"
	"github.com/openartifact/openartifact/internal/license"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/repodispatch"
	"github.com/openartifact/openartifact/internal/search"
	"github.com/openartifact/openartifact/internal/secrets"
	"github.com/openartifact/openartifact/internal/storage"
)

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

	w.hmacKey = hmacKey

	api.NewOIDCHandler(db, log, evaluator, hmacKey, cfg.BaseURL).WithTrustedProxies(w.trustedProxies).RegisterPublic(router)
	api.NewSAMLHandler(db, log, evaluator, hmacKey, cfg.BaseURL, cfg).WithTrustedProxies(w.trustedProxies).RegisterPublic(router)
	api.NewOIDCExchangeHandler(db, log, w.trustedProxies).RegisterPublic(router)

	w.public.sysInfo = api.NewSystemInfoHandler(version, commit, buildTime, db, a.store.blobs, cfg.Storage.Type).
		WithDocsBaseURL(cfg.Docs.BaseURL).
		WithAPIReference(cfg.API.Docs.Enabled).
		WithAIAvailable(aiBuilt)
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
	a.registerMCPMetadata(router)

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
	authedAPI.Use(middleware.CSRFMiddleware(a.cfg.BaseURL, w.trustedProxies))
	authedAPI.Use(middleware.AuditMiddleware(a.auditLogger, w.trustedProxies))

	w.authed = authedAPI
}

// registerAdminRoutes registers the account, administration and project
// routes and search.
func (a *app) registerAdminRoutes() {
	cfg, db, log := a.cfg, a.db, a.log
	w := &a.web
	authedAPI, evaluator := w.authed, w.evaluator

	w.public.auth.RegisterAuthed(authedAPI)

	api.NewAdminSSOHandler(db, log, evaluator, cfg.BaseURL, cfg).RegisterAuthed(authedAPI)

	api.NewAdminUsersHandler(db, log, evaluator).Register(authedAPI)
	api.NewAdminServiceAccountsHandler(db, log, evaluator).Register(authedAPI)
	api.NewAdminOIDCPoliciesHandler(db, log, evaluator).Register(authedAPI)
	api.NewAdminGroupsHandler(db, log, evaluator).Register(authedAPI)
	api.NewAdminPermissionsHandler(db, log, evaluator).Register(authedAPI)
	api.NewAdminDeclarativeHandler(db, log, evaluator).Register(authedAPI)
	api.NewAdminTokensHandler(db, log, evaluator).Register(authedAPI)
	api.NewSelfTokensHandler(db, log, evaluator).Register(authedAPI)
	api.NewAdminAuditHandler(db, log, evaluator).Register(authedAPI)
	api.NewAdminStorageHandler(db, log, evaluator).WithPrimary(cfg.Storage, a.store.raw).Register(authedAPI)
	api.NewAdminTieringHandler(db, a.store.tiers, a.workers.tierEngine, a.workers.tierWorker, log, evaluator).Register(authedAPI)
	api.NewAdminReplicationHandler(a.store.replAdmin, log, evaluator).Register(authedAPI)
	api.NewAdminRecoveryHandler(a.workers.journal, log, evaluator).Register(authedAPI)
	api.NewAdminGPGHandler(db, log, evaluator, a.workers.gpgKeys).Register(authedAPI)
	api.NewApkKeyHandler(db, log, evaluator, a.workers.apkKeys).Register(authedAPI)
	api.NewNixKeyHandler(db, log, evaluator, a.workers.nixKeys).Register(authedAPI)
	w.public.sysInfo.RegisterAuthed(authedAPI)
	w.public.brand.RegisterAuthed(authedAPI)
	api.NewFrontendTelemetryHandler(cfg.Telemetry, a.reg, log).RegisterAuthed(authedAPI)

	if cfg.Server.PprofEnabled {
		registerPprof(authedAPI)
		log.Warnw("pprof enabled at /api/v1/admin/pprof/ (admin only); disable outside diagnostics")
	}

	projectsAPI := api.NewProjectsHandler(db, log, evaluator, w.projectSettings)
	projectsAPI.SetCleanupPreviewer(a.workers.cleanup)
	projectsAPI.RegisterRoutes(authedAPI)

	searchHandler := search.NewHandler(db.GetDB(), evaluator, log, search.WithAuthCache(db))
	authedAPI.Get("/api/v1/search", searchHandler.ServeHTTP)
}

// newFormatHandlers builds the native protocol handler of every format.
func (a *app) newFormatHandlers() error {
	hosted, err := plugin.BuildHosted(a.formatDeps())
	if err != nil {
		return a.fail("failed to build the format handlers", err)
	}

	// The registry stores the attestations beside the images they describe.
	referrers, ok := hosted["docker"].(api.ReferrerRegistry)
	if !ok {
		return a.fail("failed to build the format handlers", errNoReferrerRegistry)
	}

	a.web.hosted, a.web.referrers = hosted, referrers

	return nil
}

// errNoReferrerRegistry: the docker handler cannot store referrers.
var errNoReferrerRegistry = errors.New("the docker handler is not a referrer registry")

// formatDeps are the services the formats' handlers are built from. A service
// this process lacks (the route tests build none) is left out, not passed as a
// typed nil.
func (a *app) formatDeps() *plugin.Deps {
	log, db, blobStore, wk := a.log, a.db, a.store.blobs, &a.workers

	deps := &plugin.Deps{
		Log: log,
		DB:  db,
		// The served store is read from with presigned redirects; writers use
		// the plain one.
		Served: a.store.served,
		Blobs:  blobStore,
		// BaseURL is BASE_URL as an override only: unset, the handlers derive the
		// origin of the URLs they hand to clients from each request.
		BaseURL:         a.cfg.ClientBaseURL(),
		Metrics:         a.reg,
		HMACKey:         a.web.hmacKey,
		DockerUploadDir: dockerUploadDir(a.cfg),
		Workers:         wk.metadata,
		Proxy:           wk.proxySvc,
		CountUploads: func(format string, next http.HandlerFunc) http.HandlerFunc {
			return middleware.CountUploads(a.reg, format, next)
		},
		REST: a.restUploadHandlers(),
	}

	if a.web.evaluator != nil {
		deps.DockerAuthorizer = a.web.evaluator
	}

	if wk.apkKeys != nil {
		deps.APKKeys = wk.apkKeys
	}

	if wk.nixKeys != nil {
		deps.NixKeys = wk.nixKeys
	}

	if wk.gpgKeys != nil {
		deps.GPG = wk.gpgKeys
	}

	return deps
}

// restUploadHandlers builds the REST upload handlers of internal/api, by the
// format they upload to. Their formats mount them (plugin.Deps.REST): they
// register on the authenticated API router, or on the repository mount for npm.
func (a *app) restUploadHandlers() map[string]any {
	db, log, blobStore, reg := a.db, a.log, a.store.blobs, a.reg
	wk := &a.workers

	rpm := api.NewRPMUploadHandler(log, db, blobStore, reg)
	rpm.SetDebWorker(wk.deb)
	rpm.SetRPMWorker(wk.rpm)
	rpm.SetPackageDeleter(wk.cleanup)
	rpm.SetKeyResolver(wk.gpgKeys)

	deb := api.NewDebUploadHandler(log, db, blobStore, reg)
	deb.SetDebWorker(wk.deb)

	raw := api.NewPresignedUploadHandler(
		log,
		db,
		blobStore,
		a.web.evaluator,
		a.cfg.Storage.PresignUploadTTL,
		a.cfg.Storage.PresignMaxPendingUploads,
		reg,
	)

	npm := api.NewNPMUploadHandler(log, db, blobStore, reg)
	npm.SetNPMWorker(wk.npm)

	maven := api.NewMavenUploadHandler(log, db, blobStore, reg).WithTasks(a.tasks)
	maven.SetMavenWorker(wk.maven)

	return map[string]any{"rpm": rpm, "deb": deb, "raw": raw, "npm": npm, "maven": maven}
}

// hostedRouters returns in-memory routers of the hosted handlers per format.
func (a *app) hostedRouters() map[string]http.Handler {
	return plugin.ReadRouters(a.web.hosted)
}

// registerFormatRoutes mounts the repository protocol handlers behind the
// proxy/group dispatcher.
func (a *app) registerFormatRoutes() error {
	if err := a.newFormatHandlers(); err != nil {
		return err
	}

	w := &a.workers
	repoDispatch := repodispatch.New(a.log, a.db, w.proxySvc, a.web.evaluator, a.hostedRouters()).
		WithBaseURL(a.cfg.ClientBaseURL()).WithGroupMetadata(a.store.blobs, w.groupMeta).WithApkKeys(w.apkKeys).WithNixKeys(w.nixKeys)

	if err := a.mountFormats(repoDispatch); err != nil {
		return a.fail("failed to mount the format handlers", err)
	}

	return nil
}

// mountFormats mounts every format where its Spec places it, then the REST
// upload endpoints. The isolated mounts go before the API routes: a route on a
// path an isolated mount also serves would be overridden by it.
func (a *app) mountFormats(repoDispatch *repodispatch.Dispatcher) error {
	authed, hosted := a.web.authed, a.web.hosted

	if err := plugin.MountShared(authed, hosted, repoDispatch.Middleware); err != nil {
		return err //nolint:wrapcheck // the caller reports it
	}

	plugin.MountIsolated(authed, hosted, repoDispatch.Middleware)
	plugin.MountOCI(authed, hosted, repoDispatch.OCIMiddleware)
	plugin.RegisterAPIRoutes(authed, hosted)

	a.log.Infow("Repository HTTP handlers registered")

	return nil
}

// registerRepositoryRoutes registers repository management: the repositories
// API, federation, cleanup, docker GC, trash and release bundles.
func (a *app) registerRepositoryRoutes() {
	db, log, authedAPI, evaluator := a.db, a.log, a.web.authed, a.web.evaluator
	w := &a.workers
	repoHandler := api.NewRepositoriesHandler(db, log, evaluator).
		WithBlobStore(a.store.blobs).
		WithPublisher(w.webhook).
		WithGroupMetadata(w.groupMeta).
		WithTasks(a.tasks)
	if a.cfg.Federation.Enabled {
		repoHandler.WithFederation(a.feat.fed.store)
	}

	repoHandler.RegisterAuthed(authedAPI)

	api.NewFederationHandler(db, log, evaluator, a.feat.fed.store, a.cfg.Federation.Enabled, a.feat.fed.kick).RegisterAuthed(authedAPI)

	api.NewCleanupHandler(db, log, evaluator, w.cleanup).RegisterAuthed(authedAPI)

	api.NewCurationHandler(db, log, evaluator, w.proxySvc).RegisterAuthed(authedAPI)

	api.NewDockerGCHandler(db, log, evaluator, w.dockerGC).RegisterAuthed(authedAPI)

	api.NewTrashHandler(db, log, evaluator).
		WithBlobStore(a.store.blobs).
		WithPublisher(w.webhook).
		WithWorkers(w.metadata).
		WithGroupMetadata(w.groupMeta).
		WithTasks(a.tasks).
		RegisterAuthed(authedAPI)
	// The retention purge worker is started above; the manual purge lives in
	// TrashHandler.

	api.NewBundlesHandler(db, log, evaluator).
		WithGPGKeys(w.gpgKeys).
		WithBundleSigners(a.cfg.GPG.BundleSigners).
		WithPublisher(w.webhook).
		WithWorkers(w.metadata).
		RegisterAuthed(authedAPI)
}

// registerSupplyChainRoutes registers SBOM, VEX and attestation routes.
func (a *app) registerSupplyChainRoutes() {
	db, log, authedAPI, evaluator := a.db, a.log, a.web.authed, a.web.evaluator

	a.buildSupplyChain(a.web.referrers)

	sbomGen, attestSvc := a.feat.sbomGen, a.workers.scan.attest
	attestHandler := api.NewAttestationHandler(attestSvc, log, evaluator)
	attestHandler.RegisterPublic(a.web.router)
	attestHandler.RegisterAuthed(authedAPI)

	sbomHandler := api.NewSBOMHandler(db, log, evaluator, sbomGen, a.store.blobs).WithAttestation(attestSvc)
	sbomHandler.WithAttacher(a.feat.attacher).RegisterAuthed(authedAPI)
	api.NewAttestationAttachHandler(db, log, evaluator, a.feat.attacher, a.store.blobs).RegisterAuthed(authedAPI)
	api.NewVEXHandler(db, log, evaluator, sbomGen).WithAttestation(attestSvc).RegisterAuthed(authedAPI)
}

// registerDependencyTrack builds the Dependency-Track pusher
// (buildDependencyTrack) and registers its API.
func (a *app) registerDependencyTrack() {
	a.buildDependencyTrack()

	api.NewDependencyTrackHandler(a.db, a.log, a.web.evaluator, a.feat.dtPusher).RegisterAuthed(a.web.authed)

	// Every upload is also recorded as an audit event of its repository.
	a.feat.dtPublisher = dtrack.NewUploadPublisher(middleware.NewUploadAuditPublisher(a.feat.uploadPublisher), a.feat.dtPusher)
}

// registerWebhooksAndBackup registers the webhooks and backup APIs.
func (a *app) registerWebhooksAndBackup() {
	db, log, authedAPI, evaluator := a.db, a.log, a.web.authed, a.web.evaluator
	w := &a.workers

	api.NewWebhooksHandler(db, log, evaluator, w.webhook).RegisterAuthed(authedAPI)

	backupEngine := backup.NewEngine(db, a.store.blobs, log, a.cfg.Backup.BackupDir)
	backupEngine.SetLeaseTTL(a.cfg.Backup.LeaseTTL)
	backupEngine.SetWorkers(w.metadata)
	api.NewBackupHandler(db, log, evaluator, backupEngine).WithTasks(a.tasks).RegisterAuthed(authedAPI)
}

// registerScanningRoutes registers license, secret and vulnerability scanning,
// quotas, properties, AI and delta routes.
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
	quotasHandler.RegisterAdminRoutes(authedAPI)

	api.NewRepositoryActivityHandler(db, log, evaluator).RegisterAuthed(authedAPI)

	propertiesHandler := api.NewPropertiesHandler(log, db, blobStore)
	propertiesHandler.RegisterRoutes(authedAPI, "/api/v1/repositories/{name}")

	a.registerAIRoutes(authedAPI)
	deltaHandler := api.NewDeltaHandler(log, db, blobStore)
	deltaHandler.Register(authedAPI)
}

// registerVulnRoutes builds the vulnerability scanning services
// (buildVulnScanning) and registers their routes.
func (a *app) registerVulnRoutes() {
	vulnHandler := a.buildVulnScanning()
	vulnHandler.RegisterRoutes(a.web.authed, "/api/v1/repositories/{name}")
	vulnHandler.RegisterOverviewRoutes(a.web.authed)

	api.NewSecurityHandler(a.db, a.log, a.web.evaluator, a.workers.scan.scanners, a.feat.sbomGen, a.aiEnabled()).
		WithExploit(a.workers.exploit).RegisterRoutes(a.web.authed)
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
	w := &a.web
	router, authedAPI := w.router, w.authed

	// Every handler that stores or removes artifacts announces artifact.uploaded /
	// artifact.deleted through the webhook dispatcher. The download-only rpm and
	// deb HTTP handlers do not publish events.
	plugin.SetPublisher(w.hosted, a.feat.dtPublisher)

	// The MCP server, when enabled and built in (feature_mcp.go).
	a.mountMCP(authedAPI)

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

	// Terraform service discovery and the signed download URLs are fetched
	// without credentials (the URL signature is the credential), so they sit
	// outside the authenticated router.
	plugin.RegisterPublicRoutes(router, w.hosted)

	// Peer endpoints authenticate with their own per-repository bearer token,
	// so they sit outside the RBAC/CSRF chain of authedAPI.
	if a.feat.fed.server != nil {
		router.Mount("/federation/v1", a.feat.fed.server.Handler())
	}

	router.Mount("/", authedAPI)
}
