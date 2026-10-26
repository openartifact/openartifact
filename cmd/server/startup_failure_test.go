// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

const leakProbe = "sup3r-s3cret-v@lue-0123456789abcdef"

func writeFile(t *testing.T, name, content string) string {
	t.Helper()

	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return p
}

// resetStartupEnv isolates a test from the developer's environment and restores
// the process-wide encryption key afterwards.
func resetStartupEnv(t *testing.T) {
	t.Helper()

	for _, k := range []string{
		"OPENARTIFACT_CONFIG", "SERVER_PORT", "STORAGE_TYPE", "STORAGE_BACKEND", "LOG_OUTPUT", "LOG_FORMAT",
		"OPENARTIFACT_SECRET_KEY", "DB_PASSWORD", "STORAGE_SECRET_KEY",
	} {
		t.Setenv(k, "")

		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}

	t.Cleanup(func() { _ = crypto.Init("") })
}

func TestNewAppRejectsBadStartupInput(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "x", "app.log")

	tests := []struct {
		name    string
		env     map[string]string
		yaml    string // written to a config file when non-empty
		cfgPath string // used verbatim when set
		want    string
	}{
		{name: "missing config file", cfgPath: filepath.Join(t.TempDir(), "absent.yaml"), want: "failed to load configuration"},
		{name: "malformed yaml", yaml: "server: [unclosed\n", want: "failed to load configuration"},
		{name: "port zero", env: map[string]string{"SERVER_PORT": "0"}, want: "invalid configuration"},
		{name: "port too large", env: map[string]string{"SERVER_PORT": "65536"}, want: "invalid configuration"},
		{name: "placeholder db password", env: map[string]string{"DB_PASSWORD": "CHANGE_ME"}, want: "invalid configuration"},
		{name: "tls without files", yaml: "server:\n  tlsEnabled: true\n", want: "invalid configuration"},
		{name: "secret key too short", env: map[string]string{"OPENARTIFACT_SECRET_KEY": "short"}, want: "invalid " + crypto.EnvKey},
		{name: "unwritable log file", env: map[string]string{"LOG_OUTPUT": logPath}, yaml: "", want: "failed to initialize logger"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resetStartupEnv(t)

			for k, v := range tc.env {
				t.Setenv(k, v)
			}

			path := tc.cfgPath
			if tc.yaml != "" {
				path = writeFile(t, "cfg.yaml", tc.yaml)
			}

			a, err := newApp(options{configFile: path})
			if err == nil {
				a.close()
				t.Fatal("newApp succeeded, want an error")
			}

			if a != nil {
				t.Errorf("newApp returned a partially built app alongside %v", err)
			}

			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}

			if crypto.Enabled() {
				t.Error("encryption was left enabled by a failed startup")
			}
		})
	}
}

// A rejected secret key must be named in the error but never echoed.
func TestNewAppDoesNotEchoRejectedSecretKey(t *testing.T) {
	resetStartupEnv(t)

	key := "tiny-" + leakProbe[:6]
	t.Setenv("OPENARTIFACT_SECRET_KEY", key)

	_, err := newApp(options{})
	if err == nil {
		t.Fatal("newApp accepted a key below the minimum length")
	}

	if strings.Contains(err.Error(), key) {
		t.Errorf("error leaks the secret key: %v", err)
	}
}

func TestNewAppAcceptsValidConfigAndEnablesEncryption(t *testing.T) {
	resetStartupEnv(t)
	t.Setenv("OPENARTIFACT_SECRET_KEY", strings.Repeat("k", crypto.MinKeyLen))
	t.Setenv("LOG_OUTPUT", filepath.Join(t.TempDir(), "app.log"))

	a, err := newApp(options{})
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}

	a.close()

	if !crypto.Enabled() {
		t.Error("a valid secret key did not enable encryption")
	}

	if a.tasks == nil || a.cfg == nil || a.log == nil {
		t.Error("newApp returned an app without its core services")
	}
}

func newTestApp(t *testing.T, cfg *config.Config) (*app, string) {
	t.Helper()

	logPath := filepath.Join(t.TempDir(), "app.log")

	log, err := logger.New("debug", "json", logPath)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = log.Sync() })

	return &app{cfg: cfg, log: log}, logPath
}

