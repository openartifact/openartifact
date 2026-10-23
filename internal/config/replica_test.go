// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/storage"
)

func loadReplicaYAML(t *testing.T, yaml string) *Config {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	return cfg
}

func TestReplicaDisabledByDefault(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}

	if cfg.StorageReplica.Enabled() {
		t.Fatal("a replica is configured by default")
	}

	want := storage.DefaultReplicaConfig()
	got := cfg.StorageReplica

	if got.ReadFallback != want.ReadFallback || got.Interval != 5*time.Second || got.Batch != 100 || got.Concurrency != 4 ||
		got.VerifyInterval != 24*time.Hour || got.VerifyDeep || got.MaxBackoff != time.Hour {
		t.Fatalf("defaults = %+v", got)
	}

	if err := cfg.validateReplica(); err != nil {
		t.Fatalf("validate without a replica: %v", err)
	}
}

func TestReplicaYAMLKeepsDefaultsAndTheNestedPrimary(t *testing.T) {
	cfg := loadReplicaYAML(t, `
storage:
  type: s3
  bucket: primary-bucket
  endpoint: https://s3.eu-west-1.amazonaws.com
  region: eu-west-1
  replica:
    type: s3
    bucket: replica-bucket
    endpoint: https://s3.eu-central-1.amazonaws.com
    region: eu-central-1
    accessKey: AK
    secretKey: SK
    readFallback: error
    interval: 2s
    verifyInterval: 0s
    verifyDeep: true
`)

	r := cfg.StorageReplica

	if !r.Enabled() || r.Type != "s3" || r.Bucket != "replica-bucket" || r.Region != "eu-central-1" || r.AccessKey != "AK" || r.SecretKey != "SK" {
		t.Fatalf("replica backend = %+v", r.StorageConfig)
	}

	if r.ReadFallback != "error" || r.Interval != 2*time.Second || r.VerifyInterval != 0 || !r.VerifyDeep {
		t.Fatalf("replica tuning = %+v", r)
	}

	// Keys the file does not set keep their defaults.
	if r.Batch != 100 || r.Concurrency != 4 || r.MaxBackoff != time.Hour {
		t.Fatalf("defaults lost: %+v", r)
	}

	// The primary is untouched by the nested block.
	if cfg.Storage.Bucket != "primary-bucket" || cfg.Storage.Region != "eu-west-1" || cfg.Storage.Type != "s3" {
		t.Fatalf("primary = %+v", cfg.Storage)
	}

	if err := cfg.validateReplica(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestReplicaEnvOverrides(t *testing.T) {
	env := map[string]string{
		"TYPE": "s3", "ENDPOINT": "http://garage:3900", "BUCKET": "dr", "REGION": "garage", "ACCESS_KEY": "ak", "SECRET_KEY": "sk",
		"FORCE_PATH_STYLE": "true", "READ_FALLBACK": "off", "INTERVAL": "9s", "BATCH": "7", "CONCURRENCY": "3",
		"VERIFY_INTERVAL": "2h", "VERIFY_DEEP": "true", "MAX_BACKOFF": "10m",
	}

	for k, v := range env {
		t.Setenv("OPENARTIFACT_STORAGE_REPLICA_"+k, v)
	}

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}

	r := cfg.StorageReplica
	if r.Type != "s3" || r.Endpoint != "http://garage:3900" || r.Bucket != "dr" || r.Region != "garage" || r.AccessKey != "ak" ||
		r.SecretKey != "sk" || !r.ForcePathStyle {
		t.Fatalf("backend = %+v", r.StorageConfig)
	}

	if r.ReadFallback != "off" || r.Interval != 9*time.Second || r.Batch != 7 || r.Concurrency != 3 ||
		r.VerifyInterval != 2*time.Hour || !r.VerifyDeep || r.MaxBackoff != 10*time.Minute {
		t.Fatalf("tuning = %+v", r)
	}

	if err := cfg.validateReplica(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestReplicaFilesystemEnv(t *testing.T) {
	t.Setenv("OPENARTIFACT_STORAGE_REPLICA_TYPE", "filesystem")
	t.Setenv("OPENARTIFACT_STORAGE_REPLICA_PATH", "/mnt/replica")

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}

	if cfg.StorageReplica.Path != "/mnt/replica" || cfg.StorageReplica.Summary() != "/mnt/replica" {
		t.Fatalf("replica = %+v", cfg.StorageReplica)
	}

	if err := cfg.validateReplica(); err != nil {
		t.Fatal(err)
	}
}

func TestReplicaValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(c *Config)
		wantErr string
	}{
		{"unknown type", func(c *Config) { c.StorageReplica.Type = "ftp" }, "storage.replica.type"},
		{"bad fallback", func(c *Config) { c.StorageReplica.ReadFallback = "sometimes" }, "readFallback"},
		{"zero batch", func(c *Config) { c.StorageReplica.Batch = 0 }, "at least 1"},
		{"zero concurrency", func(c *Config) { c.StorageReplica.Concurrency = 0 }, "at least 1"},
		{"zero interval", func(c *Config) { c.StorageReplica.Interval = 0 }, "positive"},
		{"zero backoff", func(c *Config) { c.StorageReplica.MaxBackoff = 0 }, "positive"},
		{"negative verify interval", func(c *Config) { c.StorageReplica.VerifyInterval = -time.Second }, "positive"},
		{"filesystem without a path", func(c *Config) { c.StorageReplica.Path = "" }, "needs the location"},
		{"same directory as the primary", func(c *Config) { c.StorageReplica.Path = "/var/lib/artifacts" }, "storage.replica"},
		{"nested in the primary directory", func(c *Config) { c.StorageReplica.Path = "/var/lib/artifacts/replica" }, "storage.replica"},
		{"pool too small for the concurrency", func(c *Config) { c.Database.MaxOpenConns = 5 }, "maxOpenConns"},
		{"pool just big enough", func(c *Config) { c.Database.MaxOpenConns = 10 }, ""},
		{"valid", func(*Config) {}, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultConfig()
			cfg.StorageReplica.Type = "filesystem"
			cfg.StorageReplica.Path = "/mnt/replica"
			tc.mutate(cfg)

			err := cfg.validateReplica()

			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestReplicaMustNotAliasTheObjectStorePrimary(t *testing.T) {
	cfg := defaultConfig()
	cfg.Storage = storage.StorageConfig{Type: "s3", Endpoint: "https://s3.example.com", Bucket: "artifacts", Region: "us-east-1"}
	cfg.StorageReplica.Type = "s3"
	cfg.StorageReplica.Endpoint = "https://s3.example.com"
	cfg.StorageReplica.Bucket = "artifacts"

	if err := cfg.validateReplica(); err == nil {
		t.Fatal("the same bucket as the primary was accepted")
	}

	cfg.StorageReplica.Bucket = "artifacts-dr"

	if err := cfg.validateReplica(); err != nil {
		t.Fatalf("a different bucket was refused: %v", err)
	}
}

func TestReplicaStrayTuningIsHarmlessWithoutABackend(t *testing.T) {
	cfg := loadReplicaYAML(t, `
storage:
  replica:
    batch: 0
`)

	if cfg.StorageReplica.Enabled() {
		t.Fatal("enabled without a type")
	}

	if err := cfg.validateReplica(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestReplicaSummaryHidesCredentials(t *testing.T) {
	tests := []struct {
		cfg  storage.StorageConfig
		want string
	}{
		{storage.StorageConfig{Type: "s3", Endpoint: "https://" + "who" + ":" + "secret" + "@" + "s3.example.com:9000/path?x=1", Bucket: "b", SecretKey: "sk"}, "s3.example.com:9000/b"},
		{storage.StorageConfig{Type: "azure", AzureAccountName: "acct", AzureContainerName: "c", AzureAccountKey: "key"}, "acct/c"},
		{storage.StorageConfig{Type: "gcs", GCSBucket: "gb", GCSHMACSecret: "x"}, "gb"},
		{storage.StorageConfig{Type: "filesystem", Path: "/data"}, "/data"},
	}

	for _, tc := range tests {
		got := storage.ReplicaConfig{StorageConfig: tc.cfg}.Summary()
		if got != tc.want || strings.Contains(got, "secret") {
			t.Errorf("Summary(%s) = %q, want %q", tc.cfg.Type, got, tc.want)
		}
	}
}
