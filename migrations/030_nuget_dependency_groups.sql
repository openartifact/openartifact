-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- The registration index lists every version's dependency groups. They live in
-- the .nupkg's nuspec, so serving the index meant reading each package from
-- the blob store. Persist them at publish time instead.
--
-- NULL means "not recorded yet" (rows published before this migration, or
-- restored from a backup): the server derives the value from the package on
-- first use and stores it. '[]' means the package declares no dependencies.
ALTER TABLE nuget_packages ADD COLUMN IF NOT EXISTS dependency_groups jsonb NULL;
