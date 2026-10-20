-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Persist parsed RPM dependency/file/changelog data and the header byte
-- range so primary.xml, filelists.xml and other.xml can be generated.
ALTER TABLE rpm_packages ADD COLUMN IF NOT EXISTS header_start bigint NOT NULL DEFAULT 0;
ALTER TABLE rpm_packages ADD COLUMN IF NOT EXISTS header_end bigint NOT NULL DEFAULT 0;
ALTER TABLE rpm_packages ADD COLUMN IF NOT EXISTS rpm_extra jsonb NOT NULL DEFAULT '{}'::jsonb;
