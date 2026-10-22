-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- AI analyses keep the analysed package's display identity. artifact_refs only
-- tracks (repository_id, artifact_path, format), so the name and version the
-- UI lists next to each analysis had no home and always came back empty.
-- Metadata-only ADD COLUMN with constant defaults; existing rows keep ''.
ALTER TABLE ai_results ADD COLUMN artifact_name text DEFAULT '' NOT NULL;
ALTER TABLE ai_results ADD COLUMN artifact_version text DEFAULT '' NOT NULL;
