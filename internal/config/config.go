// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/telemetry"
	"go.yaml.in/yaml/v3"
)

// placeholderPassword is the value the example files ship for secrets; a server
// started with it as the database password is a copied-and-forgotten config.
const placeholderPassword = "CHANGE_ME"

var (
	errInvalidPort        = errors.New("invalid server port")
	errMalformedKeyValue  = errors.New("expected key=value")
	errMCPTransport       = errors.New(`mcp.transport must be "http" (stdio is not supported)`)
	errMCPLimits          = errors.New("mcp.maxBodyBytes must be positive; mcp.rateLimitPerMinute and mcp.aiTriggerPerMinute must not be negative")
	errMCPOrigin          = errors.New("mcp.allowedOrigins entries must be origins like https://host[:port]")
	errMissingDBDriver    = errors.New("database driver is required")
	errMissingStorageType = errors.New("storage type is required")
	errMissingLogLevel    = errors.New("logger level is required")
	errTLSFilesRequired   = errors.New("server TLS is enabled but tlsCert/tlsKey are not set")

	errNegativeDockerGCGrace    = errors.New("workers.dockerGcGracePeriod must not be negative")
	errNegativeDigestTTL        = errors.New("workers.proxyDockerDigestTtl must not be negative")
	errNegativeWebhookRetention = errors.New("workers.webhookDeliveryRetentionDays must not be negative")
	errNegativeTrashRetention   = errors.New("trash.retentionDays must not be negative")
	errBlobDeletionBatch        = errors.New("blobDeletion.sweepBatch must be at least 1")
	errBlobDeletionNegative     = errors.New("blobDeletion.grace must not be negative")
	errBackupLeaseTTL           = errors.New("backup.leaseTtl must be positive")
	errRecoveryBatch            = errors.New("recovery.journal.batch and recovery.journal.concurrency must be at least 1")
	errRecoveryNegative         = errors.New("recovery.journal.interval must be positive when the journal is enabled and recovery.journal.reconcileInterval must not be negative")
	errTieringBatch             = errors.New("tiering.batch and tiering.concurrency must be at least 1")
	errTieringNegative          = errors.New("tiering.maxBytesPerSecond, accessFlushInterval and refreshInterval must not be negative")
	errTieringPoolSize          = errors.New("database.maxOpenConns must be 0 (driver default) or at least 4 while tiering.enabled is true: a mover pass pins one connection for its cluster lock, and blob-lock transactions need two each")
	errPlaceholderDBPassword    = errors.New("database.password is still the CHANGE_ME placeholder from the example files: set a real password")
	errMaxOpenConnsTooSmall     = errors.New("database.maxOpenConns must be 0 (driver default) or at least 2: a blob-lock transaction pins one connection and its callback needs another")

	errPresignEndpoint   = errors.New("storage.presignEndpoint must be an absolute http(s) URL without query or fragment")
	errPresignMinSize    = errors.New("storage.presignMinSize must not be negative")
	errPresignMaxPending = errors.New("storage.presignMaxPendingUploads must not be negative")
)

type Config struct {
	Server   ServerConfig            `yaml:"server"`
	Database database.DatabaseConfig `yaml:"database"`
	Storage  storage.StorageConfig   `yaml:"storage"`
	Logger   LoggerConfig            `yaml:"log"`
	GPG      GPGConfig               `yaml:"gpg"`
	Attest   AttestationConfig       `yaml:"attestation"`
	Auth     AuthConfig              `yaml:"auth"`
	Backup   BackupConfig            `yaml:"backup"`
	AI       AIConfig                `yaml:"ai"`
	MCP      MCPConfig               `yaml:"mcp"`
	Workers  WorkersConfig           `yaml:"workers"`
	Trash    TrashConfig             `yaml:"trash"`
	// BlobDeletion tunes the deferred deletion of blob bytes.
	BlobDeletion BlobDeletionConfig `yaml:"blobDeletion"`
	Tiering      TieringConfig      `yaml:"tiering"`
	// Recovery tunes the disaster-recovery row journal (see
	// docs/operations/rebuild-from-storage.md).
	Recovery RecoveryConfig `yaml:"recovery"`
	Projects ProjectsConfig `yaml:"projects"`
	// Telemetry configures OpenTelemetry export (off by default), see
	// docs/operations/monitoring.md.
	Telemetry telemetry.Config `yaml:"telemetry"`
	Scanners  ScannersConfig   `yaml:"scanners"`
	BaseURL   string           `yaml:"baseUrl"`

	// StorageReplica is the optional blob replica, the storage.replica block
	// (decoded by loadReplicaFromYAML, see docs/operations/replication.md).
	StorageReplica storage.ReplicaConfig `yaml:"-"`
}

// ProjectsConfig tunes project self-service (see docs/security/projects.md).
type ProjectsConfig struct {
	// SelfCreate lets any authenticated human user create a project and become
	// its owner. Off by default: only global admins create projects. An
	// administrator can change it at runtime (system setting projects.self_create),
	// which then takes precedence over this default.
	SelfCreate bool `yaml:"selfCreate"`
}

