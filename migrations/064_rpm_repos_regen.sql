-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- primary.xml of earlier versions wrote each package's download path as
-- <Location>, which dnf cannot read, and omitted sizes dnf checks. The
-- metadata worker only regenerates repositories flagged metadata_stale, and
-- that flag is cleared after every regeneration, so existing repositories kept
-- the old documents until their next upload. Flag every hosted rpm repository
-- once; the worker regenerates the flagged ones at startup.
SET LOCAL lock_timeout = '5s';

UPDATE rpm_repos x
SET metadata_stale = true
FROM repositories r
WHERE r.id = x.repository_id
  AND r.repo_type = 'hosted'
  AND NOT x.metadata_stale;
