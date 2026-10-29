// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

// PostgresDB is the PostgreSQL database: the composition root that embeds one
// store per domain (see stores.go). Consumers never depend on this type but on
// the small interfaces they declare themselves; PostgresDB satisfies them
// through the promoted store methods.
type PostgresDB struct {
	*core
	*BlobRefStore
	*SettingsStore
	*AuditStore
	*ProxyCacheStore
	*WebhookStore
	*CleanupStore
	*PresignedUploadStore
	*PropertyStore
	*DeltaStore
	*DeclarativeStore
	*GPGKeyStore
	*BackupStore
	*BundleStore
	*QuarantineStore
	*SecretScanStore
	*LicenseStore
	*SBOMStore
	*VEXStore
	*VulnStore
	*ExploitDB
	*OSVDB
	*DependencyTrackStore
	*AIAnalysisStore
	*QuotaStore
	*StorageBackendStore
	*TieringStore
	*ReplicationStore
	*TrashDB
	*RepoMetadataStore
	*RepoGroupStore
	*ComponentStore
	*CatalogStore
	*RepositoryStore
	*UserStore
	*SessionStore
	*UserGroupStore
	*TokenStore
	*PermissionStore
	*SSOStore
	*ProjectStore
	*DebStore
	*RPMStore
	*APKStore
	*PacmanStore
	*MavenStore
	*NPMStore
	*TerraformStore
	*ComposerStore
	*AnsibleStore
	*NixStore
	*GoModStore
	*DockerStore
	// STORES-STRUCT
}

// newPostgresDB wires one store per domain around the shared core c.
func newPostgresDB(c *core) *PostgresDB {
	return &PostgresDB{
		core:                 c,
		BlobRefStore:         &BlobRefStore{core: c},
		SettingsStore:        &SettingsStore{core: c},
		AuditStore:           &AuditStore{core: c},
		ProxyCacheStore:      &ProxyCacheStore{core: c},
		WebhookStore:         &WebhookStore{core: c},
		CleanupStore:         &CleanupStore{core: c},
		PresignedUploadStore: &PresignedUploadStore{core: c},
		PropertyStore:        &PropertyStore{core: c},
		DeltaStore:           &DeltaStore{core: c},
		DeclarativeStore:     &DeclarativeStore{core: c},
		GPGKeyStore:          &GPGKeyStore{core: c},
		BackupStore:          &BackupStore{core: c},
		BundleStore:          &BundleStore{core: c},
		QuarantineStore:      &QuarantineStore{core: c},
		SecretScanStore:      &SecretScanStore{core: c},
		LicenseStore:         &LicenseStore{core: c},
		SBOMStore:            &SBOMStore{core: c},
		VEXStore:             &VEXStore{core: c},
		VulnStore:            &VulnStore{core: c},
		ExploitDB:            &ExploitDB{core: c},
		OSVDB:                &OSVDB{core: c},
		DependencyTrackStore: &DependencyTrackStore{core: c},
		AIAnalysisStore:      &AIAnalysisStore{core: c},
		QuotaStore:           &QuotaStore{core: c},
		StorageBackendStore:  &StorageBackendStore{core: c},
		TieringStore:         &TieringStore{core: c},
		ReplicationStore:     &ReplicationStore{core: c},
		TrashDB:              &TrashDB{core: c},
		RepoMetadataStore:    &RepoMetadataStore{core: c},
		RepoGroupStore:       &RepoGroupStore{core: c},
		ComponentStore:       &ComponentStore{core: c},
		CatalogStore:         &CatalogStore{core: c},
		RepositoryStore:      &RepositoryStore{core: c},
		UserStore:            &UserStore{core: c},
		SessionStore:         &SessionStore{core: c},
		UserGroupStore:       &UserGroupStore{core: c},
		TokenStore:           &TokenStore{core: c},
		PermissionStore:      &PermissionStore{core: c},
		SSOStore:             &SSOStore{core: c},
		ProjectStore:         &ProjectStore{core: c},
		DebStore:             &DebStore{core: c},
		RPMStore:             &RPMStore{core: c},
		APKStore:             &APKStore{core: c},
		PacmanStore:          &PacmanStore{core: c},
		MavenStore:           &MavenStore{core: c},
		NPMStore:             &NPMStore{core: c},
		TerraformStore:       &TerraformStore{core: c},
		ComposerStore:        &ComposerStore{core: c},
		AnsibleStore:         &AnsibleStore{core: c},
		NixStore:             &NixStore{core: c},
		GoModStore:           &GoModStore{core: c},
		DockerStore:          &DockerStore{core: c},
		// STORES-INIT
	}
}