// WorkersConfig contains background worker tuning.
type WorkersConfig struct {
	// MetadataRegenConcurrency caps how many metadata regenerations (npm,
	// maven, deb, rpm together) run at once. Each pins up to two database
	// connections, so keep it well below database.maxOpenConns.
	MetadataRegenConcurrency int `yaml:"metadataRegenConcurrency"`
	// DockerGCInterval is the pause between scheduled Docker registry garbage
	// collection passes (untagged manifests and the blobs only they used).
	// Zero or negative disables the schedule; the admin API can still run it.
	DockerGCInterval time.Duration `yaml:"dockerGcInterval"`
	// DockerGCGracePeriod is how long an untagged manifest, or an unreferenced
	// blob, must have been idle before registry GC may delete it. It must
	// outlast the slowest push: layers are uploaded before the manifest that
	// references them.
	DockerGCGracePeriod time.Duration `yaml:"dockerGcGracePeriod"`
	// ProxyDockerDigestTTL is how long a docker proxy keeps a manifest or blob
	// addressed by digest after its last pull. Such content never changes, so
	// this only bounds the cache's size: the proxy cache worker evicts what has
	// not been pulled for this long, and the next pull fetches it again. Zero
	// keeps it until an admin purge or the repository's removal.
	ProxyDockerDigestTTL time.Duration `yaml:"proxyDockerDigestTtl"`
	// DockerUploadTTL is how long a blob upload session may sit idle before
	// the sweeper deletes it and its staged file. Zero or negative disables
	// the sweeper.
	DockerUploadTTL time.Duration `yaml:"dockerUploadTtl"`
	// DockerUploadDir is where in-progress blob uploads are staged. Empty
	// means /tmp/openartifact/docker-uploads.
	DockerUploadDir string `yaml:"dockerUploadDir"`
	// WebhookDeliveryRetentionDays is how many days finished (success or
	// failed) webhook delivery records are kept before the hourly purge deletes
	// them. 0 keeps them forever. Pending deliveries are never purged.
	WebhookDeliveryRetentionDays int `yaml:"webhookDeliveryRetentionDays"`
}

// TrashConfig tunes soft delete (see docs/operations/trash.md).
type TrashConfig struct {
	// RetentionDays is how many days a deleted artifact, version or
	// repository stays restorable before the purge worker removes it for good.
	// It applies to repositories without their own trash_retention_days;
	// 0 disables the trash (deletes are immediate and permanent).
	RetentionDays int `yaml:"retentionDays"`
	// PurgeInterval is the pause between purge passes. Zero or negative
	// disables the schedule: nothing expires until an admin purges by hand.
	PurgeInterval time.Duration `yaml:"purgeInterval"`
}

// TieringConfig tunes the storage tier mover (see docs/operations/storage-tiering.md).
// Tiers and policies themselves are managed through the admin API; with no
// tier besides the primary store configured none of this has any effect.
type TieringConfig struct {
	// Enabled switches the background mover on. Reads from every tier work
	// regardless; only the movement of blobs between tiers depends on it.
	Enabled bool `yaml:"enabled"`
	// Interval is the pause between mover passes. Zero or negative disables the
	// schedule; an admin can still start a pass by hand.
	Interval time.Duration `yaml:"interval"`
	// Batch caps how many blobs one pass moves; the rest wait for the next pass.
	Batch int `yaml:"batch"`
	// Concurrency is how many blobs are copied at once.
	Concurrency int `yaml:"concurrency"`
	// MaxBytesPerSecond caps the mover's total copy bandwidth (all concurrent
	// copies together). Zero is unlimited.
	MaxBytesPerSecond int64 `yaml:"maxBytesPerSecond"`
	// VerifyReadback re-reads each copied blob from the target tier and
	// compares its sha256 before the placement flips, on top of the size check.
	VerifyReadback bool `yaml:"verifyReadback"`
	// AccessFlushInterval is how often recorded blob downloads (used by
	// "idle for N days" policies and promote-on-read) are written to the database.
	AccessFlushInterval time.Duration `yaml:"accessFlushInterval"`
	// RefreshInterval is how often each replica reloads the tier list, so a tier
	// created on one replica is used by the others.
	RefreshInterval time.Duration `yaml:"refreshInterval"`
}

// RecoveryConfig groups the disaster-recovery settings.
type RecoveryConfig struct {
	Journal RecoveryJournalConfig `yaml:"journal"`
}

// RecoveryJournalConfig tunes the row journal that mirrors package metadata as
// sidecar blobs in the blob store (see docs/operations/rebuild-from-storage.md).
type RecoveryJournalConfig struct {
	// Enabled switches the journal on: the database triggers mark changed rows
	// and the worker writes their sidecar blobs. Off, nothing is captured and
	// `openartifact rebuild` cannot restore rows created while it was off.
	Enabled bool `yaml:"enabled"`
	// Interval is the pause between worker passes that drain the dirty queue.
	Interval time.Duration `yaml:"interval"`
	// Batch is how many dirty rows one drain step claims.
	Batch int `yaml:"batch"`
	// Concurrency is how many sidecar blobs are written or deleted at once.
	Concurrency int `yaml:"concurrency"`
	// ReconcileInterval is how often every journaled table is re-checked for
	// rows without a sidecar (backfill) and sidecars without a row. Zero
	// disables periodic reconciles after the first complete one.
	ReconcileInterval time.Duration `yaml:"reconcileInterval"`
}

type AuthConfig struct {
	AllowAnonymousRead   bool   `yaml:"allowAnonymousRead"`
	LegacyAPIKeyEnabled  bool   `yaml:"legacyApiKeyEnabled"`
	SessionDurationHours int    `yaml:"sessionDurationHours"`
	HMACKey              string `yaml:"hmacKey"`
	SAMLSPCertPEM        string `yaml:"samlSpCert"` // OPENARTIFACT_SAML_SP_CERT: PEM-encoded certificate
	SAMLSPKeyPEM         string `yaml:"samlSpKey"`  // OPENARTIFACT_SAML_SP_KEY: PEM-encoded private key
}

