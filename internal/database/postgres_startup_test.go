// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"strings"
	"testing"

	"github.com/openartifact/openartifact/pkg/logger"
)

// A database that cannot be opened must fail NewPostgresDB with an error that
// never carries the password, whichever step rejected the configuration.
func TestNewPostgresDBFailuresDoNotLeakThePassword(t *testing.T) {
	const password = "pw-Zk39!probe value"

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		cfg  DatabaseConfig
	}{
		{"option key injection", DatabaseConfig{Host: "127.0.0.1", Port: 1, Options: map[string]string{"x=1 host": "evil"}}},
		{"unknown sslmode", DatabaseConfig{Host: "127.0.0.1", Port: 1, SSLMode: "sometimes"}},
		{"unparsable runtime option", DatabaseConfig{Host: "127.0.0.1", Port: 1, Options: map[string]string{"connect_timeout": "soon"}}},
		{"connection refused", DatabaseConfig{Host: "127.0.0.1", Port: 1, SSLMode: "disable"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.cfg.User, tc.cfg.Password, tc.cfg.Database = "oa", password, "oa"

			db, err := NewPostgresDB(tc.cfg, log)
			if err == nil {
				_ = db.Close()

				t.Fatal("NewPostgresDB succeeded, want an error")
			}

			if db != nil {
				t.Error("NewPostgresDB returned a handle alongside an error")
			}

			if strings.Contains(err.Error(), password) || strings.Contains(err.Error(), "Zk39") {
				t.Errorf("error leaks the password: %v", err)
			}
		})
	}
}
