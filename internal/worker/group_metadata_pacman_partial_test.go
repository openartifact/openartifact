// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/pkg/logger"
)

func TestBuildPacmanKeepsKnownArchWhenProxyUnreadable(t *testing.T) {
	t.Parallel()

	log, err := logger.New("error", "console", "stderr")
	require.NoError(t, err)

	remote := &models.Repository{
		ID:          "p",
		Name:        "p",
		Format:      "pacman",
		RepoType:    repotype.Proxy,
		UpstreamURL: "https://mirror.example/extra",
	}
	up := &apkUpstream{
		files:   map[string][]byte{},
		failing: map[string]bool{"p|os/x86_64/extra.db": true},
	}

	j := &groupJob{
		w:       &GroupMetadataWorker{opener: up, log: log},
		group:   &models.Repository{ID: "grp", Name: "grp", Format: "pacman", RepoType: repotype.Group},
		members: []*models.Repository{remote},
		blobs:   make(map[string][]byte),
		owners:  make(map[string]groupOwner),
		keep:    make(map[string]struct{}),
	}

	// The generation is partial, so every known architecture without a result
	// keeps its previous rows.
	require.NoError(t, j.buildPacman(context.Background(), []string{"x86_64", "aarch64"}, ""))

	assert.True(t, j.partial)
	assert.Equal(t, []string{"aarch64", "x86_64"}, j.dists)
	assert.Equal(t, []string{"os/aarch64/", "os/x86_64/"}, j.keptPrefixes())
	assert.Empty(t, j.blobs, "nothing is rendered for the unreadable architectures, their rows stay")
}

func TestClaimEmptyChecksumAdoptsNewOwner(t *testing.T) {
	t.Parallel()

	j := &groupJob{owners: make(map[string]groupOwner)}

	assert.True(t, j.claim("a/x.pkg", "m1", ""))
	assert.True(t, j.claim("a/x.pkg", "m2", "sum"))
	assert.Equal(t, groupOwner{member: "m2", sha256: "sum"}, j.owners["a/x.pkg"])
	assert.False(t, j.claim("a/x.pkg", "m3", "other"))
}