// ServerConfig contains HTTP server configuration.
type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	// ReadTimeout bounds an entire request (headers + body). Defaults to 0
	// (no limit) so large artifact uploads are never truncated; slowloris
	// protection instead comes from ReadHeaderTimeout below.
	ReadTimeout int `yaml:"readTimeout"` // seconds, 0 = no limit
	// WriteTimeout bounds writing the response. Defaults to 0 (no limit) —
	// large artifact downloads/streams rely on per-handler deadlines
	// instead of one server-wide cutoff (see api.HTTPServer).
	WriteTimeout int `yaml:"writeTimeout"` // seconds, 0 = no limit
	// ReadHeaderTimeout bounds only the time to read request headers, kept
	// short regardless of ReadTimeout to still mitigate slowloris-style
	// attacks even when body reads are unbounded.
	ReadHeaderTimeout int `yaml:"readHeaderTimeout"` // seconds
	// MaxBodyBytes caps the body of every request (http.MaxBytesReader), a
	// last line of defence behind the per-handler limits so no endpoint can be
	// made to spool or buffer an unbounded body. 0 disables the cap.
	MaxBodyBytes    int64    `yaml:"maxBodyBytes"`
	ShutdownTimeout int      `yaml:"shutdownTimeout"` // seconds
	TLSEnabled      bool     `yaml:"tlsEnabled"`
	TLSCert         string   `yaml:"tlsCert"`
	TLSKey          string   `yaml:"tlsKey"`
	APIKey          string   `yaml:"apiKey"`       // API key for write endpoint authentication (empty = auth disabled)
	CORSOrigins     []string `yaml:"corsOrigins"`  // Allowed CORS origins (default: none)
	InstanceID      string   `yaml:"instanceId"`   // Instance identifier for HA deployments
	IPAllowlist     []string `yaml:"ipAllowlist"`  // Allowed CIDR blocks (empty = allow all)
	RateLimitRPS    int      `yaml:"rateLimitRps"` // Rate limit in requests per second (0 = disabled)
	// TrustedProxies lists CIDR blocks of reverse proxies allowed to set
	// X-Forwarded-For/X-Real-IP. Empty (default) means no proxy is trusted —
	// the immediate TCP peer (RemoteAddr) is always used as the client IP,
	// so a client can never spoof its own IP via those headers.
	TrustedProxies []string `yaml:"trustedProxies"`
	// PprofEnabled mounts Go's runtime profiler under /api/v1/admin/pprof/
	// (admin "manage" permission required). Off by default: profiles expose
	// internals and CPU profiling costs cycles. Sets non-zero mutex and block
	// profile rates while enabled.
	PprofEnabled bool `yaml:"pprofEnabled"`
}

// LoggerConfig contains logging configuration.
type LoggerConfig struct {
	Level      string `yaml:"level"`  // "debug", "info", "warn", "error"
	Format     string `yaml:"format"` // "json", "text"
	OutputPath string `yaml:"output"` // "stdout", "stderr", or file path
}

type GPGConfig struct {
	Enabled      bool   `yaml:"enabled"`
	KeyPath      string `yaml:"keyPath"`
	Passphrase   string `yaml:"passphrase"`
	AutoGenerate bool   `yaml:"autoGenerate"`
}

// AttestationConfig configures the signing key of SBOM and VEX attestations.
type AttestationConfig struct {
	// KeyPath is an unencrypted PKCS#8 or SEC1 PEM private key (ECDSA or Ed25519).
	// Empty: a P-256 key is generated on first use and stored encrypted in the database.
	KeyPath string `yaml:"keyPath"`
}

// BackupConfig contains backup configuration.
type BackupConfig struct {
	BackupDir string `yaml:"backupDir"` // Directory to store backup files
	// LeaseTTL is how long a running backup's hold on deleted blob bytes
	// survives without a heartbeat. A running backup renews it every third of
	// this; a crashed backup stops holding deletions back once it expires.
	LeaseTTL time.Duration `yaml:"leaseTtl"`
}

// BlobDeletionConfig tunes the deferred deletion of blob bytes (see
// docs/operations/storage.md). Deleting a blob (or retiring a tier copy)
// removes its database row at once but queues its bytes, which a sweeper
// removes after the grace period and once no running backup can still need
// them; storage is freed then, not at the delete.
type BlobDeletionConfig struct {
	// Grace is how long the bytes of a deleted blob are kept before they may be
	// removed. During it a re-upload of the same content reuses them. Zero
	// removes them as soon as no running backup needs them.
	Grace time.Duration `yaml:"grace"`
	// SweepInterval is the pause between sweeper passes. Zero or negative
	// disables the schedule (the queue then only grows).
	SweepInterval time.Duration `yaml:"sweepInterval"`
	// SweepBatch is how many queued deletions a sweeper claims at a time.
	SweepBatch int `yaml:"sweepBatch"`
}

// AIConfig contains AI analysis configuration.
type AIConfig struct {
	Enabled      bool   `yaml:"enabled"`
	AutoAnalyze  bool   `yaml:"autoAnalyze"` // analyse every upload without being asked (default off)
	Provider     string `yaml:"provider"`    // "openai", "azure", "ollama", "custom"
	BaseURL      string `yaml:"baseUrl"`     // API base URL (for Azure/Ollama/custom)
	APIKey       string `yaml:"apiKey"`
	Model        string `yaml:"model"` // e.g. "gpt-4o-mini", "llama3"
	MaxTokens    int    `yaml:"maxTokens"`
	TimeoutSecs  int    `yaml:"timeoutSecs"`
	SystemPrompt string `yaml:"systemPrompt"` // override default system prompt
}

// MCPConfig contains MCP server configuration.
type MCPConfig struct {
	Enabled   bool   `yaml:"enabled"`
	Transport string `yaml:"transport"` // only "http" is supported
	HTTPPath  string `yaml:"httpPath"`  // mount path of the HTTP endpoint, default "/mcp"
	// AllowedOrigins are the browser origins (scheme://host[:port]) accepted in
	// the Origin header of MCP requests, besides the BASE_URL origin. A request
	// with any other Origin is refused with 403; requests without an Origin
	// header (non-browser clients) are unaffected.
	AllowedOrigins []string `yaml:"allowedOrigins"`
	// MaxBodyBytes caps one MCP request body (default 1 MiB).
	MaxBodyBytes int64 `yaml:"maxBodyBytes"`
	// RateLimitPerMinute caps MCP requests per authenticated principal per
	// minute (default 600; 0 disables the limit).
	RateLimitPerMinute int `yaml:"rateLimitPerMinute"`
	// AITriggerPerMinute caps trigger_ai_analysis calls per principal per
	// minute, since each one can cost an LLM call (default 10; 0 disables).
	AITriggerPerMinute int `yaml:"aiTriggerPerMinute"`
}

