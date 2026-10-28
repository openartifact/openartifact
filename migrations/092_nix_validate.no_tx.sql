-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Checks the existing repositories rows against the widened format list without
-- blocking writes (SHARE UPDATE EXCLUSIVE). A no-op once validated.
ALTER TABLE repositories VALIDATE CONSTRAINT chk_repo_format;
