// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLoad_Defaults(t *testing.T) {
	t.Run("default values", func(t *testing.T) {
		for _, k := range []string{
			"SERVER_HOST", "SERVER_PORT", "LOG_LEVEL", "DB_HOST", "DB_PORT",
			"DB_DRIVER", "DB_SSLMODE", "STORAGE_BACKEND", "STORAGE_TYPE",
			"ALLOW_ANONYMOUS_READ", "SESSION_DURATION_HOURS", "CORS_ORIGINS",
		} {
			if v, ok := os.LookupEnv(k); ok {
				_ = os.Unsetenv(k)
				t.Cleanup(func() { _ = os.Setenv(k, v) })
			}
		}

		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		if cfg.Server.Host != "0.0.0.0" {
			t.Errorf("expected host=0.0.0.0, got %s", cfg.Server.Host)
		}

		if cfg.Server.Port != 8080 {
			t.Errorf("expected port=8080, got %d", cfg.Server.Port)
		}

		if cfg.Logger.Level != "info" {
			t.Errorf("expected log level=info, got %s", cfg.Logger.Level)
		}

		if cfg.Database.Host != "localhost" {
			t.Errorf("expected db host=localhost, got %s", cfg.Database.Host)
		}

		if cfg.Database.Port != 5432 {
			t.Errorf("expected db port=5432, got %d", cfg.Database.Port)
		}

		if cfg.Database.Driver != "postgres" {
			t.Errorf("expected db driver=postgres, got %s", cfg.Database.Driver)
		}
		if cfg.Database.SSLMode != "require" {
			t.Errorf("expected db ssl mode=require, got %s", cfg.Database.SSLMode)
		}

		if cfg.Storage.Type != "filesystem" {
			t.Errorf("expected storage type=filesystem, got %s", cfg.Storage.Type)
		}

		if cfg.Auth.AllowAnonymousRead != false {
			t.Errorf("expected AllowAnonymousRead=false, got %v", cfg.Auth.AllowAnonymousRead)
		}

		if cfg.Auth.SessionDurationHours != 24 {
			t.Errorf("expected SessionDurationHours=24, got %d", cfg.Auth.SessionDurationHours)
		}
	})
}

func TestLoad_CustomPort(t *testing.T) {
	t.Setenv("SERVER_PORT", "9090")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if cfg.Server.Port != 9090 {
		t.Errorf("expected port=9090, got %d", cfg.Server.Port)
	}
}

func TestLoad_DatabaseConfig(t *testing.T) {
	t.Run("database config from env", func(t *testing.T) {
		t.Setenv("DB_HOST", "db.example.com")
		t.Setenv("DB_PORT", "3306")
		t.Setenv("DB_NAME", "mydb")
		t.Setenv("DB_USER", "dbuser")
		t.Setenv("DB_PASSWORD", "dbpass123")

		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		if cfg.Database.Host != "db.example.com" {
			t.Errorf("expected DB_HOST=db.example.com, got %s", cfg.Database.Host)
		}

		if cfg.Database.Port != 3306 {
			t.Errorf("expected DB_PORT=3306, got %d", cfg.Database.Port)
		}

		if cfg.Database.Database != "mydb" {
			t.Errorf("expected DB_NAME=mydb, got %s", cfg.Database.Database)
		}

		if cfg.Database.User != "dbuser" {
			t.Errorf("expected DB_USER=dbuser, got %s", cfg.Database.User)
		}

		if cfg.Database.Password != "dbpass123" {
			t.Errorf("expected DB_PASSWORD=dbpass123, got %s", cfg.Database.Password)
		}
	})
}

func TestLoad_StorageConfig(t *testing.T) {
	t.Run("storage config from env", func(t *testing.T) {
		backend, backendSet := os.LookupEnv("STORAGE_BACKEND")
		if err := os.Unsetenv("STORAGE_BACKEND"); err != nil {
			t.Fatalf("clearing STORAGE_BACKEND: %v", err)
		}
		t.Cleanup(func() {
			var err error
			if backendSet {
				err = os.Setenv("STORAGE_BACKEND", backend)
			} else {
				err = os.Unsetenv("STORAGE_BACKEND")
			}
			if err != nil {
				t.Errorf("restoring STORAGE_BACKEND: %v", err)
			}
		})
		t.Setenv("STORAGE_TYPE", "s3")
		t.Setenv("STORAGE_ENDPOINT", "https://s3.amazonaws.com")
		t.Setenv("STORAGE_BUCKET", "my-bucket")
		t.Setenv("STORAGE_REGION", "us-west-2")

		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		if cfg.Storage.Type != "s3" {
			t.Errorf("expected storage type=s3, got %s", cfg.Storage.Type)
		}

		if cfg.Storage.Endpoint != "https://s3.amazonaws.com" {
			t.Errorf("expected endpoint=https://s3.amazonaws.com, got %s", cfg.Storage.Endpoint)
		}

		if cfg.Storage.Bucket != "my-bucket" {
			t.Errorf("expected bucket=my-bucket, got %s", cfg.Storage.Bucket)
		}

		if cfg.Storage.Region != "us-west-2" {
			t.Errorf("expected region=us-west-2, got %s", cfg.Storage.Region)
		}
	})
}

