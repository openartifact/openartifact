// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/storage"
	"go.yaml.in/yaml/v3"
)

var (
	errInvalidPort        = errors.New("invalid server port")
	errMCPTransport       = errors.New(`mcp.transport must be "http" (stdio is not supported)`)
	errMissingDBDriver    = errors.New("database driver is required")
	errMissingStorageType = errors.New("storage type is required")
	errMissingLogLevel    = errors.New("logger level is required")
	errTLSFilesRequired   = errors.New("server TLS is enabled but tlsCert/tlsKey are not set")

	errNegativeDockerGCGrace = errors.New("workers.dockerGcGracePeriod must not be negative")
)

type Config struct {
	Server   ServerConfig            `yaml:"server"`
	Database database.DatabaseConfig `yaml:"database"`
	Storage  storage.StorageConfig   `yaml:"storage"`
	Logger   LoggerConfig            `yaml:"log"`
	GPG      GPGConfig               `yaml:"gpg"`
	Auth     AuthConfig              `yaml:"auth"`
	Backup   BackupConfig            `yaml:"backup"`
	AI       AIConfig                `yaml:"ai"`
	MCP      MCPConfig               `yaml:"mcp"`
	Workers  WorkersConfig           `yaml:"workers"`
	BaseURL  string                  `yaml:"baseUrl"`
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
	ReadHeaderTimeout int      `yaml:"readHeaderTimeout"` // seconds
	ShutdownTimeout   int      `yaml:"shutdownTimeout"`   // seconds
	TLSEnabled        bool     `yaml:"tlsEnabled"`
	TLSCert           string   `yaml:"tlsCert"`
	TLSKey            string   `yaml:"tlsKey"`
	APIKey            string   `yaml:"apiKey"`       // API key for write endpoint authentication (empty = auth disabled)
	CORSOrigins       []string `yaml:"corsOrigins"`  // Allowed CORS origins (default: none)
	InstanceID        string   `yaml:"instanceId"`   // Instance identifier for HA deployments
	IPAllowlist       []string `yaml:"ipAllowlist"`  // Allowed CIDR blocks (empty = allow all)
	RateLimitRPS      int      `yaml:"rateLimitRps"` // Rate limit in requests per second (0 = disabled)
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

// BackupConfig contains backup configuration.
type BackupConfig struct {
	BackupDir string `yaml:"backupDir"` // Directory to store backup files
}

// AIConfig contains AI analysis configuration.
type AIConfig struct {
	Enabled      bool   `yaml:"enabled"`
	Provider     string `yaml:"provider"` // "openai", "azure", "vertex", "ollama"
	BaseURL      string `yaml:"baseUrl"`  // API base URL (for Azure/Ollama/custom)
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
	if err := finalizeConfig(cfg); err != nil {
		return nil, err
	}

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
		},
		Storage: storage.StorageConfig{
			Type:               "filesystem",
			Endpoint:           "",
			Bucket:             "artifacts",
			Region:             "us-east-1",
			AccessKey:          "",
			SecretKey:          "",
			Path:               "/var/lib/artifacts",
			Options:            make(map[string]string),
			ForcePathStyle:     false,
			PresignTTL:         5 * time.Minute,
			GCSBucket:          "",
			GCSHMACKey:         "",
			GCSHMACSecret:      "",
			AzureAccountName:   "",
			AzureAccountKey:    "",
			AzureContainerName: "",
		},
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
			LegacyAPIKeyEnabled:  true,
			SessionDurationHours: 24,
			HMACKey:              "",
			SAMLSPCertPEM:        "",
			SAMLSPKeyPEM:         "",
		},
		Backup: BackupConfig{
			BackupDir: "/var/lib/openartifact/backups",
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
		},
		Workers: WorkersConfig{
			MetadataRegenConcurrency: 4,
			DockerGCInterval:         time.Hour,
			DockerGCGracePeriod:      24 * time.Hour,
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

	return nil
}

