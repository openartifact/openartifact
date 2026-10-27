-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Arch Linux pacman repositories (docs/formats/pacman.md): the "pacman"
-- repository format and its package table.
--
-- Rolling-safe: the repositories.format constraint is widened in one statement
-- pair (a superset of the old list, added NOT VALID, so replicas of the previous
-- version keep working and no table scan runs under the lock; the existing rows
-- are validated in 090_pacman_validate.no_tx.sql), and the package table is
-- new, so no existing table is rewritten. Its live-row indexes are
-- created together with the empty table.

SET LOCAL lock_timeout = '10s';

ALTER TABLE repositories DROP CONSTRAINT chk_repo_format;
ALTER TABLE repositories ADD CONSTRAINT chk_repo_format CHECK (format IN (
    'rpm', 'deb', 'maven', 'npm', 'docker', 'helm', 'pypi', 'nuget', 'go', 'cargo', 'rubygems', 'raw',
    'conda', 'conan', 'ivy', 'sbt', 'terraform', 'apk', 'pacman')) NOT VALID;

COMMENT ON COLUMN repositories.format IS 'Package format: rpm, deb, maven, npm, docker, helm, pypi, nuget, go, cargo, rubygems, raw, conda, conan, ivy, sbt, terraform, apk, pacman. Constrained by chk_repo_format.';

-- One row per package file. A repository database lists the newest version of
-- each package name; older versions stay downloadable by file name until they
-- are cleaned up.
CREATE TABLE pacman_packages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    name character varying(255) NOT NULL,
    base character varying(255) NOT NULL,
    version character varying(255) NOT NULL,
    arch character varying(32) NOT NULL,
    repo_arch character varying(32) NOT NULL,
    filename character varying(600) NOT NULL,
    blob_id uuid NOT NULL,
    md5 character varying(32) NOT NULL,
    description text DEFAULT '' NOT NULL,
    url text DEFAULT '' NOT NULL,
    packager text DEFAULT '' NOT NULL,
    build_date bigint DEFAULT 0 NOT NULL,
    installed_size bigint DEFAULT 0 NOT NULL,
    licenses text[] DEFAULT '{}' NOT NULL,
    package_groups text[] DEFAULT '{}' NOT NULL,
    replaces text[] DEFAULT '{}' NOT NULL,
    conflicts text[] DEFAULT '{}' NOT NULL,
    provides text[] DEFAULT '{}' NOT NULL,
    depends text[] DEFAULT '{}' NOT NULL,
    optdepends text[] DEFAULT '{}' NOT NULL,
    makedepends text[] DEFAULT '{}' NOT NULL,
    checkdepends text[] DEFAULT '{}' NOT NULL,
    file_list text,
    signature text DEFAULT '' NOT NULL,
    signature_source character varying(8) DEFAULT '' NOT NULL,
    signature_key character varying(64) DEFAULT '' NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by text,
    CONSTRAINT pacman_packages_pkey PRIMARY KEY (id),
    CONSTRAINT pacman_packages_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES blobs(id) ON DELETE RESTRICT,
    CONSTRAINT pacman_packages_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE,
    CONSTRAINT pacman_packages_signature_source_check CHECK (signature_source IN ('', 'uploader', 'server'))
);

-- The coordinates fix the file name up to its compression, so one unique index
-- also keeps a name, version and architecture from existing as .zst and .xz.
CREATE UNIQUE INDEX pacman_packages_live_coords_key ON pacman_packages (repository_id, name, version, arch) WHERE deleted_at IS NULL;
CREATE INDEX idx_pacman_packages_filename ON pacman_packages (repository_id, filename) WHERE deleted_at IS NULL;
CREATE INDEX idx_pacman_packages_repo_created ON pacman_packages (repository_id, created_at DESC, id) WHERE deleted_at IS NULL;
CREATE INDEX idx_pacman_packages_blob_id ON pacman_packages (blob_id);
CREATE INDEX idx_pacman_packages_deleted ON pacman_packages (deleted_at) WHERE deleted_at IS NOT NULL;

CREATE TRIGGER update_pacman_packages_updated_at
    BEFORE UPDATE ON pacman_packages
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

COMMENT ON TABLE pacman_packages IS 'Arch Linux pacman packages: one .pkg.tar.{zst,xz,gz} file per name/version/arch.';
COMMENT ON COLUMN pacman_packages.arch IS 'Architecture of the package ("any" or a CPU architecture), from its .PKGINFO.';
COMMENT ON COLUMN pacman_packages.repo_arch IS 'Architecture directory (os/ARCH) the package was uploaded to; an "any" package is listed in the database of every architecture of the repository.';
COMMENT ON COLUMN pacman_packages.file_list IS 'Newline-separated file list for the files database; NULL until it is read from the package blob (the metadata worker fills it).';
COMMENT ON COLUMN pacman_packages.signature IS 'Base64 of the binary detached OpenPGP signature of the package file; empty when unsigned.';
COMMENT ON COLUMN pacman_packages.signature_source IS 'Who made the signature: uploader (a .sig upload), server (the repository signing key, made by the metadata worker) or empty.';
COMMENT ON COLUMN pacman_packages.signature_key IS 'Fingerprint of the repository key that made a server signature; the worker re-signs when the repository key changes.';

-- Quota reservations (migration 061): one reserved artifact per row, sized by
-- the row's blob, exactly as the other single-blob package tables.
CREATE FUNCTION quota_pending_pacman_packages() RETURNS trigger LANGUAGE plpgsql AS $body$
BEGIN
    IF TG_OP IN ('DELETE', 'UPDATE') THEN
        PERFORM quota_pending_add(OLD.repository_id, quota_blob_size(OLD.blob_id), 1);
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        PERFORM quota_pending_take(NEW.repository_id, quota_blob_size(NEW.blob_id), 1, false);
    END IF;
    RETURN NULL;
END
$body$;

CREATE TRIGGER quota_pending_ins AFTER INSERT ON pacman_packages FOR EACH ROW EXECUTE FUNCTION quota_pending_pacman_packages();
CREATE TRIGGER quota_pending_del AFTER DELETE ON pacman_packages FOR EACH ROW EXECUTE FUNCTION quota_pending_pacman_packages();
CREATE TRIGGER quota_pending_upd AFTER UPDATE OF blob_id ON pacman_packages FOR EACH ROW WHEN (OLD.blob_id IS DISTINCT FROM NEW.blob_id) EXECUTE FUNCTION quota_pending_pacman_packages();

-- Recovery journal (migration 068; keep in step with internal/recovery/tables.go).
CREATE TRIGGER recovery_journal AFTER INSERT OR UPDATE OR DELETE ON pacman_packages
    FOR EACH ROW EXECUTE FUNCTION recovery_trg_id();