// Load loads configuration from YAML file (if provided) and environment variables.
// Precedence: defaults < YAML file < environment variables.
func Load(filePath string) (*Config, error) {
	// 1. Start with defaults
	cfg := defaultConfig()

	// 2. Load YAML file if provided
	if filePath != "" {
		if err := loadFromFile(filePath, cfg); err != nil {
			return nil, err
		}
	}

	// 3. Apply environment variable overrides
	applyEnvOverrides(cfg)

	// 4. Finalize config (handle special cases)
	finalizeConfig(cfg)

	return cfg, nil
}

// defaultConfig returns a Config with all default values.
func defaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Host:              "0.0.0.0",
			Port:              8080,
			ReadTimeout:       0,
			WriteTimeout:      0,
			ReadHeaderTimeout: 20,
			MaxBodyBytes:      16 << 30,
			ShutdownTimeout:   30,
			TLSEnabled:        false,
			TLSCert:           "",
			TLSKey:            "",
			APIKey:            "",
			CORSOrigins:       []string{},
			InstanceID:        "",
			IPAllowlist:       []string{},
			RateLimitRPS:      0,
			TrustedProxies:    []string{},
		},
		Database: database.DatabaseConfig{
			Driver:                 "postgres",
			Host:                   "localhost",
			Port:                   5432,
			User:                   "postgres",
			Password:               "",
			Database:               "artifact_repo",
			SSLMode:                "require",
			Options:                make(map[string]string),
			MaxOpenConns:           25,
			MaxIdleConns:           5,
			ConnMaxLifetimeMinutes: 30,
			AuthCacheTTLMillis:     2000,
			ConnectTimeout:         60 * time.Second,
		},
		Storage: storage.StorageConfig{
			Type:                     "filesystem",
			Endpoint:                 "",
			Bucket:                   "artifacts",
			Region:                   "us-east-1",
			AccessKey:                "",
			SecretKey:                "",
			Path:                     "/var/lib/artifacts",
			Options:                  make(map[string]string),
			ForcePathStyle:           false,
			PresignTTL:               5 * time.Minute,
			PresignUploadTTL:         30 * time.Minute,
			PresignMaxPendingUploads: 20,
			PresignMinSize:           1 << 20,
			GCSBucket:                "",
			GCSHMACKey:               "",
			GCSHMACSecret:            "",
			AzureAccountName:         "",
			AzureAccountKey:          "",
			AzureContainerName:       "",
			AzureEndpoint:            "",
			StagingPath:              "",
		},
		Telemetry: telemetry.DefaultConfig(),
		Logger: LoggerConfig{
			Level:      "info",
			Format:     "json",
			OutputPath: "stdout",
		},
		GPG: GPGConfig{
			Enabled:      false,
			KeyPath:      "",
			Passphrase:   "",
			AutoGenerate: false,
		},
		Auth: AuthConfig{
			AllowAnonymousRead:   false,
			LegacyAPIKeyEnabled:  false,
			SessionDurationHours: 24,
			HMACKey:              "",
			SAMLSPCertPEM:        "",
			SAMLSPKeyPEM:         "",
		},
		Backup: BackupConfig{
			BackupDir: "/var/lib/openartifact/backups",
			LeaseTTL:  5 * time.Minute,
		},
		BlobDeletion: BlobDeletionConfig{
			Grace:         24 * time.Hour,
			SweepInterval: time.Minute,
			SweepBatch:    200,
		},
		AI: AIConfig{
			Enabled:      false,
			Provider:     "openai",
			BaseURL:      "",
			APIKey:       "",
			Model:        "gpt-4o-mini",
			MaxTokens:    1024,
			TimeoutSecs:  30,
			SystemPrompt: "",
		},
		MCP: MCPConfig{
			Enabled:   false,
			Transport: "http",
			HTTPPath:  "/mcp",

			AllowedOrigins:     []string{},
			MaxBodyBytes:       1 << 20,
			RateLimitPerMinute: 600,
			AITriggerPerMinute: 10,
		},
		Workers: WorkersConfig{
			MetadataRegenConcurrency:     4,
			DockerGCInterval:             time.Hour,
			DockerGCGracePeriod:          24 * time.Hour,
			ProxyDockerDigestTTL:         30 * 24 * time.Hour,
			DockerUploadTTL:              24 * time.Hour,
			WebhookDeliveryRetentionDays: 30,
		},
		StorageReplica: defaultReplicaConfig(),
		Trash: TrashConfig{
			RetentionDays: 7,
			PurgeInterval: 15 * time.Minute,
		},
		Tiering: TieringConfig{
			Enabled:             true,
			Interval:            5 * time.Minute,
			Batch:               200,
			Concurrency:         2,
			MaxBytesPerSecond:   64 << 20,
			VerifyReadback:      true,
			AccessFlushInterval: 30 * time.Second,
			RefreshInterval:     30 * time.Second,
		},
		Recovery: RecoveryConfig{Journal: RecoveryJournalConfig{
			Enabled:           true,
			Interval:          10 * time.Second,
			Batch:             200,
			Concurrency:       4,
			ReconcileInterval: 24 * time.Hour,
		}},
		Scanners: ScannersConfig{
			UpdateInterval:  6 * time.Hour,
			DownloadBaseURL: DefaultScannerDownloadBaseURL,
			Trivy:           TrivyScanner{Install: InstallNone},
			Grype:           GrypeScanner{Install: InstallNone},
		},
		BaseURL: "http://localhost:8080",
	}
}