func TestLoad_AuthConfig(t *testing.T) {
	t.Run("auth config from env", func(t *testing.T) {
		t.Setenv("ALLOW_ANONYMOUS_READ", "true")
		t.Setenv("SESSION_DURATION_HOURS", "48")

		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		if cfg.Auth.AllowAnonymousRead != true {
			t.Errorf("expected AllowAnonymousRead=true, got %v", cfg.Auth.AllowAnonymousRead)
		}

		if cfg.Auth.SessionDurationHours != 48 {
			t.Errorf("expected SessionDurationHours=48, got %d", cfg.Auth.SessionDurationHours)
		}
	})
}

func TestLoad_BaseURL(t *testing.T) {
	t.Run("base url from env", func(t *testing.T) {
		t.Setenv("BASE_URL", "https://example.com")

		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		if cfg.BaseURL != "https://example.com" {
			t.Errorf("expected BASE_URL=https://example.com, got %s", cfg.BaseURL)
		}
	})

	t.Run("base url default", func(t *testing.T) {
		// Don't set BASE_URL
		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		if cfg.BaseURL != "http://localhost:8080" {
			t.Errorf("expected default BASE_URL=http://localhost:8080, got %s", cfg.BaseURL)
		}
	})
}

func TestLoad_CORSOrigins(t *testing.T) {
	t.Run("cors origins from env", func(t *testing.T) {
		t.Setenv("CORS_ORIGINS", "http://a.com,http://b.com")

		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		if len(cfg.Server.CORSOrigins) != 2 {
			t.Errorf("expected 2 CORS origins, got %d", len(cfg.Server.CORSOrigins))
		}

		if cfg.Server.CORSOrigins[0] != "http://a.com" {
			t.Errorf("expected first origin=http://a.com, got %s", cfg.Server.CORSOrigins[0])
		}

		if cfg.Server.CORSOrigins[1] != "http://b.com" {
			t.Errorf("expected second origin=http://b.com, got %s", cfg.Server.CORSOrigins[1])
		}
	})

	t.Run("cors origins default", func(t *testing.T) {
		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		if len(cfg.Server.CORSOrigins) != 0 {
			t.Errorf("expected default CORS origins=[], got %v", cfg.Server.CORSOrigins)
		}
	})
}

func TestValidate_Valid(t *testing.T) {
	t.Run("valid config", func(t *testing.T) {
		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		err = cfg.Validate()
		if err != nil {
			t.Errorf("Validate() should not fail for valid config: %v", err)
		}
	})
}

func TestValidate_MaxOpenConns(t *testing.T) {
	for _, tc := range []struct {
		conns int
		ok    bool
	}{{-1, false}, {0, true}, {1, false}, {2, true}, {25, true}} {
		cfg, err := Load("")
		if err != nil {
			t.Fatal(err)
		}

		cfg.Tiering.Enabled = false // the tier mover has its own, larger minimum
		cfg.Database.MaxOpenConns = tc.conns

		if got := cfg.Validate() == nil; got != tc.ok {
			t.Errorf("maxOpenConns=%d: valid=%v, want %v", tc.conns, got, tc.ok)
		}
	}
}

func TestValidate_TieringNeedsRoomInThePool(t *testing.T) {
	for _, tc := range []struct {
		enabled bool
		conns   int
		ok      bool
	}{{true, 0, true}, {true, 2, false}, {true, 3, false}, {true, 4, true}, {true, 25, true}, {false, 2, true}} {
		cfg, err := Load("")
		if err != nil {
			t.Fatal(err)
		}

		cfg.Tiering.Enabled, cfg.Database.MaxOpenConns = tc.enabled, tc.conns

		err = cfg.Validate()
		if got := err == nil; got != tc.ok {
			t.Errorf("tiering.enabled=%v maxOpenConns=%d: err=%v, want ok=%v", tc.enabled, tc.conns, err, tc.ok)
		}

		if !tc.ok && !errors.Is(err, errTieringPoolSize) {
			t.Errorf("tiering.enabled=%v maxOpenConns=%d: err=%v, want errTieringPoolSize", tc.enabled, tc.conns, err)
		}
	}
}

func TestValidate_RejectsPlaceholderDBPassword(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}

	cfg.Database.Password = "CHANGE_ME"

	if err := cfg.Validate(); !errors.Is(err, errPlaceholderDBPassword) {
		t.Fatalf("Validate() = %v, want errPlaceholderDBPassword", err)
	}

	cfg.Database.Password = "a-real-password"

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with a real password: %v", err)
	}
}

