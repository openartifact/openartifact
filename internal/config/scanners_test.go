// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoad_ScannersDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	s := cfg.Scanners
	if s.UpdateInterval != 6*time.Hour || s.Offline || s.CacheDir != "" || s.DownloadBaseURL != "https://github.com" ||
		s.Trivy != (TrivyScanner{Install: "none"}) || s.Grype != (GrypeScanner{Install: "none"}) {
		t.Errorf("defaults = %+v", s)
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("defaults must validate: %v", err)
	}
}

func TestLoad_ScannersYAMLAndEnv(t *testing.T) {
	yamlText := `scanners:
  cacheDir: /data/scanners
  updateInterval: 12h
  offline: false
  downloadBaseUrl: https://mirror.example/github
  trivy:
    path: /opt/trivy
    install: auto
    dbRepository: mirror.example/trivy-db
    javaDbRepository: mirror.example/trivy-java-db
  grype:
    path: grype
    dbUrl: https://mirror.example/grype/listing.json
`

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(yamlText), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := ScannersConfig{
		CacheDir:        "/data/scanners",
		UpdateInterval:  12 * time.Hour,
		DownloadBaseURL: "https://mirror.example/github",
		Trivy:           TrivyScanner{Path: "/opt/trivy", Install: "auto", DBRepository: "mirror.example/trivy-db", JavaDBRepository: "mirror.example/trivy-java-db"},
		Grype:           GrypeScanner{Path: "grype", Install: "none", DBURL: "https://mirror.example/grype/listing.json"},
	}
	if cfg.Scanners != want {
		t.Fatalf("yaml: got %+v, want %+v", cfg.Scanners, want)
	}

	t.Setenv("OPENARTIFACT_SCANNER_CACHE_DIR", "/env/cache")
	t.Setenv("OPENARTIFACT_SCANNER_UPDATE_INTERVAL", "24h")
	t.Setenv("OPENARTIFACT_SCANNER_OFFLINE", "true")
	t.Setenv("OPENARTIFACT_TRIVY_PATH", "/env/trivy")
	t.Setenv("OPENARTIFACT_TRIVY_DB_REPOSITORY", "env.example/db")
	t.Setenv("OPENARTIFACT_TRIVY_JAVA_DB_REPOSITORY", "env.example/java")
	t.Setenv("OPENARTIFACT_GRYPE_PATH", "/env/grype")
	t.Setenv("OPENARTIFACT_GRYPE_DB_URL", "https://env.example/listing.json")
	t.Setenv("OPENARTIFACT_SCANNER_DOWNLOAD_BASE_URL", "https://env.example/gh")
	t.Setenv("OPENARTIFACT_TRIVY_INSTALL", "none")
	t.Setenv("OPENARTIFACT_GRYPE_INSTALL", "auto")

	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want = ScannersConfig{
		CacheDir:        "/env/cache",
		UpdateInterval:  24 * time.Hour,
		Offline:         true,
		DownloadBaseURL: "https://env.example/gh",
		Trivy:           TrivyScanner{Path: "/env/trivy", Install: "none", DBRepository: "env.example/db", JavaDBRepository: "env.example/java"},
		Grype:           GrypeScanner{Path: "/env/grype", Install: "auto", DBURL: "https://env.example/listing.json"},
	}
	if cfg.Scanners != want {
		t.Fatalf("env: got %+v, want %+v", cfg.Scanners, want)
	}

	opts := cfg.Scanners.VulnOptions()
	if opts.CacheDir != "/env/cache" || !opts.Offline || opts.UpdateInterval != 24*time.Hour ||
		opts.Trivy.Path != "/env/trivy" || opts.Trivy.DBRepository != "env.example/db" || opts.Trivy.JavaDBRepository != "env.example/java" ||
		opts.Grype.Path != "/env/grype" || opts.Grype.DBURL != "https://env.example/listing.json" ||
		opts.DownloadBaseURL != "https://env.example/gh" || opts.Trivy.Install || !opts.Grype.Install {
		t.Errorf("VulnOptions lost a setting: %+v", opts)
	}
}

func TestValidate_Scanners(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*ScannersConfig)
		wantErr bool
	}{
		{"defaults", func(*ScannersConfig) {}, false},
		{"interval zero (download once)", func(s *ScannersConfig) { s.UpdateInterval = 0 }, false},
		{"interval too short", func(s *ScannersConfig) { s.UpdateInterval = time.Minute }, true},
		{"interval negative", func(s *ScannersConfig) { s.UpdateInterval = -time.Hour }, true},
		{"absolute cache dir", func(s *ScannersConfig) { s.CacheDir = "/var/cache/oa" }, false},
		{"relative cache dir", func(s *ScannersConfig) { s.CacheDir = "cache" }, true},
		{"trivy by absolute path", func(s *ScannersConfig) { s.Trivy.Path = "/usr/local/bin/trivy" }, false},
		{"trivy by command name", func(s *ScannersConfig) { s.Trivy.Path = "trivy-0.60" }, false},
		{"trivy by relative path", func(s *ScannersConfig) { s.Trivy.Path = "./bin/trivy" }, true},
		{"grype path starting with dash", func(s *ScannersConfig) { s.Grype.Path = "-x" }, true},
		{"trivy mirror", func(s *ScannersConfig) { s.Trivy.DBRepository = "registry.example.com:5000/trivy-db:2" }, false},
		{"trivy mirror like a flag", func(s *ScannersConfig) { s.Trivy.DBRepository = "--skip-db-update" }, true},
		{"trivy mirror with space", func(s *ScannersConfig) { s.Trivy.DBRepository = "a b" }, true},
		{"java mirror with newline", func(s *ScannersConfig) { s.Trivy.JavaDBRepository = "a\nb" }, true},
		{"grype mirror https", func(s *ScannersConfig) { s.Grype.DBURL = "https://mirror.example/listing.json" }, false},
		{"grype mirror ftp", func(s *ScannersConfig) { s.Grype.DBURL = "ftp://mirror.example/listing.json" }, true},
		{"grype mirror with userinfo", func(s *ScannersConfig) { s.Grype.DBURL = "https://" + "u" + ":" + "p" + "@mirror.example/listing.json" }, true},
		{"grype mirror without host", func(s *ScannersConfig) { s.Grype.DBURL = "https:///x" }, true},
		{"trivy install auto", func(s *ScannersConfig) { s.Trivy.Install = "auto" }, false},
		{"grype install unknown", func(s *ScannersConfig) { s.Grype.Install = "always" }, true},
		{"trivy install empty", func(s *ScannersConfig) { s.Trivy.Install = "" }, true},
		{"download base mirror with path", func(s *ScannersConfig) { s.DownloadBaseURL = "https://mirror.example/github/" }, false},
		{"download base http", func(s *ScannersConfig) { s.DownloadBaseURL = "http://mirror.example" }, true},
		{"download base loopback http", func(s *ScannersConfig) { s.DownloadBaseURL = "http://127.0.0.1:8000" }, true},
		{"download base userinfo", func(s *ScannersConfig) { s.DownloadBaseURL = "https://user:pw@mirror.example" }, true},
		{"download base query", func(s *ScannersConfig) { s.DownloadBaseURL = "https://mirror.example/?a=b" }, true},
		{"download base empty", func(s *ScannersConfig) { s.DownloadBaseURL = "" }, true},
		{"download base without host", func(s *ScannersConfig) { s.DownloadBaseURL = "https:///x" }, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load("")
			if err != nil {
				t.Fatalf("Load: %v", err)
			}

			tt.mutate(&cfg.Scanners)

			if err := cfg.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