// loadFromFile loads configuration from a YAML file.
func loadFromFile(path string, cfg *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config file: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse config file: %w", err)
	}

	return loadReplicaFromYAML(data, cfg)
}

// applyEnvOverrides applies environment variable overrides to the config.
func applyEnvOverrides(cfg *Config) {
	// Server config
	overrideString(&cfg.Server.Host, "SERVER_HOST")
	overrideInt(&cfg.Server.Port, "SERVER_PORT")
	overrideInt(&cfg.Server.ReadTimeout, "SERVER_READ_TIMEOUT")
	overrideInt(&cfg.Server.WriteTimeout, "SERVER_WRITE_TIMEOUT")
	overrideInt(&cfg.Server.ReadHeaderTimeout, "SERVER_READ_HEADER_TIMEOUT")
	overrideInt64(&cfg.Server.MaxBodyBytes, "SERVER_MAX_BODY_BYTES")
	overrideInt(&cfg.Server.ShutdownTimeout, "SERVER_SHUTDOWN_TIMEOUT")
	overrideBool(&cfg.Server.TLSEnabled, "SERVER_TLS_ENABLED")
	overrideString(&cfg.Server.TLSCert, "SERVER_TLS_CERT")
	overrideString(&cfg.Server.TLSKey, "SERVER_TLS_KEY")
	overrideString(&cfg.Server.APIKey, "API_KEY")
	overrideSlice(&cfg.Server.CORSOrigins, "CORS_ORIGINS")
	overrideString(&cfg.Server.InstanceID, "OPENARTIFACT_INSTANCE_ID")
	overrideSlice(&cfg.Server.IPAllowlist, "OPENARTIFACT_IP_ALLOWLIST")
	overrideInt(&cfg.Server.RateLimitRPS, "OPENARTIFACT_RATE_LIMIT_RPS")
	overrideSlice(&cfg.Server.TrustedProxies, "OPENARTIFACT_TRUSTED_PROXIES")
	overrideBool(&cfg.Server.PprofEnabled, "OPENARTIFACT_PPROF_ENABLED")

	// Database config
	overrideString(&cfg.Database.Driver, "DB_DRIVER")
	overrideString(&cfg.Database.Host, "DB_HOST")
	overrideInt(&cfg.Database.Port, "DB_PORT")
	overrideString(&cfg.Database.User, "DB_USER")
	overrideString(&cfg.Database.Password, "DB_PASSWORD")
	overrideString(&cfg.Database.Database, "DB_NAME")
	overrideString(&cfg.Database.SSLMode, "DB_SSLMODE")
	overrideInt(&cfg.Database.MaxOpenConns, "DB_MAX_OPEN_CONNS")
	overrideInt(&cfg.Database.MaxIdleConns, "DB_MAX_IDLE_CONNS")
	overrideInt(&cfg.Database.ConnMaxLifetimeMinutes, "DB_CONN_MAX_LIFETIME_MINUTES")
	overrideDuration(&cfg.Database.ConnectTimeout, "DB_CONNECT_TIMEOUT")
	overrideInt(&cfg.Database.AuthCacheTTLMillis, "DB_AUTH_CACHE_TTL_MS")
	overrideKeyValues(&cfg.Database.Options, "DB_OPTIONS")

	// Storage config
	if v, ok := os.LookupEnv("STORAGE_BACKEND"); ok {
		cfg.Storage.Type = v
	} else if v, ok := os.LookupEnv("STORAGE_TYPE"); ok {
		cfg.Storage.Type = v
	}

	overrideString(&cfg.Storage.Endpoint, "STORAGE_ENDPOINT")

	overrideString(&cfg.Storage.Bucket, "STORAGE_BUCKET")
	overrideString(&cfg.Storage.Region, "STORAGE_REGION")
	overrideString(&cfg.Storage.AccessKey, "STORAGE_ACCESS_KEY")
	overrideString(&cfg.Storage.SecretKey, "STORAGE_SECRET_KEY")
	overrideString(&cfg.Storage.Path, "STORAGE_PATH")
	overrideKeyValues(&cfg.Storage.Options, "STORAGE_OPTIONS")
	overrideBool(&cfg.Storage.ForcePathStyle, "STORAGE_S3_FORCE_PATH_STYLE")
	overrideDuration(&cfg.Storage.PresignTTL, "STORAGE_S3_PRESIGN_TTL")
	overrideDuration(&cfg.Storage.PresignUploadTTL, "STORAGE_PRESIGN_UPLOAD_TTL")
	overrideInt(&cfg.Storage.PresignMaxPendingUploads, "STORAGE_PRESIGN_MAX_PENDING_UPLOADS")
	overrideBool(&cfg.Storage.PresignDownloads, "STORAGE_PRESIGN_DOWNLOADS")
	overrideInt64(&cfg.Storage.PresignMinSize, "STORAGE_PRESIGN_MIN_SIZE")
	overrideString(&cfg.Storage.PresignEndpoint, "STORAGE_PRESIGN_ENDPOINT")
	overrideString(&cfg.Storage.GCSBucket, "OPENARTIFACT_GCS_BUCKET")
	overrideString(&cfg.Storage.GCSHMACKey, "OPENARTIFACT_GCS_HMAC_KEY")
	overrideString(&cfg.Storage.GCSHMACSecret, "OPENARTIFACT_GCS_HMAC_SECRET")
	overrideString(&cfg.Storage.AzureAccountName, "OPENARTIFACT_AZURE_ACCOUNT_NAME")
	overrideString(&cfg.Storage.AzureAccountKey, "OPENARTIFACT_AZURE_ACCOUNT_KEY")
	overrideString(&cfg.Storage.AzureContainerName, "OPENARTIFACT_AZURE_CONTAINER_NAME")
	overrideString(&cfg.Storage.AzureEndpoint, "OPENARTIFACT_AZURE_ENDPOINT")
	overrideString(&cfg.Storage.StagingPath, "STORAGE_STAGING_PATH")
	overrideString(&cfg.Storage.OCIUsername, "STORAGE_OCI_USERNAME")
	overrideString(&cfg.Storage.OCIPassword, "STORAGE_OCI_PASSWORD")
	overrideBool(&cfg.Storage.OCIInsecure, "STORAGE_OCI_INSECURE")

	// Logger config
	overrideString(&cfg.Logger.Level, "LOG_LEVEL")
	overrideString(&cfg.Logger.Format, "LOG_FORMAT")
	overrideString(&cfg.Logger.OutputPath, "LOG_OUTPUT")

	applyTelemetryEnv(&cfg.Telemetry.OTLP)

	// GPG config
	overrideBool(&cfg.GPG.Enabled, "GPG_ENABLED")
	overrideString(&cfg.GPG.KeyPath, "GPG_KEY_PATH")
	overrideString(&cfg.GPG.Passphrase, "GPG_PASSPHRASE")
	overrideBool(&cfg.GPG.AutoGenerate, "GPG_AUTO_GENERATE")

	// Attestation signing key
	overrideString(&cfg.Attest.KeyPath, "ATTESTATION_KEY_PATH")

	// Auth config
	overrideBool(&cfg.Auth.AllowAnonymousRead, "ALLOW_ANONYMOUS_READ")
	overrideBool(&cfg.Auth.LegacyAPIKeyEnabled, "LEGACY_API_KEY_ENABLED")
	overrideInt(&cfg.Auth.SessionDurationHours, "SESSION_DURATION_HOURS")
	overrideString(&cfg.Auth.HMACKey, "AUTH_HMAC_KEY")
	overrideString(&cfg.Auth.SAMLSPCertPEM, "OPENARTIFACT_SAML_SP_CERT")
	overrideString(&cfg.Auth.SAMLSPKeyPEM, "OPENARTIFACT_SAML_SP_KEY")

	// Backup config
	overrideString(&cfg.Backup.BackupDir, "OPENARTIFACT_BACKUP_DIR")
	overrideDuration(&cfg.Backup.LeaseTTL, "OPENARTIFACT_BACKUP_LEASE_TTL")

	// Deferred blob deletion
	overrideDuration(&cfg.BlobDeletion.Grace, "OPENARTIFACT_BLOB_DELETION_GRACE")
	overrideDuration(&cfg.BlobDeletion.SweepInterval, "OPENARTIFACT_BLOB_DELETION_SWEEP_INTERVAL")
	overrideInt(&cfg.BlobDeletion.SweepBatch, "OPENARTIFACT_BLOB_DELETION_SWEEP_BATCH")

	// AI config
	overrideBool(&cfg.AI.Enabled, "AI_ENABLED")
	overrideBool(&cfg.AI.AutoAnalyze, "AI_AUTO_ANALYZE")
	overrideString(&cfg.AI.Provider, "AI_PROVIDER")
	overrideString(&cfg.AI.BaseURL, "AI_BASE_URL")
	overrideString(&cfg.AI.APIKey, "AI_API_KEY")
	overrideString(&cfg.AI.Model, "AI_MODEL")
	overrideInt(&cfg.AI.MaxTokens, "AI_MAX_TOKENS")
	overrideInt(&cfg.AI.TimeoutSecs, "AI_TIMEOUT_SECS")
	overrideString(&cfg.AI.SystemPrompt, "AI_SYSTEM_PROMPT")

	// Projects config
	overrideBool(&cfg.Projects.SelfCreate, "OPENARTIFACT_PROJECTS_SELF_CREATE")

	// MCP config
	overrideBool(&cfg.MCP.Enabled, "MCP_ENABLED")
	overrideString(&cfg.MCP.Transport, "MCP_TRANSPORT")
	overrideString(&cfg.MCP.HTTPPath, "MCP_HTTP_PATH")
	overrideSlice(&cfg.MCP.AllowedOrigins, "MCP_ALLOWED_ORIGINS")
	overrideInt64(&cfg.MCP.MaxBodyBytes, "MCP_MAX_BODY_BYTES")
	overrideInt(&cfg.MCP.RateLimitPerMinute, "MCP_RATE_LIMIT_PER_MINUTE")
	overrideInt(&cfg.MCP.AITriggerPerMinute, "MCP_AI_TRIGGER_PER_MINUTE")

	// Worker config
	overrideInt(&cfg.Workers.MetadataRegenConcurrency, "OPENARTIFACT_METADATA_REGEN_CONCURRENCY")
	overrideDuration(&cfg.Workers.DockerGCInterval, "OPENARTIFACT_DOCKER_GC_INTERVAL")
	overrideDuration(&cfg.Workers.DockerGCGracePeriod, "OPENARTIFACT_DOCKER_GC_GRACE_PERIOD")
	overrideDuration(&cfg.Workers.ProxyDockerDigestTTL, "OPENARTIFACT_PROXY_DOCKER_DIGEST_TTL")
	overrideDuration(&cfg.Workers.DockerUploadTTL, "OPENARTIFACT_DOCKER_UPLOAD_TTL")
	overrideString(&cfg.Workers.DockerUploadDir, "OPENARTIFACT_DOCKER_UPLOAD_DIR")
	overrideInt(&cfg.Workers.WebhookDeliveryRetentionDays, "OPENARTIFACT_WEBHOOK_DELIVERY_RETENTION_DAYS")
	overrideInt(&cfg.Trash.RetentionDays, "OPENARTIFACT_TRASH_RETENTION")
	overrideDuration(&cfg.Trash.PurgeInterval, "OPENARTIFACT_TRASH_PURGE_INTERVAL")

	// Storage replica
	applyReplicaEnvOverrides(cfg)

	// Storage tiering
	overrideBool(&cfg.Tiering.Enabled, "OPENARTIFACT_TIERING_ENABLED")
	overrideDuration(&cfg.Tiering.Interval, "OPENARTIFACT_TIERING_INTERVAL")
	overrideInt(&cfg.Tiering.Batch, "OPENARTIFACT_TIERING_BATCH")
	overrideInt(&cfg.Tiering.Concurrency, "OPENARTIFACT_TIERING_CONCURRENCY")
	overrideInt64(&cfg.Tiering.MaxBytesPerSecond, "OPENARTIFACT_TIERING_MAX_BYTES_PER_SECOND")
	overrideBool(&cfg.Tiering.VerifyReadback, "OPENARTIFACT_TIERING_VERIFY_READBACK")
	overrideDuration(&cfg.Tiering.AccessFlushInterval, "OPENARTIFACT_TIERING_ACCESS_FLUSH_INTERVAL")
	overrideDuration(&cfg.Tiering.RefreshInterval, "OPENARTIFACT_TIERING_REFRESH_INTERVAL")

	// Disaster-recovery row journal
	overrideBool(&cfg.Recovery.Journal.Enabled, "OPENARTIFACT_RECOVERY_JOURNAL_ENABLED")
	overrideDuration(&cfg.Recovery.Journal.Interval, "OPENARTIFACT_RECOVERY_JOURNAL_INTERVAL")
	overrideInt(&cfg.Recovery.Journal.Batch, "OPENARTIFACT_RECOVERY_JOURNAL_BATCH")
	overrideInt(&cfg.Recovery.Journal.Concurrency, "OPENARTIFACT_RECOVERY_JOURNAL_CONCURRENCY")
	overrideDuration(&cfg.Recovery.Journal.ReconcileInterval, "OPENARTIFACT_RECOVERY_JOURNAL_RECONCILE_INTERVAL")

	// Vulnerability scanners
	overrideString(&cfg.Scanners.CacheDir, "OPENARTIFACT_SCANNER_CACHE_DIR")
	overrideDuration(&cfg.Scanners.UpdateInterval, "OPENARTIFACT_SCANNER_UPDATE_INTERVAL")
	overrideBool(&cfg.Scanners.Offline, "OPENARTIFACT_SCANNER_OFFLINE")
	overrideString(&cfg.Scanners.Trivy.Path, "OPENARTIFACT_TRIVY_PATH")
	overrideString(&cfg.Scanners.Trivy.DBRepository, "OPENARTIFACT_TRIVY_DB_REPOSITORY")
	overrideString(&cfg.Scanners.Trivy.JavaDBRepository, "OPENARTIFACT_TRIVY_JAVA_DB_REPOSITORY")
	overrideString(&cfg.Scanners.Grype.Path, "OPENARTIFACT_GRYPE_PATH")
	overrideString(&cfg.Scanners.Grype.DBURL, "OPENARTIFACT_GRYPE_DB_URL")
	overrideString(&cfg.Scanners.DownloadBaseURL, "OPENARTIFACT_SCANNER_DOWNLOAD_BASE_URL")
	overrideString(&cfg.Scanners.Trivy.Install, "OPENARTIFACT_TRIVY_INSTALL")
	overrideString(&cfg.Scanners.Grype.Install, "OPENARTIFACT_GRYPE_INSTALL")

	// Base URL
	overrideString(&cfg.BaseURL, "BASE_URL")
}