func TestValidate_InvalidPort(t *testing.T) {
	t.Run("invalid port", func(t *testing.T) {
		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		cfg.Server.Port = 99999

		err = cfg.Validate()
		if err == nil {
			t.Error("Validate() should fail for invalid port")
		}

		if !strings.Contains(err.Error(), "invalid server port") {
			t.Errorf("expected error about invalid port, got: %v", err)
		}
	})
}

func TestValidate_MissingDBDriver(t *testing.T) {
	t.Run("missing db driver", func(t *testing.T) {
		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		cfg.Database.Driver = ""

		err = cfg.Validate()
		if err == nil {
			t.Error("Validate() should fail for missing database driver")
		}

		if !strings.Contains(err.Error(), "database driver is required") {
			t.Errorf("expected error about missing driver, got: %v", err)
		}
	})
}

func TestValidate_MissingStorageType(t *testing.T) {
	t.Run("missing storage type", func(t *testing.T) {
		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		cfg.Storage.Type = ""

		err = cfg.Validate()
		if err == nil {
			t.Error("Validate() should fail for missing storage type")
		}

		if !strings.Contains(err.Error(), "storage type is required") {
			t.Errorf("expected error about missing storage type, got: %v", err)
		}
	})
}

func TestValidate_MissingLogLevel(t *testing.T) {
	t.Run("missing log level", func(t *testing.T) {
		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		cfg.Logger.Level = ""

		err = cfg.Validate()
		if err == nil {
			t.Error("Validate() should fail for missing log level")
		}

		if !strings.Contains(err.Error(), "logger level is required") {
			t.Errorf("expected error about missing log level, got: %v", err)
		}
	})
}

func TestLoad_ServerTimeouts(t *testing.T) {
	t.Run("server timeouts", func(t *testing.T) {
		t.Setenv("SERVER_READ_TIMEOUT", "30")
		t.Setenv("SERVER_WRITE_TIMEOUT", "45")
		t.Setenv("SERVER_SHUTDOWN_TIMEOUT", "60")

		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		if cfg.Server.ReadTimeout != 30 {
			t.Errorf("expected ReadTimeout=30, got %d", cfg.Server.ReadTimeout)
		}

		if cfg.Server.WriteTimeout != 45 {
			t.Errorf("expected WriteTimeout=45, got %d", cfg.Server.WriteTimeout)
		}

		if cfg.Server.ShutdownTimeout != 60 {
			t.Errorf("expected ShutdownTimeout=60, got %d", cfg.Server.ShutdownTimeout)
		}
	})
}

func TestLoad_TLSConfig(t *testing.T) {
	t.Run("tls config", func(t *testing.T) {
		t.Setenv("SERVER_TLS_ENABLED", "true")
		t.Setenv("SERVER_TLS_CERT", "/path/to/cert.pem")
		t.Setenv("SERVER_TLS_KEY", "/path/to/key.pem")

		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		if cfg.Server.TLSEnabled != true {
			t.Errorf("expected TLSEnabled=true, got %v", cfg.Server.TLSEnabled)
		}

		if cfg.Server.TLSCert != "/path/to/cert.pem" {
			t.Errorf("expected TLSCert=/path/to/cert.pem, got %s", cfg.Server.TLSCert)
		}

		if cfg.Server.TLSKey != "/path/to/key.pem" {
			t.Errorf("expected TLSKey=/path/to/key.pem, got %s", cfg.Server.TLSKey)
		}
	})
}

func TestLoad_APIKey(t *testing.T) {
	t.Run("api key", func(t *testing.T) {
		t.Setenv("API_KEY", "secret-api-key-123")

		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		if cfg.Server.APIKey != "secret-api-key-123" {
			t.Errorf("expected APIKey=secret-api-key-123, got %s", cfg.Server.APIKey)
		}
	})
}

func TestLoad_GPGConfig(t *testing.T) {
	t.Run("gpg config", func(t *testing.T) {
		t.Setenv("GPG_ENABLED", "true")
		t.Setenv("GPG_KEY_PATH", "/home/user/.gnupg/pubring.gpg")
		t.Setenv("GPG_AUTO_GENERATE", "true")

		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		if cfg.GPG.Enabled != true {
			t.Errorf("expected GPG.Enabled=true, got %v", cfg.GPG.Enabled)
		}

		if cfg.GPG.KeyPath != "/home/user/.gnupg/pubring.gpg" {
			t.Errorf("expected GPG.KeyPath=/home/user/.gnupg/pubring.gpg, got %s", cfg.GPG.KeyPath)
		}

		if cfg.GPG.AutoGenerate != true {
			t.Errorf("expected GPG.AutoGenerate=true, got %v", cfg.GPG.AutoGenerate)
		}
	})
}

