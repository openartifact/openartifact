// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

// Cleanup is how the retention worker ranks and protects the versions of a
// format. The candidates are the live, listed package_files rows of one
// repository; the worker ranks them with
//
//	DENSE_RANK() OVER (PARTITION BY <Partition> ORDER BY <newest created_at of the
//	                   version group> DESC, <VersionExpr> DESC)
//
// so the rows that share a (Partition, VersionExpr) group (the jar and pom of a
// maven release, the sdist and wheel of a pypi release) count as one version for
// keep_last_n. Every fragment is a compile-time constant of the format, never
// user input, and sees the aliases f (package_files), v (package_versions) and
// p (packages). The zero value is the generic default.
type Cleanup struct {
	// Partition is the list of expressions that tell "the same package" apart:
	// versions are only ranked against versions of the same partition, so a
	// newer arm64 build never evicts the only amd64 one. Each element is a
	// complete SQL expression (it may contain commas). Empty means
	// p.id, f.arch, f.part.
	Partition []string
	// VersionExpr is the text expression identifying one version inside a
	// partition. Empty means v.version_key.
	VersionExpr string
	// ProtectExpr is a boolean expression; rows for which it holds still occupy
	// their rank but are never selected (npm versions a dist-tag points at, the
	// newest build of a maven snapshot). It may use window functions. Empty
	// means nothing is protected.
	ProtectExpr string
}

// PartitionExprs is Partition with the generic default applied.
func (c Cleanup) PartitionExprs() []string {
	if len(c.Partition) > 0 {
		return c.Partition
	}

	return []string{"p.id", "f.arch", "f.part"}
}

// VersionSQL is VersionExpr with the generic default applied.
func (c Cleanup) VersionSQL() string {
	if c.VersionExpr != "" {
		return c.VersionExpr
	}

	return "v.version_key"
}

// ProtectSQL is ProtectExpr with the generic default applied.
func (c Cleanup) ProtectSQL() string {
	if c.ProtectExpr != "" {
		return c.ProtectExpr
	}

	return "false"
}