// finalizeConfig handles special configuration cases.
func finalizeConfig(cfg *Config) {
	// Handle BASE_URL default
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://localhost:8080"

		fmt.Fprintln(os.Stderr, "WARN: BASE_URL not set, defaulting to localhost — SSO callbacks will not work behind a proxy")
	}

	// Server-relayed presigned uploads (filesystem and oci storage) point
	// clients at this server.
	cfg.Storage.PublicURL = cfg.BaseURL
}

// ValidateFile validates a config file without loading it into the application.
func ValidateFile(path string) error {
	cfg, err := Load(path)
	if err != nil {
		return err
	}

	return cfg.Validate()
}

// Validate validates the configuration.
func (c *Config) Validate() error {
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("%w: %d", errInvalidPort, c.Server.Port)
	}

	if c.Database.Driver == "" {
		return errMissingDBDriver
	}

	if c.Storage.Type == "" {
		return errMissingStorageType
	}

	if c.Logger.Level == "" {
		return errMissingLogLevel
	}

	if c.Server.TLSEnabled && (c.Server.TLSCert == "" || c.Server.TLSKey == "") {
		return errTLSFilesRequired
	}

	if err := c.validateMCP(); err != nil {
		return err
	}

	if c.Workers.WebhookDeliveryRetentionDays < 0 {
		return errNegativeWebhookRetention
	}

	if c.Workers.DockerGCGracePeriod < 0 {
		return errNegativeDockerGCGrace
	}

	if c.Workers.ProxyDockerDigestTTL < 0 {
		return errNegativeDigestTTL
	}

	if c.Trash.RetentionDays < 0 {
		return errNegativeTrashRetention
	}

	if err := c.validateBackgroundServices(); err != nil {
		return err
	}

	if c.Database.MaxOpenConns < 0 || c.Database.MaxOpenConns == 1 {
		return errMaxOpenConnsTooSmall
	}

	if c.Database.Password == placeholderPassword {
		return errPlaceholderDBPassword
	}

	if _, err := netutil.ParseNetworks(c.Server.IPAllowlist); err != nil {
		return fmt.Errorf("server.ipAllowlist: %w", err)
	}

	return c.validateFeatures()
}

