-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Checks the existing audit_logs rows against the widened action list without
-- blocking inserts (SHARE UPDATE EXCLUSIVE). A no-op once validated.
ALTER TABLE audit_logs VALIDATE CONSTRAINT chk_audit_action;