func TestLoad_BackupConfig(t *testing.T) {
	t.Run("backup config", func(t *testing.T) {
		t.Setenv("OPENARTIFACT_BACKUP_DIR", "/backups/custom")

		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		if cfg.Backup.BackupDir != "/backups/custom" {
			t.Errorf("expected BackupDir=/backups/custom, got %s", cfg.Backup.BackupDir)
		}
	})
}

func TestLoad_TimeoutDefaults(t *testing.T) {
	t.Run("read/write timeouts default to unbounded, header timeout stays sane", func(t *testing.T) {
		// Explicitly clear so ambient env vars in the test runner never mask
		// the defaults under test (overrideInt no-ops on an unparsable/empty
		// value, so it falls through to defaultConfig's value).
		t.Setenv("SERVER_READ_TIMEOUT", "")
		t.Setenv("SERVER_WRITE_TIMEOUT", "")

		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		if cfg.Server.ReadTimeout != 0 {
			t.Errorf("expected default ReadTimeout=0 (unbounded), got %d", cfg.Server.ReadTimeout)
		}

		if cfg.Server.WriteTimeout != 0 {
			t.Errorf("expected default WriteTimeout=0 (unbounded), got %d", cfg.Server.WriteTimeout)
		}

		if cfg.Server.ReadHeaderTimeout <= 0 {
			t.Errorf("expected a positive default ReadHeaderTimeout to mitigate slowloris, got %d", cfg.Server.ReadHeaderTimeout)
		}
	})
}

func TestLoad_ReadHeaderTimeoutOverride(t *testing.T) {
	t.Run("read header timeout from env", func(t *testing.T) {
		t.Setenv("SERVER_READ_HEADER_TIMEOUT", "25")

		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		if cfg.Server.ReadHeaderTimeout != 25 {
			t.Errorf("expected ReadHeaderTimeout=25, got %d", cfg.Server.ReadHeaderTimeout)
		}
	})
}

func TestLoad_TrustedProxies(t *testing.T) {
	t.Run("defaults to empty (no proxy trusted)", func(t *testing.T) {
		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		if len(cfg.Server.TrustedProxies) != 0 {
			t.Errorf("expected empty TrustedProxies by default, got %v", cfg.Server.TrustedProxies)
		}
	})

	t.Run("trusted proxies from env", func(t *testing.T) {
		t.Setenv("OPENARTIFACT_TRUSTED_PROXIES", "10.0.0.0/8,192.168.1.0/24")

		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}

		if len(cfg.Server.TrustedProxies) != 2 {
			t.Fatalf("expected 2 trusted proxy CIDRs, got %v", cfg.Server.TrustedProxies)
		}

		if cfg.Server.TrustedProxies[0] != "10.0.0.0/8" || cfg.Server.TrustedProxies[1] != "192.168.1.0/24" {
			t.Errorf("unexpected trusted proxies: %v", cfg.Server.TrustedProxies)
		}
	})
}

func TestEnvOverrides_HMACAndOCI(t *testing.T) {
	t.Setenv("AUTH_HMAC_KEY", "k-from-env")
	t.Setenv("STORAGE_OCI_USERNAME", "u")
	t.Setenv("STORAGE_OCI_PASSWORD", "p")
	t.Setenv("STORAGE_OCI_INSECURE", "true")

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Auth.HMACKey != "k-from-env" || cfg.Storage.OCIUsername != "u" || cfg.Storage.OCIPassword != "p" || !cfg.Storage.OCIInsecure {
		t.Fatalf("env overrides not applied: %+v %+v", cfg.Auth.HMACKey, cfg.Storage)
	}
}

func TestBadBoolIgnoredNotFalse(t *testing.T) {
	t.Setenv("SERVER_TLS_ENABLED", "maybe")

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Server.TLSEnabled {
		t.Fatal("invalid bool must keep default")
	}
}

func TestValidate_TLSRequiresFiles(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}

	cfg.Server.TLSEnabled = true
	if cfg.Validate() == nil {
		t.Fatal("TLS without cert/key must fail validation")
	}
}

