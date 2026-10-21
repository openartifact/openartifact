-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Proxy cache entries reference blobs by sha256 (blob_key) and count as blob
-- references (BlobReferenceCount); index the lookup used by SafeDeleteBlob and
-- the orphan collector.
CREATE INDEX IF NOT EXISTS idx_proxy_cache_blob_key
    ON proxy_cache_entries (blob_key)
    WHERE blob_key IS NOT NULL;
