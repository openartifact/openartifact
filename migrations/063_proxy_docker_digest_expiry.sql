-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Docker proxy entries addressed by digest were cached with a 100-year expiry,
-- so the proxy cache worker never evicted them. They now expire after
-- workers.proxyDockerDigestTtl without a pull (a pull renews them). Give the
-- entries already cached the default 30 days from now: ones in use are renewed
-- by their next pull, the rest are evicted by the worker. One-shot update of
-- the open-ended rows only; new entries get their expiry when they are stored.
SET LOCAL lock_timeout = '5s';

UPDATE proxy_cache_entries e
SET expires_at = now() + interval '30 days'
FROM repositories r
WHERE r.id = e.repository_id
  AND r.format = 'docker'
  AND e.blob_key IS NOT NULL
  AND e.expires_at > now() + interval '50 years'
  AND e.cache_key ~ '/(manifests|blobs)/(sha256:[a-f0-9]{64}|sha512:[a-f0-9]{128})$';
