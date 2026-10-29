// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package replication

// verifyLockKey names the cluster-wide advisory lock (hashed by Postgres) the
// replica working on a verify pass holds, so passes of different replicas
// never overlap.
const verifyLockKey = "openartifact:replication-verify"