func TestSetupStorageFailureLeavesNothingStartedAndLeaksNoSecret(t *testing.T) {
	tests := []struct {
		name string
		cfg  storage.StorageConfig
	}{
		{"unknown backend", storage.StorageConfig{Type: "tape", SecretKey: leakProbe}},
		{"azure with invalid account key", storage.StorageConfig{
			Type: "azure", AzureAccountName: "acct", AzureAccountKey: leakProbe, AzureContainerName: "c",
		}},
		{"filesystem path is a regular file", storage.StorageConfig{Type: "filesystem", SecretKey: leakProbe}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.cfg.Type == "filesystem" {
				tc.cfg.Path = filepath.Join(writeFile(t, "blocker", "x"), "sub")
			}

			a, logPath := newTestApp(t, &config.Config{Storage: tc.cfg})

			err := a.setupStorage()
			if err == nil {
				t.Fatal("setupStorage succeeded, want an error")
			}

			if _, ok := errors.AsType[reportedError](err); !ok {
				t.Errorf("error %T is not a reportedError; main would print it twice or not at all", err)
			}

			if !strings.Contains(err.Error(), "failed to initialize storage") {
				t.Errorf("error %q does not say storage failed", err)
			}

			if a.store.raw != nil || a.store.blobs != nil || a.store.tiers != nil || a.store.tierAccess != nil {
				t.Errorf("failed storage setup left services behind: %+v", a.store)
			}

			_ = a.log.Sync()

			logged, _ := os.ReadFile(logPath)
			if strings.Contains(err.Error(), leakProbe) || strings.Contains(string(logged), leakProbe) {
				t.Errorf("secret leaked.\nerror: %v\nlog: %s", err, logged)
			}
		})
	}
}

func TestNewReplication(t *testing.T) {
	t.Run("disabled replica is not an error and builds nothing", func(t *testing.T) {
		cfg := config.Defaults()

		svc, err := newReplication(cfg, nil, nil, nil, nil)
		if err != nil || svc != nil {
			t.Fatalf("svc=%v err=%v, want nil, nil", svc, err)
		}
	})

	t.Run("unusable replica backend is reported and does not leak its secret", func(t *testing.T) {
		cfg := config.Defaults()
		cfg.StorageReplica.Type = "azure"
		cfg.StorageReplica.AzureAccountName = "acct"
		cfg.StorageReplica.AzureContainerName = "c"
		cfg.StorageReplica.AzureAccountKey = leakProbe

		svc, err := newReplication(cfg, nil, nil, nil, nil)
		if err == nil || svc != nil {
			t.Fatalf("svc=%v err=%v, want an error and no service", svc, err)
		}

		if !strings.Contains(err.Error(), "open storage.replica") {
			t.Errorf("error %q does not name storage.replica", err)
		}

		if strings.Contains(err.Error(), leakProbe) {
			t.Errorf("error leaks the replica secret: %v", err)
		}
	})
}

func TestSetupReplicationFailureIsFatal(t *testing.T) {
	cfg := config.Defaults()
	cfg.StorageReplica.Type = "tape"

	if !cfg.StorageReplica.Enabled() {
		t.Skip("replica with a type is not considered enabled")
	}

	a, _ := newTestApp(t, cfg)

	top, err := a.setupReplication()
	if err == nil || top != nil {
		t.Fatalf("top=%v err=%v, want a fatal error and no store", top, err)
	}

	if !strings.Contains(err.Error(), "failed to initialize blob replication") {
		t.Errorf("error %q does not say replication failed", err)
	}
}

func TestRunHealthcheckConfigErrors(t *testing.T) {
	resetStartupEnv(t)

	err := runHealthcheck(writeFile(t, "bad.yaml", "server: [unclosed\n"))
	if err == nil || !strings.Contains(err.Error(), "load configuration") {
		t.Fatalf("err=%v, want a load configuration error", err)
	}

	err = runHealthcheck(filepath.Join(t.TempDir(), "absent.yaml"))
	if err == nil {
		t.Fatal("a missing config file passed the healthcheck")
	}
}
