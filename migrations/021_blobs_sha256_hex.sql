-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- blobs.sha256 is the content address every upsert conflicts on. OpenArtifact
-- <= 0.4.1 inserted Go module zips with sha256 = '', so every zip shared one
-- row. Reject anything that is not 64 lowercase hex characters from now on.
-- NOT VALID: new and updated rows are checked, legacy rows are not scanned, so
-- the Go module repair can still find and replace them (it inserts a proper row,
-- relinks the modules and deletes the old row; it never UPDATEs the old one).
SET LOCAL lock_timeout = '10s';

ALTER TABLE blobs
    ADD CONSTRAINT blobs_sha256_hex CHECK (sha256 ~ '^[0-9a-f]{64}$') NOT VALID;