// validateFeatures checks the scanner and presign settings.
func (c *Config) validateFeatures() error {
	if err := c.Scanners.validate(); err != nil {
		return err
	}

	if err := c.validateReplica(); err != nil {
		return err
	}

	return c.validatePresign()
}

// minTieringOpenConns is the smallest explicit database pool a tier mover can
// share with the server: one pinned connection for the pass lock, plus a
// blob-lock transaction and the query its callback runs.
const minTieringOpenConns = 4

// validateTiering checks the storage tiering settings.
func (c *Config) validateTiering() error {
	t := c.Tiering

	if t.Batch < 1 || t.Concurrency < 1 {
		return errTieringBatch
	}

	if t.MaxBytesPerSecond < 0 || t.AccessFlushInterval < 0 || t.RefreshInterval < 0 {
		return errTieringNegative
	}

	// A pass holds one pooled connection for up to two hours (the cluster
	// advisory lock) that database.maxOpenConns does not account for.
	if t.Enabled && c.Database.MaxOpenConns > 0 && c.Database.MaxOpenConns < minTieringOpenConns {
		return errTieringPoolSize
	}

	return nil
}

// validateRecovery checks the row journal settings.
func (c *Config) validateRecovery() error {
	j := c.Recovery.Journal

	if j.Batch < 1 || j.Concurrency < 1 {
		return errRecoveryBatch
	}

	if j.ReconcileInterval < 0 || j.Interval < 0 || (j.Enabled && j.Interval == 0) {
		return errRecoveryNegative
	}

	return nil
}

