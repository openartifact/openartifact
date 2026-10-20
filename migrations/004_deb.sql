-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Debian: persist the full control stanza so generated Packages entries carry
-- Depends, Pre-Depends, Maintainer, Section, Priority, etc.
ALTER TABLE deb_packages ADD COLUMN IF NOT EXISTS control_fields jsonb NOT NULL DEFAULT '{}'::jsonb;
