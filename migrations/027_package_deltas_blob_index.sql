-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Delta patch blobs are now reference-counted (blobRefSpecs): the orphan scan
-- and SafeDeleteBlob look package_deltas up by delta_sha256. package_deltas is
-- a small cache table (pruned per repository), so a plain build is fine.
CREATE INDEX IF NOT EXISTS idx_package_deltas_delta_sha256 ON package_deltas (delta_sha256);