// validateBlobRetention checks the deferred blob deletion and backup lease settings.
// validateBackgroundServices checks the optional subsystems (telemetry,
// storage tiering, deferred blob deletion).
func (c *Config) validateBackgroundServices() error {
	if err := c.Telemetry.Validate(); err != nil {
		return err
	}

	if err := c.validateTiering(); err != nil {
		return err
	}

	if err := c.validateRecovery(); err != nil {
		return err
	}

	return c.validateBlobRetention()
}

func (c *Config) validateBlobRetention() error {
	if c.BlobDeletion.Grace < 0 {
		return errBlobDeletionNegative
	}

	if c.BlobDeletion.SweepBatch < 1 {
		return errBlobDeletionBatch
	}

	if c.Backup.LeaseTTL <= 0 {
		return errBackupLeaseTTL
	}

	return nil
}

// validatePresign checks the presigned-URL settings.
func (c *Config) validatePresign() error {
	if c.Storage.PresignMinSize < 0 {
		return errPresignMinSize
	}

	if c.Storage.PresignMaxPendingUploads < 0 {
		return errPresignMaxPending
	}

	if ep := c.Storage.PresignEndpoint; ep != "" {
		u, err := url.Parse(ep)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
			return errPresignEndpoint
		}
	}

	return nil
}

// Override helper functions — only override if env var is set.

// warnBadEnv reports an unparseable env override without echoing the value
// (it may be a secret) and keeps the previous setting.
func warnBadEnv(key, _ string, err error) {
	fmt.Fprintf(os.Stderr, "WARN: ignoring invalid value for %s: %v\n", key, err)
}

func overrideString(cfg *string, key string) {
	if v, ok := os.LookupEnv(key); ok {
		*cfg = v
	}
}

func overrideInt(cfg *int, key string) {
	if v, ok := os.LookupEnv(key); ok {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			warnBadEnv(key, v, err)
			return
		}
		*cfg = n
	}
}

func overrideInt64(cfg *int64, key string) {
	if v, ok := os.LookupEnv(key); ok {
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			warnBadEnv(key, v, err)
			return
		}
		*cfg = n
	}
}

func overrideBool(cfg *bool, key string) {
	if v, ok := os.LookupEnv(key); ok {
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			warnBadEnv(key, v, err)
			return
		}
		*cfg = b
	}
}

func overrideSlice(cfg *[]string, key string) {
	if v, ok := os.LookupEnv(key); ok {
		parts := strings.Split(v, ",")
		result := make([]string, 0, len(parts))

		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				result = append(result, p)
			}
		}

		if len(result) > 0 {
			*cfg = result
		}
	}
}

// overrideKeyValues merges comma-separated key=value pairs from the
// environment into dst, replacing values of keys already set (for example by
// YAML). Entries without a key are ignored with a warning.
func overrideKeyValues(dst *map[string]string, key string) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return
	}

	if *dst == nil {
		*dst = make(map[string]string)
	}

	for pair := range strings.SplitSeq(v, ",") {
		if pair = strings.TrimSpace(pair); pair == "" {
			continue
		}

		k, val, found := strings.Cut(pair, "=")
		if k = strings.TrimSpace(k); !found || k == "" {
			warnBadEnv(key, pair, errMalformedKeyValue)

			continue
		}

		(*dst)[k] = strings.TrimSpace(val)
	}
}

func overrideDuration(cfg *time.Duration, key string) {
	if v, ok := os.LookupEnv(key); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			warnBadEnv(key, v, err)
			return
		}
		*cfg = d
	}
}

// validateMCP checks the MCP transport, limits and Origin allowlist entries
// when MCP is enabled.
func (c *Config) validateMCP() error {
	if !c.MCP.Enabled {
		return nil
	}

	if c.MCP.Transport != "http" {
		return errMCPTransport
	}

	if c.MCP.MaxBodyBytes <= 0 || c.MCP.RateLimitPerMinute < 0 || c.MCP.AITriggerPerMinute < 0 {
		return errMCPLimits
	}

	for _, o := range c.MCP.AllowedOrigins {
		u, err := url.Parse(o)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("%w: %q", errMCPOrigin, o)
		}
	}

	return nil
}