// applyEnvOverrides applies environment variable overrides to the config.
func applyEnvOverrides(cfg *Config) {
	// Server config
	overrideString(&cfg.Server.Host, "SERVER_HOST")
	overrideInt(&cfg.Server.Port, "SERVER_PORT")
	overrideInt(&cfg.Server.ReadTimeout, "SERVER_READ_TIMEOUT")
	overrideInt(&cfg.Server.WriteTimeout, "SERVER_WRITE_TIMEOUT")
	overrideInt(&cfg.Server.ReadHeaderTimeout, "SERVER_READ_HEADER_TIMEOUT")
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
	overrideInt(&cfg.Database.AuthCacheTTLMillis, "DB_AUTH_CACHE_TTL_MS")

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
	overrideBool(&cfg.Storage.ForcePathStyle, "STORAGE_S3_FORCE_PATH_STYLE")
	overrideDuration(&cfg.Storage.PresignTTL, "STORAGE_S3_PRESIGN_TTL")
	overrideString(&cfg.Storage.GCSBucket, "OPENARTIFACT_GCS_BUCKET")
	overrideString(&cfg.Storage.GCSHMACKey, "OPENARTIFACT_GCS_HMAC_KEY")
	overrideString(&cfg.Storage.GCSHMACSecret, "OPENARTIFACT_GCS_HMAC_SECRET")
	overrideString(&cfg.Storage.AzureAccountName, "OPENARTIFACT_AZURE_ACCOUNT_NAME")
	overrideString(&cfg.Storage.AzureAccountKey, "OPENARTIFACT_AZURE_ACCOUNT_KEY")
	overrideString(&cfg.Storage.AzureContainerName, "OPENARTIFACT_AZURE_CONTAINER_NAME")
	overrideString(&cfg.Storage.OCIUsername, "STORAGE_OCI_USERNAME")
	overrideString(&cfg.Storage.OCIPassword, "STORAGE_OCI_PASSWORD")
	overrideBool(&cfg.Storage.OCIInsecure, "STORAGE_OCI_INSECURE")

	// Logger config
	overrideString(&cfg.Logger.Level, "LOG_LEVEL")
	overrideString(&cfg.Logger.Format, "LOG_FORMAT")
	overrideString(&cfg.Logger.OutputPath, "LOG_OUTPUT")

	// GPG config
	overrideBool(&cfg.GPG.Enabled, "GPG_ENABLED")
	overrideString(&cfg.GPG.KeyPath, "GPG_KEY_PATH")
	overrideString(&cfg.GPG.Passphrase, "GPG_PASSPHRASE")
	overrideBool(&cfg.GPG.AutoGenerate, "GPG_AUTO_GENERATE")

	// Auth config
	overrideBool(&cfg.Auth.AllowAnonymousRead, "ALLOW_ANONYMOUS_READ")
	overrideBool(&cfg.Auth.LegacyAPIKeyEnabled, "LEGACY_API_KEY_ENABLED")
	overrideInt(&cfg.Auth.SessionDurationHours, "SESSION_DURATION_HOURS")
	overrideString(&cfg.Auth.HMACKey, "AUTH_HMAC_KEY")
	overrideString(&cfg.Auth.SAMLSPCertPEM, "OPENARTIFACT_SAML_SP_CERT")
	overrideString(&cfg.Auth.SAMLSPKeyPEM, "OPENARTIFACT_SAML_SP_KEY")

	// Backup config
	overrideString(&cfg.Backup.BackupDir, "OPENARTIFACT_BACKUP_DIR")

	// AI config
	overrideBool(&cfg.AI.Enabled, "AI_ENABLED")
	overrideString(&cfg.AI.Provider, "AI_PROVIDER")
	overrideString(&cfg.AI.BaseURL, "AI_BASE_URL")
	overrideString(&cfg.AI.APIKey, "AI_API_KEY")
	overrideString(&cfg.AI.Model, "AI_MODEL")
	overrideInt(&cfg.AI.MaxTokens, "AI_MAX_TOKENS")
	overrideInt(&cfg.AI.TimeoutSecs, "AI_TIMEOUT_SECS")
	overrideString(&cfg.AI.SystemPrompt, "AI_SYSTEM_PROMPT")

	// MCP config
	overrideBool(&cfg.MCP.Enabled, "MCP_ENABLED")
	overrideString(&cfg.MCP.Transport, "MCP_TRANSPORT")
	overrideString(&cfg.MCP.HTTPPath, "MCP_HTTP_PATH")

	// Worker config
	overrideInt(&cfg.Workers.MetadataRegenConcurrency, "OPENARTIFACT_METADATA_REGEN_CONCURRENCY")
	overrideDuration(&cfg.Workers.DockerGCInterval, "OPENARTIFACT_DOCKER_GC_INTERVAL")
	overrideDuration(&cfg.Workers.DockerGCGracePeriod, "OPENARTIFACT_DOCKER_GC_GRACE_PERIOD")

	// Base URL
	overrideString(&cfg.BaseURL, "BASE_URL")
}

// finalizeConfig handles special configuration cases.
func finalizeConfig(cfg *Config) error {
	// Handle BASE_URL default
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://localhost:8080"

		fmt.Fprintln(os.Stderr, "WARN: BASE_URL not set, defaulting to localhost — SSO callbacks will not work behind a proxy")
	}

	// Handle HMAC key generation
	if cfg.Auth.HMACKey == "" {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return fmt.Errorf("generate ephemeral hmac key: %w", err)
		}

		cfg.Auth.HMACKey = hex.EncodeToString(raw)

		fmt.Fprintln(os.Stderr, "WARN: AUTH_HMAC_KEY not set, generated ephemeral key — SAML RelayState will fail across server restarts")
	}

	return nil
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

	if c.MCP.Enabled && c.MCP.Transport != "http" {
		return errMCPTransport
	}

	if c.Workers.DockerGCGracePeriod < 0 {
		return errNegativeDockerGCGrace
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