func TestValidate_MCPTransport(t *testing.T) {
	tests := []struct {
		name      string
		enabled   bool
		transport string
		wantErr   bool
	}{
		{"http enabled", true, "http", false},
		{"stdio enabled is rejected", true, "stdio", true},
		{"empty enabled is rejected", true, "", true},
		{"stdio disabled is ignored", false, "stdio", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load("")
			if err != nil {
				t.Fatal(err)
			}

			cfg.MCP.Enabled = tt.enabled
			cfg.MCP.Transport = tt.transport

			if err := cfg.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDockerGCConfig(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		cfg, err := Load("")
		if err != nil {
			t.Fatal(err)
		}

		if cfg.Workers.DockerGCInterval != time.Hour || cfg.Workers.DockerGCGracePeriod != 24*time.Hour {
			t.Fatalf("defaults = %v / %v, want 1h / 24h", cfg.Workers.DockerGCInterval, cfg.Workers.DockerGCGracePeriod)
		}
	})

	t.Run("env overrides", func(t *testing.T) {
		t.Setenv("OPENARTIFACT_DOCKER_GC_INTERVAL", "0s")
		t.Setenv("OPENARTIFACT_DOCKER_GC_GRACE_PERIOD", "36h")

		cfg, err := Load("")
		if err != nil {
			t.Fatal(err)
		}

		if cfg.Workers.DockerGCInterval != 0 || cfg.Workers.DockerGCGracePeriod != 36*time.Hour {
			t.Fatalf("overrides = %v / %v, want 0s / 36h", cfg.Workers.DockerGCInterval, cfg.Workers.DockerGCGracePeriod)
		}
	})

	t.Run("negative grace is rejected", func(t *testing.T) {
		t.Setenv("OPENARTIFACT_DOCKER_GC_GRACE_PERIOD", "-1h")

		cfg, err := Load("")
		if err != nil {
			t.Fatal(err)
		}

		if err := cfg.Validate(); !errors.Is(err, errNegativeDockerGCGrace) {
			t.Fatalf("Validate() = %v, want errNegativeDockerGCGrace", err)
		}
	})
}

func TestProxyDockerDigestTTLConfig(t *testing.T) {
	t.Run("defaults to 30 days", func(t *testing.T) {
		cfg, err := Load("")
		if err != nil {
			t.Fatal(err)
		}

		if got := cfg.Workers.ProxyDockerDigestTTL; got != 30*24*time.Hour {
			t.Fatalf("default = %v, want 720h", got)
		}
	})

	t.Run("env override, zero disables", func(t *testing.T) {
		t.Setenv("OPENARTIFACT_PROXY_DOCKER_DIGEST_TTL", "0s")

		cfg, err := Load("")
		if err != nil {
			t.Fatal(err)
		}

		if got := cfg.Workers.ProxyDockerDigestTTL; got != 0 {
			t.Fatalf("override = %v, want 0", got)
		}

		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate() = %v, want nil", err)
		}
	})

	t.Run("negative is rejected", func(t *testing.T) {
		t.Setenv("OPENARTIFACT_PROXY_DOCKER_DIGEST_TTL", "-1h")

		cfg, err := Load("")
		if err != nil {
			t.Fatal(err)
		}

		if err := cfg.Validate(); !errors.Is(err, errNegativeDigestTTL) {
			t.Fatalf("Validate() = %v, want errNegativeDigestTTL", err)
		}
	})
}

func TestLoad_PresignDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	s := cfg.Storage
	if s.PresignDownloads {
		t.Error("presignDownloads must default to false")
	}

	if s.PresignMinSize != 1<<20 {
		t.Errorf("presignMinSize = %d, want 1 MiB", s.PresignMinSize)
	}

	if s.PresignTTL != 5*time.Minute || s.PresignUploadTTL != 30*time.Minute {
		t.Errorf("TTLs = %v / %v, want 5m / 30m", s.PresignTTL, s.PresignUploadTTL)
	}

	if s.PresignEndpoint != "" {
		t.Errorf("presignEndpoint = %q, want empty", s.PresignEndpoint)
	}

	if s.PresignMaxPendingUploads != 20 {
		t.Errorf("presignMaxPendingUploads = %d, want 20", s.PresignMaxPendingUploads)
	}
}

func TestLoad_PresignEnvOverrides(t *testing.T) {
	t.Setenv("STORAGE_PRESIGN_DOWNLOADS", "true")
	t.Setenv("STORAGE_PRESIGN_MIN_SIZE", "0")
	t.Setenv("STORAGE_S3_PRESIGN_TTL", "90s")
	t.Setenv("STORAGE_PRESIGN_UPLOAD_TTL", "2h")
	t.Setenv("STORAGE_PRESIGN_ENDPOINT", "https://s3.example.com")
	t.Setenv("STORAGE_PRESIGN_MAX_PENDING_UPLOADS", "0")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	s := cfg.Storage
	if !s.PresignDownloads || s.PresignMinSize != 0 || s.PresignTTL != 90*time.Second ||
		s.PresignUploadTTL != 2*time.Hour || s.PresignEndpoint != "https://s3.example.com" || s.PresignMaxPendingUploads != 0 {
		t.Errorf("env overrides not applied: %+v", s)
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() rejected a valid presign configuration: %v", err)
	}
}

func TestValidate_Presign(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		minSize  int64
		wantErr  bool
	}{
		{"empty endpoint", "", 1, false},
		{"https endpoint", "https://s3.example.com", 1, false},
		{"http endpoint with port and path", "http://minio.svc:9000/prefix", 1, false},
		{"relative endpoint", "s3.example.com", 1, true},
		{"unsupported scheme", "ftp://s3.example.com", 1, true},
		{"endpoint with query", "https://s3.example.com?x=1", 1, true},
		{"endpoint with fragment", "https://s3.example.com#x", 1, true},
		{"negative min size", "", -1, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load("")
			if err != nil {
				t.Fatalf("Load() failed: %v", err)
			}

			cfg.Storage.PresignEndpoint, cfg.Storage.PresignMinSize = tt.endpoint, tt.minSize

			if err := cfg.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidate_PresignMaxPendingUploadsNegative(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	cfg.Storage.PresignMaxPendingUploads = -1

	if err := cfg.Validate(); err == nil {
		t.Error("Validate() accepted a negative presignMaxPendingUploads")
	}
}

func TestLoad_OptionsEnv(t *testing.T) {
	t.Setenv("DB_OPTIONS", "connect_timeout=5, application_name = oa ,bogus")
	t.Setenv("STORAGE_OPTIONS", "k=v=w")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if got := cfg.Database.Options; len(got) != 2 || got["connect_timeout"] != "5" || got["application_name"] != "oa" {
		t.Errorf("database options = %v", got)
	}

	if got := cfg.Storage.Options; len(got) != 1 || got["k"] != "v=w" {
		t.Errorf("storage options = %v", got)
	}
}

func TestLoad_MaxBodyBytes(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Server.MaxBodyBytes != 16<<30 {
		t.Errorf("default MaxBodyBytes = %d, want 16 GiB", cfg.Server.MaxBodyBytes)
	}

	t.Setenv("SERVER_MAX_BODY_BYTES", "1048576")

	cfg, err = Load("")
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Server.MaxBodyBytes != 1<<20 {
		t.Errorf("env MaxBodyBytes = %d, want 1 MiB", cfg.Server.MaxBodyBytes)
	}
}

func TestLoad_TieringDefaultsAndEnv(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	d := cfg.Tiering
	if !d.Enabled || !d.VerifyReadback || d.Interval != 5*time.Minute || d.Batch != 200 || d.Concurrency != 2 || d.MaxBytesPerSecond != 64<<20 {
		t.Errorf("unexpected tiering defaults: %+v", d)
	}

	t.Setenv("OPENARTIFACT_TIERING_ENABLED", "false")
	t.Setenv("OPENARTIFACT_TIERING_INTERVAL", "90s")
	t.Setenv("OPENARTIFACT_TIERING_BATCH", "5")
	t.Setenv("OPENARTIFACT_TIERING_CONCURRENCY", "7")
	t.Setenv("OPENARTIFACT_TIERING_MAX_BYTES_PER_SECOND", "1024")
	t.Setenv("OPENARTIFACT_TIERING_VERIFY_READBACK", "false")
	t.Setenv("OPENARTIFACT_TIERING_ACCESS_FLUSH_INTERVAL", "2m")
	t.Setenv("OPENARTIFACT_TIERING_REFRESH_INTERVAL", "3m")

	cfg, err = Load("")
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	want := TieringConfig{
		Enabled: false, Interval: 90 * time.Second, Batch: 5, Concurrency: 7, MaxBytesPerSecond: 1024,
		VerifyReadback: false, AccessFlushInterval: 2 * time.Minute, RefreshInterval: 3 * time.Minute,
	}
	if cfg.Tiering != want {
		t.Errorf("tiering = %+v, want %+v", cfg.Tiering, want)
	}
}

func TestValidate_Tiering(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(c *TieringConfig)
		ok     bool
	}{
		{"defaults", func(*TieringConfig) {}, true},
		{"unlimited bandwidth", func(c *TieringConfig) { c.MaxBytesPerSecond = 0 }, true},
		{"zero batch", func(c *TieringConfig) { c.Batch = 0 }, false},
		{"zero concurrency", func(c *TieringConfig) { c.Concurrency = 0 }, false},
		{"negative bandwidth", func(c *TieringConfig) { c.MaxBytesPerSecond = -1 }, false},
		{"negative flush interval", func(c *TieringConfig) { c.AccessFlushInterval = -time.Second }, false},
		{"negative refresh interval", func(c *TieringConfig) { c.RefreshInterval = -time.Second }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load("")
			if err != nil {
				t.Fatalf("Load() failed: %v", err)
			}

			tc.mutate(&cfg.Tiering)

			if err := cfg.validateTiering(); (err == nil) != tc.ok {
				t.Errorf("validateTiering() = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestLoad_BlobDeletionAndBackupLeaseDefaultsAndEnv(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if d := cfg.BlobDeletion; d.Grace != 24*time.Hour || d.SweepInterval != time.Minute || d.SweepBatch != 200 {
		t.Errorf("unexpected blob deletion defaults: %+v", d)
	}

	if cfg.Backup.LeaseTTL != 5*time.Minute {
		t.Errorf("backup lease TTL default = %v, want 5m", cfg.Backup.LeaseTTL)
	}

	t.Setenv("OPENARTIFACT_BLOB_DELETION_GRACE", "0s")
	t.Setenv("OPENARTIFACT_BLOB_DELETION_SWEEP_INTERVAL", "10s")
	t.Setenv("OPENARTIFACT_BLOB_DELETION_SWEEP_BATCH", "7")
	t.Setenv("OPENARTIFACT_BACKUP_LEASE_TTL", "90s")

	cfg, err = Load("")
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if want := (BlobDeletionConfig{Grace: 0, SweepInterval: 10 * time.Second, SweepBatch: 7}); cfg.BlobDeletion != want {
		t.Errorf("blob deletion = %+v, want %+v", cfg.BlobDeletion, want)
	}

	if cfg.Backup.LeaseTTL != 90*time.Second {
		t.Errorf("backup lease TTL = %v, want 90s", cfg.Backup.LeaseTTL)
	}
}

func TestValidate_BlobRetention(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(c *Config)
		ok     bool
	}{
		{"defaults", func(*Config) {}, true},
		{"grace zero means immediate", func(c *Config) { c.BlobDeletion.Grace = 0 }, true},
		{"negative grace", func(c *Config) { c.BlobDeletion.Grace = -time.Second }, false},
		{"schedule disabled", func(c *Config) { c.BlobDeletion.SweepInterval = 0 }, true},
		{"zero batch", func(c *Config) { c.BlobDeletion.SweepBatch = 0 }, false},
		{"zero lease TTL", func(c *Config) { c.Backup.LeaseTTL = 0 }, false},
		{"negative lease TTL", func(c *Config) { c.Backup.LeaseTTL = -time.Second }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load("")
			if err != nil {
				t.Fatalf("Load() failed: %v", err)
			}

			tc.mutate(cfg)

			if err := cfg.validateBlobRetention(); (err == nil) != tc.ok {
				t.Errorf("validateBlobRetention() = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestLoad_MCPLimitsDefaultsAndEnv(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}

	if m := cfg.MCP; m.MaxBodyBytes != 1<<20 || m.RateLimitPerMinute != 600 || m.AITriggerPerMinute != 10 || len(m.AllowedOrigins) != 0 {
		t.Fatalf("unexpected MCP defaults: %+v", m)
	}

	t.Setenv("MCP_ALLOWED_ORIGINS", "https://a.example.org, https://b.example.org:8443")
	t.Setenv("MCP_MAX_BODY_BYTES", "2048")
	t.Setenv("MCP_RATE_LIMIT_PER_MINUTE", "0")
	t.Setenv("MCP_AI_TRIGGER_PER_MINUTE", "3")

	cfg, err = Load("")
	if err != nil {
		t.Fatal(err)
	}

	m := cfg.MCP
	if m.MaxBodyBytes != 2048 || m.RateLimitPerMinute != 0 || m.AITriggerPerMinute != 3 {
		t.Errorf("env overrides not applied: %+v", m)
	}

	if len(m.AllowedOrigins) != 2 || m.AllowedOrigins[0] != "https://a.example.org" || m.AllowedOrigins[1] != "https://b.example.org:8443" {
		t.Errorf("allowed origins = %q", m.AllowedOrigins)
	}
}

func TestValidate_MCPLimitsAndOrigins(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*MCPConfig)
		wantErr bool
	}{
		{"defaults", func(*MCPConfig) {}, false},
		{"valid origins", func(m *MCPConfig) { m.AllowedOrigins = []string{"https://a.example.org", "http://localhost:3000"} }, false},
		{"origin with a path", func(m *MCPConfig) { m.AllowedOrigins = []string{"https://a.example.org/app"} }, true},
		{"origin without scheme", func(m *MCPConfig) { m.AllowedOrigins = []string{"a.example.org"} }, true},
		{"wildcard origin", func(m *MCPConfig) { m.AllowedOrigins = []string{"*"} }, true},
		{"zero body cap", func(m *MCPConfig) { m.MaxBodyBytes = 0 }, true},
		{"negative rate limit", func(m *MCPConfig) { m.RateLimitPerMinute = -1 }, true},
		{"rate limits off", func(m *MCPConfig) { m.RateLimitPerMinute, m.AITriggerPerMinute = 0, 0 }, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load("")
			if err != nil {
				t.Fatal(err)
			}

			cfg.MCP.Enabled = true
			tt.mutate(&cfg.MCP)

			if err := cfg.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRecoveryJournalConfig(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		cfg, err := Load("")
		if err != nil {
			t.Fatal(err)
		}

		j := cfg.Recovery.Journal
		if !j.Enabled || j.Interval <= 0 || j.Batch < 1 || j.Concurrency < 1 || j.ReconcileInterval <= 0 {
			t.Fatalf("unexpected defaults: %+v", j)
		}
	})

	t.Run("env overrides", func(t *testing.T) {
		t.Setenv("OPENARTIFACT_RECOVERY_JOURNAL_ENABLED", "false")
		t.Setenv("OPENARTIFACT_RECOVERY_JOURNAL_INTERVAL", "3s")
		t.Setenv("OPENARTIFACT_RECOVERY_JOURNAL_BATCH", "7")
		t.Setenv("OPENARTIFACT_RECOVERY_JOURNAL_CONCURRENCY", "2")
		t.Setenv("OPENARTIFACT_RECOVERY_JOURNAL_RECONCILE_INTERVAL", "1h")

		cfg, err := Load("")
		if err != nil {
			t.Fatal(err)
		}

		j := cfg.Recovery.Journal
		if j.Enabled || j.Interval != 3*time.Second || j.Batch != 7 || j.Concurrency != 2 || j.ReconcileInterval != time.Hour {
			t.Fatalf("overrides not applied: %+v", j)
		}
	})

	for name, env := range map[string]map[string]string{
		"zero interval while enabled": {"OPENARTIFACT_RECOVERY_JOURNAL_INTERVAL": "0s"},
		"zero batch":                  {"OPENARTIFACT_RECOVERY_JOURNAL_BATCH": "0"},
		"zero concurrency":            {"OPENARTIFACT_RECOVERY_JOURNAL_CONCURRENCY": "0"},
		"negative reconcile":          {"OPENARTIFACT_RECOVERY_JOURNAL_RECONCILE_INTERVAL": "-1h"},
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			for k, v := range env {
				t.Setenv(k, v)
			}

			cfg, err := Load("")
			if err != nil {
				t.Fatal(err)
			}

			if err := cfg.Validate(); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

func TestLoad_AuditRetentionDefaultsAndEnv(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if want := (AuditConfig{RetentionDays: 0, PurgeInterval: time.Hour, PurgeBatchSize: 5000}); cfg.Audit != want {
		t.Errorf("audit defaults = %+v, want %+v (retention 0 keeps the log forever)", cfg.Audit, want)
	}

	t.Setenv("OPENARTIFACT_AUDIT_RETENTION_DAYS", "90")
	t.Setenv("OPENARTIFACT_AUDIT_PURGE_INTERVAL", "10m")
	t.Setenv("OPENARTIFACT_AUDIT_PURGE_BATCH_SIZE", "250")

	cfg, err = Load("")
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if want := (AuditConfig{RetentionDays: 90, PurgeInterval: 10 * time.Minute, PurgeBatchSize: 250}); cfg.Audit != want {
		t.Errorf("audit = %+v, want %+v", cfg.Audit, want)
	}
}

func TestValidate_Audit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(c *Config)
		ok     bool
	}{
		{"defaults keep forever", func(*Config) {}, true},
		{"retention set", func(c *Config) { c.Audit.RetentionDays = 365 }, true},
		{"negative retention", func(c *Config) { c.Audit.RetentionDays = -1 }, false},
		{"zero interval", func(c *Config) { c.Audit.PurgeInterval = 0 }, false},
		{"zero batch", func(c *Config) { c.Audit.PurgeBatchSize = 0 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load("")
			if err != nil {
				t.Fatalf("Load() failed: %v", err)
			}

			tc.mutate(cfg)

			if err := cfg.validateAudit(); (err == nil) != tc.ok {
				t.Errorf("validateAudit() = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestLoad_BootstrapConfig(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if cfg.Bootstrap.GeneratedPasswordTTL != 24*time.Hour || cfg.Bootstrap.ForcePasswordChange || cfg.Bootstrap.AdminTokenTTL != 0 {
		t.Errorf("unexpected defaults: %+v", cfg.Bootstrap)
	}

	t.Setenv("OPENARTIFACT_BOOTSTRAP_ADMIN_PASSWORD_FILE", "/run/secrets/pw")
	t.Setenv("OPENARTIFACT_BOOTSTRAP_ADMIN_TOKEN_FILE", "/run/secrets/token")
	t.Setenv("OPENARTIFACT_BOOTSTRAP_ADMIN_TOKEN_TTL", "720h")
	t.Setenv("OPENARTIFACT_BOOTSTRAP_GENERATED_PASSWORD_TTL", "0s")
	t.Setenv("OPENARTIFACT_BOOTSTRAP_FORCE_PASSWORD_CHANGE", "true")

	cfg, err = Load("")
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	want := BootstrapConfig{
		AdminPasswordFile:   "/run/secrets/pw",
		AdminTokenFile:      "/run/secrets/token",
		AdminTokenTTL:       720 * time.Hour,
		ForcePasswordChange: true,
	}
	if cfg.Bootstrap != want {
		t.Errorf("Bootstrap = %+v, want %+v", cfg.Bootstrap, want)
	}
}
