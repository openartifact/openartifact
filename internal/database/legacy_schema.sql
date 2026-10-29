-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- TEMPORARY: the per-format tables of releases before 0.15, kept only so sqlc
-- can type-check the queries and stores that have not been ported to the
-- generic package tables yet. It is not a migration and is never applied. Each
-- format port deletes its tables here; the file is removed with the last one.
--
--
-- Name: ansible_collections; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.ansible_collections (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    namespace character varying(64) NOT NULL,
    name character varying(64) NOT NULL,
    version character varying(128) NOT NULL,
    filename character varying(255) NOT NULL,
    blob_id uuid NOT NULL,
    dependencies jsonb DEFAULT '{}'::jsonb NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    requires_ansible character varying(255) DEFAULT ''::character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by text
);


--
-- Name: TABLE ansible_collections; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.ansible_collections IS 'Ansible collections: one ns-name-version.tar.gz artifact per namespace, name and version.';


--
-- Name: COLUMN ansible_collections.dependencies; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.ansible_collections.dependencies IS 'Collection dependencies from MANIFEST.json: {"namespace.name": "version requirement"}.';


--
-- Name: COLUMN ansible_collections.metadata; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.ansible_collections.metadata IS 'The collection_info object of MANIFEST.json (authors, description, license, tags, links); served as the metadata of the version detail.';


--
-- Name: COLUMN ansible_collections.requires_ansible; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.ansible_collections.requires_ansible IS 'requires_ansible of meta/runtime.yml, empty when the collection has none.';


--
-- Name: apk_packages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.apk_packages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    branch character varying(64) NOT NULL,
    repo character varying(64) NOT NULL,
    arch character varying(32) NOT NULL,
    pkg_arch character varying(32) NOT NULL,
    name character varying(128) NOT NULL,
    version character varying(128) NOT NULL,
    checksum text NOT NULL,
    data_sha256 text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    url text DEFAULT ''::text NOT NULL,
    license text DEFAULT ''::text NOT NULL,
    origin text DEFAULT ''::text NOT NULL,
    maintainer text DEFAULT ''::text NOT NULL,
    commit text DEFAULT ''::text NOT NULL,
    build_time bigint DEFAULT 0 NOT NULL,
    installed_size bigint DEFAULT 0 NOT NULL,
    depends text[] DEFAULT '{}'::text[] NOT NULL,
    provides text[] DEFAULT '{}'::text[] NOT NULL,
    install_if text[] DEFAULT '{}'::text[] NOT NULL,
    provider_priority integer DEFAULT 0 NOT NULL,
    download_count bigint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    blob_id uuid NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by text
);


--
-- Name: TABLE apk_packages; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.apk_packages IS 'Alpine packages: one .apk per branch/repo/arch/name/version.';


--
-- Name: COLUMN apk_packages.arch; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.apk_packages.arch IS 'Architecture directory of the mirror tree.';


--
-- Name: COLUMN apk_packages.pkg_arch; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.apk_packages.pkg_arch IS 'Architecture of the package itself (PKGINFO arch, APKINDEX A:); noarch inside an x86_64 directory.';


--
-- Name: COLUMN apk_packages.checksum; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.apk_packages.checksum IS 'APKINDEX C: value: Q1 + base64 of the SHA-1 of the control segment.';


--
-- Name: COLUMN apk_packages.data_sha256; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.apk_packages.data_sha256 IS 'Lowercase hex SHA-256 of the data segment (PKGINFO datahash).';


--
-- Name: cargo_crates; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.cargo_crates (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    name character varying(255) NOT NULL,
    version character varying(100) NOT NULL,
    description text,
    homepage text,
    documentation text,
    repository text,
    license text,
    keywords text[],
    categories text[],
    authors text[],
    readme text,
    metadata_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    yanked boolean DEFAULT false NOT NULL,
    download_count bigint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    blob_id uuid NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by text
);


--
-- Name: TABLE cargo_crates; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.cargo_crates IS 'Rust/Cargo packages.';


--
-- Name: COLUMN cargo_crates.download_count; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.cargo_crates.download_count IS 'Total downloads; BIGINT to avoid overflow on popular crates';


--
-- Name: composer_packages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.composer_packages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    name character varying(255) NOT NULL,
    version character varying(128) NOT NULL,
    version_normalized character varying(128) NOT NULL,
    blob_id uuid NOT NULL,
    sha1 character varying(40) NOT NULL,
    composer_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by text
);


--
-- Name: TABLE composer_packages; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.composer_packages IS 'Composer packages: one dist zip per package name and normalised version.';


--
-- Name: COLUMN composer_packages.version; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.composer_packages.version IS 'The version as published, e.g. v1.2.3 or dev-main.';


--
-- Name: COLUMN composer_packages.version_normalized; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.composer_packages.version_normalized IS 'Composer VersionParser::normalize of version, e.g. 1.2.3.0.';


--
-- Name: COLUMN composer_packages.sha1; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.composer_packages.sha1 IS 'Hex sha1 of the dist zip (Composer dist.shasum).';


--
-- Name: COLUMN composer_packages.composer_json; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.composer_packages.composer_json IS 'The composer.json of the package as uploaded.';


--
-- Name: conan_packages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.conan_packages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    name character varying(255) NOT NULL,
    version character varying(100) NOT NULL,
    user_channel character varying(255) DEFAULT '_/_'::character varying NOT NULL,
    recipe_revision character varying(64) DEFAULT '0'::character varying NOT NULL,
    conanfile text,
    conaninfo text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    blob_id uuid,
    deleted_at timestamp with time zone,
    deleted_by text,
    package_id character varying(64) DEFAULT ''::character varying NOT NULL,
    package_revision character varying(64) DEFAULT ''::character varying NOT NULL,
    filename text DEFAULT ''::text NOT NULL,
    md5 character varying(32) DEFAULT ''::character varying NOT NULL,
    sha1 character varying(40) DEFAULT ''::character varying NOT NULL
);


--
-- Name: COLUMN conan_packages.package_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.conan_packages.package_id IS 'Package id (binary configuration hash); empty for a file of the recipe revision itself.';


--
-- Name: COLUMN conan_packages.package_revision; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.conan_packages.package_revision IS 'Package revision; empty for a file of the recipe revision itself.';


--
-- Name: COLUMN conan_packages.filename; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.conan_packages.filename IS 'Path of the stored file inside its recipe or package revision (conanfile.py, conan_package.tgz, metadata/sign/...); empty for rows stored before migration 019.';


--
-- Name: COLUMN conan_packages.md5; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.conan_packages.md5 IS 'Lowercase hex MD5 of the file, as Conan 1 snapshots report it. Empty until computed.';


--
-- Name: COLUMN conan_packages.sha1; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.conan_packages.sha1 IS 'Lowercase hex SHA-1 of the file, for Conan checksum deploy (X-Checksum-Sha1). Empty for files stored before it was tracked.';


--
-- Name: conda_packages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.conda_packages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    name character varying(255) NOT NULL,
    version character varying(100) NOT NULL,
    build_string character varying(255) DEFAULT ''::character varying NOT NULL,
    platform character varying(50) DEFAULT 'noarch'::character varying NOT NULL,
    depends text[] DEFAULT '{}'::text[] NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    blob_id uuid NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by text,
    extension text DEFAULT '.tar.bz2'::text NOT NULL,
    build_number integer DEFAULT 0 NOT NULL,
    md5 character varying(32) DEFAULT ''::character varying NOT NULL,
    timestamp_ms bigint DEFAULT 0 NOT NULL,
    index_extra jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT chk_conda_packages_extension CHECK ((extension = ANY (ARRAY['.tar.bz2'::text, '.conda'::text])))
);


--
-- Name: COLUMN conda_packages.extension; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.conda_packages.extension IS 'Package file extension: .tar.bz2 (legacy) or .conda (zip with zstd members).';


--
-- Name: COLUMN conda_packages.timestamp_ms; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.conda_packages.timestamp_ms IS 'info/index.json timestamp in milliseconds since the epoch; 0 when the package declares none.';


--
-- Name: COLUMN conda_packages.index_extra; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.conda_packages.index_extra IS 'Remaining info/index.json fields emitted into repodata.json (constrains, license, noarch, ...).';


--
-- Name: deb_packages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.deb_packages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    distribution_id uuid NOT NULL,
    component_id uuid NOT NULL,
    blob_id uuid NOT NULL,
    name text NOT NULL,
    version text NOT NULL,
    arch text NOT NULL,
    summary text,
    description text,
    license text,
    maintainer text,
    homepage text,
    filename text,
    installed_size bigint,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    control_fields jsonb DEFAULT '{}'::jsonb NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by text
);


--
-- Name: TABLE deb_packages; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.deb_packages IS 'Debian packages. Canonical table for Debian format. Scoped to distribution+component for proper Debian model. blob_id FK to global blobs table.';


--
-- Name: gomod_modules; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.gomod_modules (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    module_path text NOT NULL,
    version character varying(100) NOT NULL,
    go_mod_content text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    zip_blob_id uuid,
    mod_blob_id uuid,
    deleted_at timestamp with time zone,
    deleted_by text
);


--
-- Name: TABLE gomod_modules; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.gomod_modules IS 'Go module proxy packages';


--
-- Name: helm_charts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.helm_charts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    name character varying(255) NOT NULL,
    version character varying(100) NOT NULL,
    app_version character varying(100),
    description text,
    keywords text[],
    home text,
    sources text[],
    maintainers jsonb,
    icon text,
    api_version character varying(20) DEFAULT 'v2'::character varying NOT NULL,
    type character varying(20) DEFAULT 'application'::character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    blob_id uuid NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by text
);


--
-- Name: TABLE helm_charts; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.helm_charts IS 'Helm chart packages';


--
-- Name: ivy_artifacts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.ivy_artifacts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    organisation character varying(255) NOT NULL,
    module character varying(255) NOT NULL,
    revision character varying(100) NOT NULL,
    artifact_name character varying(255) NOT NULL,
    artifact_type character varying(50) DEFAULT 'jar'::character varying NOT NULL,
    artifact_ext character varying(50) DEFAULT 'jar'::character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    blob_id uuid NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by text
);


--
-- Name: maven_artifacts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.maven_artifacts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    group_id text NOT NULL,
    artifact_id_coord text NOT NULL,
    version text NOT NULL,
    classifier text DEFAULT ''::text NOT NULL,
    extension text NOT NULL,
    snapshot_timestamp text,
    snapshot_build_number integer DEFAULT 0 NOT NULL,
    is_snapshot boolean DEFAULT false NOT NULL,
    repo_path text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    blob_id uuid NOT NULL,
    md5 text DEFAULT ''::text NOT NULL,
    sha1 text DEFAULT ''::text NOT NULL,
    sha512 text DEFAULT ''::text NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by text
);


--
-- Name: COLUMN maven_artifacts.snapshot_timestamp; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.maven_artifacts.snapshot_timestamp IS 'Snapshot timestamp string (e.g. "20240101.120000"). NULL for release artifacts.';


--
-- Name: COLUMN maven_artifacts.snapshot_build_number; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.maven_artifacts.snapshot_build_number IS 'Snapshot build number. 0 = not a snapshot or build number not tracked. NULL would be cleaner but 0 is kept for backward compatibility.';


--
-- Name: nix_paths; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.nix_paths (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    store_hash character varying(32) NOT NULL,
    name character varying(211) NOT NULL,
    nar_hash text NOT NULL,
    nar_size bigint NOT NULL,
    nar_file text NOT NULL,
    compression character varying(16) NOT NULL,
    refs text[] DEFAULT '{}'::text[] NOT NULL,
    deriver text DEFAULT ''::text NOT NULL,
    ca text DEFAULT ''::text NOT NULL,
    signatures text[] DEFAULT '{}'::text[] NOT NULL,
    download_count bigint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    blob_id uuid NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by text
);


--
-- Name: TABLE nix_paths; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.nix_paths IS 'Nix store paths: one narinfo and NAR file per store hash.';


--
-- Name: COLUMN nix_paths.store_hash; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.nix_paths.store_hash IS 'The 32 character nixbase32 hash of the store path (the narinfo file name).';


--
-- Name: COLUMN nix_paths.name; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.nix_paths.name IS 'Store path name without the hash, for example hello-2.12.';


--
-- Name: COLUMN nix_paths.nar_hash; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.nix_paths.nar_hash IS 'narinfo NarHash: sha256:<nixbase32> of the uncompressed NAR.';


--
-- Name: COLUMN nix_paths.nar_file; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.nix_paths.nar_file IS 'File name below nar/: <nixbase32 sha256 of the file>.nar.<ext>; several paths may share one.';


--
-- Name: COLUMN nix_paths.refs; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.nix_paths.refs IS 'narinfo References: base names of the store paths referenced.';


--
-- Name: COLUMN nix_paths.signatures; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.nix_paths.signatures IS 'narinfo Sig lines supplied by the uploader (name:base64).';


--
-- Name: npm_dist_tags; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.npm_dist_tags (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    package_id uuid NOT NULL,
    tag text NOT NULL,
    version text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE npm_dist_tags; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.npm_dist_tags IS 'Distribution tags for npm packages, e.g. latest, next, beta';


--
-- Name: COLUMN npm_dist_tags.tag; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.npm_dist_tags.tag IS 'Tag name, e.g. latest';


--
-- Name: COLUMN npm_dist_tags.version; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.npm_dist_tags.version IS 'Version string the tag currently points to';


--
-- Name: npm_packages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.npm_packages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    scope text DEFAULT ''::text NOT NULL,
    name text NOT NULL,
    full_name text NOT NULL,
    latest_version text DEFAULT ''::text NOT NULL,
    packument_stale boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE npm_packages; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.npm_packages IS 'One row per npm package (scoped or unscoped) within a repository';


--
-- Name: COLUMN npm_packages.scope; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.npm_packages.scope IS 'Scope prefix without @, empty string for unscoped packages';


--
-- Name: COLUMN npm_packages.full_name; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.npm_packages.full_name IS 'Full package name including scope, e.g. @scope/name or name';


--
-- Name: COLUMN npm_packages.packument_stale; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.npm_packages.packument_stale IS 'True when latest_version denormalization needs refresh';


--
-- Name: npm_versions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.npm_versions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    package_id uuid NOT NULL,
    version text NOT NULL,
    manifest_json jsonb NOT NULL,
    integrity text NOT NULL,
    shasum text NOT NULL,
    tarball_filename text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    blob_id uuid NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by text
);


--
-- Name: TABLE npm_versions; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.npm_versions IS 'One row per published version of an npm package';


--
-- Name: COLUMN npm_versions.manifest_json; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.npm_versions.manifest_json IS 'Full package.json manifest as stored in the packument';


--
-- Name: COLUMN npm_versions.integrity; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.npm_versions.integrity IS 'Subresource Integrity string, e.g. sha512-<base64>';


--
-- Name: COLUMN npm_versions.updated_at; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.npm_versions.updated_at IS 'Auto-updated by trigger on every UPDATE.';


--
-- Name: nuget_packages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.nuget_packages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    package_id character varying(255) NOT NULL,
    version character varying(100) NOT NULL,
    title character varying(255),
    description text,
    summary text,
    authors text,
    owners text,
    tags text,
    project_url text,
    license_url text,
    icon_url text,
    require_license_acceptance boolean DEFAULT false NOT NULL,
    is_prerelease boolean DEFAULT false NOT NULL,
    min_client_version character varying(50),
    download_count bigint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    blob_id uuid NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by text,
    dependency_groups jsonb
);


--
-- Name: TABLE nuget_packages; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.nuget_packages IS 'NuGet .NET packages';


--
-- Name: COLUMN nuget_packages.download_count; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.nuget_packages.download_count IS 'Total downloads; BIGINT to avoid overflow on popular packages';


--
-- Name: pacman_packages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pacman_packages (
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
    description text DEFAULT ''::text NOT NULL,
    url text DEFAULT ''::text NOT NULL,
    packager text DEFAULT ''::text NOT NULL,
    build_date bigint DEFAULT 0 NOT NULL,
    installed_size bigint DEFAULT 0 NOT NULL,
    licenses text[] DEFAULT '{}'::text[] NOT NULL,
    package_groups text[] DEFAULT '{}'::text[] NOT NULL,
    replaces text[] DEFAULT '{}'::text[] NOT NULL,
    conflicts text[] DEFAULT '{}'::text[] NOT NULL,
    provides text[] DEFAULT '{}'::text[] NOT NULL,
    depends text[] DEFAULT '{}'::text[] NOT NULL,
    optdepends text[] DEFAULT '{}'::text[] NOT NULL,
    makedepends text[] DEFAULT '{}'::text[] NOT NULL,
    checkdepends text[] DEFAULT '{}'::text[] NOT NULL,
    file_list text,
    signature text DEFAULT ''::text NOT NULL,
    signature_source character varying(8) DEFAULT ''::character varying NOT NULL,
    signature_key character varying(64) DEFAULT ''::character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by text,
    CONSTRAINT pacman_packages_signature_source_check CHECK (((signature_source)::text = ANY ((ARRAY[''::character varying, 'uploader'::character varying, 'server'::character varying])::text[])))
);


--
-- Name: TABLE pacman_packages; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.pacman_packages IS 'Arch Linux pacman packages: one .pkg.tar.{zst,xz,gz} file per name/version/arch.';


--
-- Name: COLUMN pacman_packages.arch; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.pacman_packages.arch IS 'Architecture of the package ("any" or a CPU architecture), from its .PKGINFO.';


--
-- Name: COLUMN pacman_packages.repo_arch; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.pacman_packages.repo_arch IS 'Architecture directory (os/ARCH) the package was uploaded to; an "any" package is listed in the database of every architecture of the repository.';


--
-- Name: COLUMN pacman_packages.file_list; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.pacman_packages.file_list IS 'Newline-separated file list for the files database; NULL until it is read from the package blob (the metadata worker fills it).';


--
-- Name: COLUMN pacman_packages.signature; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.pacman_packages.signature IS 'Base64 of the binary detached OpenPGP signature of the package file; empty when unsigned.';


--
-- Name: COLUMN pacman_packages.signature_source; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.pacman_packages.signature_source IS 'Who made the signature: uploader (a .sig upload), server (the repository signing key, made by the metadata worker) or empty.';


--
-- Name: COLUMN pacman_packages.signature_key; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.pacman_packages.signature_key IS 'Fingerprint of the repository key that made a server signature; the worker re-signs when the repository key changes.';


--
-- Name: pypi_packages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pypi_packages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    name character varying(255) NOT NULL,
    version character varying(100) NOT NULL,
    filename character varying(500) NOT NULL,
    package_type character varying(20) NOT NULL,
    python_version character varying(50),
    requires_python character varying(100),
    summary text,
    author character varying(255),
    author_email character varying(255),
    license text,
    keywords text,
    classifiers text[],
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    blob_id uuid NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by text
);


--
-- Name: TABLE pypi_packages; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.pypi_packages IS 'PyPI Python packages';


--
-- Name: raw_artifacts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.raw_artifacts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    path text NOT NULL,
    content_type character varying(255) DEFAULT 'application/octet-stream'::character varying NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    blob_id uuid NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by text
);


--
-- Name: rpm_packages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.rpm_packages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    blob_id uuid NOT NULL,
    name text NOT NULL,
    version text NOT NULL,
    release text DEFAULT ''::text NOT NULL,
    epoch integer DEFAULT 0 NOT NULL,
    arch text NOT NULL,
    summary text,
    description text,
    license text,
    url text,
    vendor text,
    group_name text,
    packager text,
    source_rpm text,
    build_host text,
    build_time timestamp with time zone,
    filename text,
    package_size bigint,
    installed_size bigint,
    archive_size bigint,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    header_start bigint DEFAULT 0 NOT NULL,
    header_end bigint DEFAULT 0 NOT NULL,
    rpm_extra jsonb DEFAULT '{}'::jsonb NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by text
);


--
-- Name: TABLE rpm_packages; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.rpm_packages IS 'RPM packages. Canonical table for RPM format. blob_id FK to global blobs table.';


--
-- Name: rubygems_gems; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.rubygems_gems (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    name character varying(255) NOT NULL,
    version character varying(100) NOT NULL,
    platform character varying(50) DEFAULT 'ruby'::character varying NOT NULL,
    description text,
    summary text,
    homepage text,
    authors text[],
    licenses text[],
    dependencies jsonb DEFAULT '[]'::jsonb NOT NULL,
    metadata_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    yanked boolean DEFAULT false NOT NULL,
    download_count bigint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    blob_id uuid NOT NULL,
    size bigint DEFAULT 0 NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by text
);


--
-- Name: COLUMN rubygems_gems.download_count; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.rubygems_gems.download_count IS 'Total downloads; BIGINT to avoid overflow on popular gems';


--
-- Name: sbt_artifacts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sbt_artifacts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    group_id character varying(255) NOT NULL,
    artifact_id character varying(255) NOT NULL,
    version character varying(100) NOT NULL,
    scala_version character varying(20) DEFAULT ''::character varying NOT NULL,
    filename character varying(255) NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    blob_id uuid NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by text
);


--
-- Name: terraform_modules; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.terraform_modules (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    name character varying(64) NOT NULL,
    system character varying(64) NOT NULL,
    version character varying(100) NOT NULL,
    archive_ext character varying(8) NOT NULL,
    download_count bigint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    blob_id uuid NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by text,
    CONSTRAINT terraform_modules_archive_ext_check CHECK (((archive_ext)::text = ANY ((ARRAY['zip'::character varying, 'tar.gz'::character varying])::text[])))
);


--
-- Name: TABLE terraform_modules; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.terraform_modules IS 'Terraform/OpenTofu registry modules: one archive (zip or tar.gz) per name/system/version.';


--
-- Name: COLUMN terraform_modules.archive_ext; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.terraform_modules.archive_ext IS 'Archive kind as stored: zip or tar.gz; it is the extension of the download URL.';


--
-- Name: terraform_providers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.terraform_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    type character varying(64) NOT NULL,
    version character varying(100) NOT NULL,
    os character varying(32) NOT NULL,
    arch character varying(32) NOT NULL,
    protocols text[] DEFAULT '{5.0}'::text[] NOT NULL,
    download_count bigint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    blob_id uuid NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by text
);


--
-- Name: TABLE terraform_providers; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.terraform_providers IS 'Terraform/OpenTofu registry providers: one platform zip per type/version/os/arch.';


--
-- Name: COLUMN terraform_providers.protocols; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.terraform_providers.protocols IS 'Plugin protocol versions the provider speaks, as the registry reports them (for example 5.0, 6.0).';


--
-- Name: ansible_collections ansible_collections_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ansible_collections
    ADD CONSTRAINT ansible_collections_pkey PRIMARY KEY (id);


--
-- Name: apk_packages apk_packages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.apk_packages
    ADD CONSTRAINT apk_packages_pkey PRIMARY KEY (id);


--
-- Name: cargo_crates cargo_crates_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.cargo_crates
    ADD CONSTRAINT cargo_crates_pkey PRIMARY KEY (id);


--
-- Name: composer_packages composer_packages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.composer_packages
    ADD CONSTRAINT composer_packages_pkey PRIMARY KEY (id);


--
-- Name: conan_packages conan_packages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.conan_packages
    ADD CONSTRAINT conan_packages_pkey PRIMARY KEY (id);


--
-- Name: conda_packages conda_packages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.conda_packages
    ADD CONSTRAINT conda_packages_pkey PRIMARY KEY (id);


--
-- Name: deb_packages deb_packages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.deb_packages
    ADD CONSTRAINT deb_packages_pkey PRIMARY KEY (id);


--
-- Name: gomod_modules gomod_modules_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.gomod_modules
    ADD CONSTRAINT gomod_modules_pkey PRIMARY KEY (id);


--
-- Name: helm_charts helm_charts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.helm_charts
    ADD CONSTRAINT helm_charts_pkey PRIMARY KEY (id);


--
-- Name: ivy_artifacts ivy_artifacts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ivy_artifacts
    ADD CONSTRAINT ivy_artifacts_pkey PRIMARY KEY (id);


--
-- Name: maven_artifacts maven_artifacts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.maven_artifacts
    ADD CONSTRAINT maven_artifacts_pkey PRIMARY KEY (id);


--
-- Name: nix_paths nix_paths_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nix_paths
    ADD CONSTRAINT nix_paths_pkey PRIMARY KEY (id);


--
-- Name: npm_dist_tags npm_dist_tags_package_id_tag_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.npm_dist_tags
    ADD CONSTRAINT npm_dist_tags_package_id_tag_key UNIQUE (package_id, tag);


--
-- Name: npm_dist_tags npm_dist_tags_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.npm_dist_tags
    ADD CONSTRAINT npm_dist_tags_pkey PRIMARY KEY (id);


--
-- Name: npm_packages npm_packages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.npm_packages
    ADD CONSTRAINT npm_packages_pkey PRIMARY KEY (id);


--
-- Name: npm_packages npm_packages_repository_id_full_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.npm_packages
    ADD CONSTRAINT npm_packages_repository_id_full_name_key UNIQUE (repository_id, full_name);


--
-- Name: npm_versions npm_versions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.npm_versions
    ADD CONSTRAINT npm_versions_pkey PRIMARY KEY (id);


--
-- Name: nuget_packages nuget_packages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nuget_packages
    ADD CONSTRAINT nuget_packages_pkey PRIMARY KEY (id);


--
-- Name: pacman_packages pacman_packages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pacman_packages
    ADD CONSTRAINT pacman_packages_pkey PRIMARY KEY (id);


--
-- Name: pypi_packages pypi_packages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pypi_packages
    ADD CONSTRAINT pypi_packages_pkey PRIMARY KEY (id);


--
-- Name: raw_artifacts raw_artifacts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.raw_artifacts
    ADD CONSTRAINT raw_artifacts_pkey PRIMARY KEY (id);


--
-- Name: rpm_packages rpm_packages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.rpm_packages
    ADD CONSTRAINT rpm_packages_pkey PRIMARY KEY (id);


--
-- Name: rubygems_gems rubygems_gems_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.rubygems_gems
    ADD CONSTRAINT rubygems_gems_pkey PRIMARY KEY (id);


--
-- Name: sbt_artifacts sbt_artifacts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sbt_artifacts
    ADD CONSTRAINT sbt_artifacts_pkey PRIMARY KEY (id);


--
-- Name: terraform_modules terraform_modules_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.terraform_modules
    ADD CONSTRAINT terraform_modules_pkey PRIMARY KEY (id);


--
-- Name: terraform_providers terraform_providers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.terraform_providers
    ADD CONSTRAINT terraform_providers_pkey PRIMARY KEY (id);


--
-- Name: ansible_collections_live_coords_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX ansible_collections_live_coords_key ON public.ansible_collections USING btree (repository_id, namespace, name, version) WHERE (deleted_at IS NULL);


--
-- Name: apk_packages_live_coords_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX apk_packages_live_coords_key ON public.apk_packages USING btree (repository_id, branch, repo, arch, name, version) WHERE (deleted_at IS NULL);


--
-- Name: cargo_crates_live_coords_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX cargo_crates_live_coords_key ON public.cargo_crates USING btree (repository_id, name, version) WHERE (deleted_at IS NULL);


--
-- Name: composer_packages_live_coords_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX composer_packages_live_coords_key ON public.composer_packages USING btree (repository_id, name, version_normalized) WHERE (deleted_at IS NULL);


--
-- Name: conan_packages_live_file_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX conan_packages_live_file_key ON public.conan_packages USING btree (repository_id, name, version, user_channel, recipe_revision, package_id, package_revision, filename) WHERE (deleted_at IS NULL);


--
-- Name: conda_packages_live_file_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX conda_packages_live_file_key ON public.conda_packages USING btree (repository_id, name, version, build_string, platform, extension) WHERE (deleted_at IS NULL);


--
-- Name: deb_packages_live_coords_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX deb_packages_live_coords_key ON public.deb_packages USING btree (repository_id, name, version, arch, component_id) WHERE (deleted_at IS NULL);


--
-- Name: gomod_modules_live_coords_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX gomod_modules_live_coords_key ON public.gomod_modules USING btree (repository_id, module_path, version) WHERE (deleted_at IS NULL);


--
-- Name: helm_charts_live_coords_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX helm_charts_live_coords_key ON public.helm_charts USING btree (repository_id, name, version) WHERE (deleted_at IS NULL);


--
-- Name: idx_ansible_collections_blob_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ansible_collections_blob_id ON public.ansible_collections USING btree (blob_id);


--
-- Name: idx_ansible_collections_deleted; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ansible_collections_deleted ON public.ansible_collections USING btree (deleted_at) WHERE (deleted_at IS NOT NULL);


--
-- Name: idx_ansible_collections_repo_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ansible_collections_repo_created ON public.ansible_collections USING btree (repository_id, created_at DESC, id) WHERE (deleted_at IS NULL);


--
-- Name: idx_apk_packages_blob_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_apk_packages_blob_id ON public.apk_packages USING btree (blob_id);


--
-- Name: idx_apk_packages_deleted; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_apk_packages_deleted ON public.apk_packages USING btree (deleted_at) WHERE (deleted_at IS NOT NULL);


--
-- Name: idx_apk_packages_repo_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_apk_packages_repo_created ON public.apk_packages USING btree (repository_id, created_at DESC, id) WHERE (deleted_at IS NULL);


--
-- Name: idx_cargo_crates_blob_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cargo_crates_blob_id ON public.cargo_crates USING btree (blob_id);


--
-- Name: idx_cargo_crates_deleted; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cargo_crates_deleted ON public.cargo_crates USING btree (deleted_at) WHERE (deleted_at IS NOT NULL);


--
-- Name: idx_cargo_crates_name_trgm; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cargo_crates_name_trgm ON public.cargo_crates USING gin (name public.gin_trgm_ops);


--
-- Name: idx_cargo_crates_repo_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cargo_crates_repo_created ON public.cargo_crates USING btree (repository_id, created_at DESC, id);


--
-- Name: idx_cargo_crates_repo_lower_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cargo_crates_repo_lower_name ON public.cargo_crates USING btree (repository_id, lower((name)::text)) WHERE (deleted_at IS NULL);


--
-- Name: idx_cargo_crates_repo_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cargo_crates_repo_name ON public.cargo_crates USING btree (repository_id, name);


--
-- Name: idx_composer_packages_blob_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_composer_packages_blob_id ON public.composer_packages USING btree (blob_id);


--
-- Name: idx_composer_packages_deleted; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_composer_packages_deleted ON public.composer_packages USING btree (deleted_at) WHERE (deleted_at IS NOT NULL);


--
-- Name: idx_composer_packages_repo_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_composer_packages_repo_created ON public.composer_packages USING btree (repository_id, created_at DESC, id) WHERE (deleted_at IS NULL);


--
-- Name: idx_composer_packages_repo_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_composer_packages_repo_name ON public.composer_packages USING btree (repository_id, name) WHERE (deleted_at IS NULL);


--
-- Name: idx_conan_packages_blob_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_conan_packages_blob_id ON public.conan_packages USING btree (blob_id);


--
-- Name: idx_conan_packages_deleted; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_conan_packages_deleted ON public.conan_packages USING btree (deleted_at) WHERE (deleted_at IS NOT NULL);


--
-- Name: idx_conan_packages_md5_lookup; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_conan_packages_md5_lookup ON public.conan_packages USING btree (md5) WHERE (((md5)::text <> ''::text) AND (deleted_at IS NULL));


--
-- Name: idx_conan_packages_name_trgm; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_conan_packages_name_trgm ON public.conan_packages USING gin (name public.gin_trgm_ops);


--
-- Name: idx_conan_packages_repo_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_conan_packages_repo_created ON public.conan_packages USING btree (repository_id, created_at DESC, id);


--
-- Name: idx_conan_packages_repo_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_conan_packages_repo_name ON public.conan_packages USING btree (repository_id, name);


--
-- Name: idx_conan_packages_sha1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_conan_packages_sha1 ON public.conan_packages USING btree (repository_id, sha1) WHERE (((sha1)::text <> ''::text) AND (deleted_at IS NULL));


--
-- Name: idx_conan_packages_sha1_lookup; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_conan_packages_sha1_lookup ON public.conan_packages USING btree (sha1) WHERE (((sha1)::text <> ''::text) AND (deleted_at IS NULL));


--
-- Name: idx_conda_packages_blob_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_conda_packages_blob_id ON public.conda_packages USING btree (blob_id);


--
-- Name: idx_conda_packages_deleted; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_conda_packages_deleted ON public.conda_packages USING btree (deleted_at) WHERE (deleted_at IS NOT NULL);


--
-- Name: idx_conda_packages_md5_lookup; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_conda_packages_md5_lookup ON public.conda_packages USING btree (md5) WHERE (((md5)::text <> ''::text) AND (deleted_at IS NULL));


--
-- Name: idx_conda_packages_name_trgm; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_conda_packages_name_trgm ON public.conda_packages USING gin (name public.gin_trgm_ops);


--
-- Name: idx_conda_packages_repo_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_conda_packages_repo_created ON public.conda_packages USING btree (repository_id, created_at DESC, id);


--
-- Name: idx_conda_packages_repo_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_conda_packages_repo_name ON public.conda_packages USING btree (repository_id, name);


--
-- Name: idx_deb_packages_blob_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_deb_packages_blob_id ON public.deb_packages USING btree (blob_id);


--
-- Name: idx_deb_packages_component_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_deb_packages_component_id ON public.deb_packages USING btree (component_id);


--
-- Name: idx_deb_packages_deleted; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_deb_packages_deleted ON public.deb_packages USING btree (deleted_at) WHERE (deleted_at IS NOT NULL);


--
-- Name: idx_deb_packages_distribution; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_deb_packages_distribution ON public.deb_packages USING btree (distribution_id);


--
-- Name: idx_deb_packages_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_deb_packages_name ON public.deb_packages USING btree (repository_id, name);


--
-- Name: idx_deb_packages_name_trgm; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_deb_packages_name_trgm ON public.deb_packages USING gin (name public.gin_trgm_ops);


--
-- Name: idx_deb_packages_repo_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_deb_packages_repo_created ON public.deb_packages USING btree (repository_id, created_at DESC, id);


--
-- Name: idx_deb_packages_repo_filename; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_deb_packages_repo_filename ON public.deb_packages USING btree (repository_id, filename) WHERE (deleted_at IS NULL);


--
-- Name: idx_gomod_modules_deleted; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_gomod_modules_deleted ON public.gomod_modules USING btree (deleted_at) WHERE (deleted_at IS NOT NULL);


--
-- Name: idx_gomod_modules_mod_blob_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_gomod_modules_mod_blob_id ON public.gomod_modules USING btree (mod_blob_id);


--
-- Name: idx_gomod_modules_path_trgm; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_gomod_modules_path_trgm ON public.gomod_modules USING gin (module_path public.gin_trgm_ops);


--
-- Name: idx_gomod_modules_repo_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_gomod_modules_repo_created ON public.gomod_modules USING btree (repository_id, created_at DESC, id);


--
-- Name: idx_gomod_modules_repo_path; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_gomod_modules_repo_path ON public.gomod_modules USING btree (repository_id, module_path);


--
-- Name: idx_gomod_modules_zip_blob_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_gomod_modules_zip_blob_id ON public.gomod_modules USING btree (zip_blob_id);


--
-- Name: idx_helm_charts_blob_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_helm_charts_blob_id ON public.helm_charts USING btree (blob_id);


--
-- Name: idx_helm_charts_deleted; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_helm_charts_deleted ON public.helm_charts USING btree (deleted_at) WHERE (deleted_at IS NOT NULL);


--
-- Name: idx_helm_charts_name_trgm; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_helm_charts_name_trgm ON public.helm_charts USING gin (name public.gin_trgm_ops);


--
-- Name: idx_helm_charts_repo_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_helm_charts_repo_created ON public.helm_charts USING btree (repository_id, created_at DESC, id);


--
-- Name: idx_helm_charts_repo_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_helm_charts_repo_name ON public.helm_charts USING btree (repository_id, name);


--
-- Name: idx_ivy_artifacts_blob_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ivy_artifacts_blob_id ON public.ivy_artifacts USING btree (blob_id);


--
-- Name: idx_ivy_artifacts_deleted; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ivy_artifacts_deleted ON public.ivy_artifacts USING btree (deleted_at) WHERE (deleted_at IS NOT NULL);


--
-- Name: idx_ivy_artifacts_name_trgm; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ivy_artifacts_name_trgm ON public.ivy_artifacts USING gin (((((organisation)::text || '/'::text) || (module)::text)) public.gin_trgm_ops);


--
-- Name: idx_ivy_artifacts_repo_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ivy_artifacts_repo_created ON public.ivy_artifacts USING btree (repository_id, created_at DESC, id);


--
-- Name: idx_ivy_artifacts_repo_org_module; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ivy_artifacts_repo_org_module ON public.ivy_artifacts USING btree (repository_id, organisation, module);


--
-- Name: idx_maven_artifacts_blob_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_maven_artifacts_blob_id ON public.maven_artifacts USING btree (blob_id);


--
-- Name: idx_maven_artifacts_deleted; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_maven_artifacts_deleted ON public.maven_artifacts USING btree (deleted_at) WHERE (deleted_at IS NOT NULL);


--
-- Name: idx_maven_artifacts_gav; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_maven_artifacts_gav ON public.maven_artifacts USING btree (repository_id, group_id, artifact_id_coord, version);


--
-- Name: idx_maven_artifacts_md5_lookup; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_maven_artifacts_md5_lookup ON public.maven_artifacts USING btree (md5) WHERE ((md5 <> ''::text) AND (deleted_at IS NULL));


--
-- Name: idx_maven_artifacts_name_trgm; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_maven_artifacts_name_trgm ON public.maven_artifacts USING gin ((((group_id || ':'::text) || artifact_id_coord)) public.gin_trgm_ops);


--
-- Name: idx_maven_artifacts_repo_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_maven_artifacts_repo_created ON public.maven_artifacts USING btree (repository_id, created_at DESC, id);


--
-- Name: idx_maven_artifacts_sha1_lookup; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_maven_artifacts_sha1_lookup ON public.maven_artifacts USING btree (sha1) WHERE ((sha1 <> ''::text) AND (deleted_at IS NULL));


--
-- Name: idx_nix_paths_blob_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_nix_paths_blob_id ON public.nix_paths USING btree (blob_id);


--
-- Name: idx_nix_paths_deleted; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_nix_paths_deleted ON public.nix_paths USING btree (deleted_at) WHERE (deleted_at IS NOT NULL);


--
-- Name: idx_nix_paths_nar_file; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_nix_paths_nar_file ON public.nix_paths USING btree (repository_id, nar_file) WHERE (deleted_at IS NULL);


--
-- Name: idx_nix_paths_repo_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_nix_paths_repo_created ON public.nix_paths USING btree (repository_id, created_at DESC, id) WHERE (deleted_at IS NULL);


--
-- Name: idx_npm_packages_name_trgm; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_npm_packages_name_trgm ON public.npm_packages USING gin (full_name public.gin_trgm_ops);


--
-- Name: idx_npm_versions_blob_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_npm_versions_blob_id ON public.npm_versions USING btree (blob_id);


--
-- Name: idx_npm_versions_deleted; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_npm_versions_deleted ON public.npm_versions USING btree (deleted_at) WHERE (deleted_at IS NOT NULL);


--
-- Name: idx_npm_versions_package_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_npm_versions_package_id ON public.npm_versions USING btree (package_id);


--
-- Name: idx_npm_versions_shasum_lookup; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_npm_versions_shasum_lookup ON public.npm_versions USING btree (shasum) WHERE ((shasum <> ''::text) AND (deleted_at IS NULL));


--
-- Name: idx_nuget_packages_blob_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_nuget_packages_blob_id ON public.nuget_packages USING btree (blob_id);


--
-- Name: idx_nuget_packages_deleted; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_nuget_packages_deleted ON public.nuget_packages USING btree (deleted_at) WHERE (deleted_at IS NOT NULL);


--
-- Name: idx_nuget_packages_name_trgm; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_nuget_packages_name_trgm ON public.nuget_packages USING gin (package_id public.gin_trgm_ops);


--
-- Name: idx_nuget_packages_repo_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_nuget_packages_repo_created ON public.nuget_packages USING btree (repository_id, created_at DESC, id);


--
-- Name: idx_nuget_packages_repo_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_nuget_packages_repo_id ON public.nuget_packages USING btree (repository_id, lower((package_id)::text));


--
-- Name: idx_pacman_packages_blob_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pacman_packages_blob_id ON public.pacman_packages USING btree (blob_id);


--
-- Name: idx_pacman_packages_deleted; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pacman_packages_deleted ON public.pacman_packages USING btree (deleted_at) WHERE (deleted_at IS NOT NULL);


--
-- Name: idx_pacman_packages_filename; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pacman_packages_filename ON public.pacman_packages USING btree (repository_id, filename) WHERE (deleted_at IS NULL);


--
-- Name: idx_pacman_packages_repo_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pacman_packages_repo_created ON public.pacman_packages USING btree (repository_id, created_at DESC, id) WHERE (deleted_at IS NULL);


--
-- Name: idx_pypi_packages_blob_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pypi_packages_blob_id ON public.pypi_packages USING btree (blob_id);


--
-- Name: idx_pypi_packages_deleted; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pypi_packages_deleted ON public.pypi_packages USING btree (deleted_at) WHERE (deleted_at IS NOT NULL);


--
-- Name: idx_pypi_packages_legacy_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pypi_packages_legacy_name ON public.pypi_packages USING btree (repository_id, regexp_replace((name)::text, '-+'::text, '-'::text, 'g'::text)) WHERE ((deleted_at IS NULL) AND ((name)::text ~~ '%--%'::text));


--
-- Name: idx_pypi_packages_name_trgm; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pypi_packages_name_trgm ON public.pypi_packages USING gin (name public.gin_trgm_ops);


--
-- Name: idx_pypi_packages_repo_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pypi_packages_repo_created ON public.pypi_packages USING btree (repository_id, created_at DESC, id);


--
-- Name: idx_pypi_packages_repo_filename; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pypi_packages_repo_filename ON public.pypi_packages USING btree (repository_id, filename) WHERE (deleted_at IS NULL);


--
-- Name: idx_pypi_packages_repo_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pypi_packages_repo_name ON public.pypi_packages USING btree (repository_id, name);


--
-- Name: idx_raw_artifacts_blob_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_raw_artifacts_blob_id ON public.raw_artifacts USING btree (blob_id);


--
-- Name: idx_raw_artifacts_deleted; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_raw_artifacts_deleted ON public.raw_artifacts USING btree (deleted_at) WHERE (deleted_at IS NOT NULL);


--
-- Name: idx_raw_artifacts_path_trgm; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_raw_artifacts_path_trgm ON public.raw_artifacts USING gin (path public.gin_trgm_ops);


--
-- Name: idx_raw_artifacts_repo_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_raw_artifacts_repo_created ON public.raw_artifacts USING btree (repository_id, created_at DESC, id);


--
-- Name: idx_raw_artifacts_repo_path; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_raw_artifacts_repo_path ON public.raw_artifacts USING btree (repository_id, path);


--
-- Name: idx_rpm_packages_blob_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rpm_packages_blob_id ON public.rpm_packages USING btree (blob_id);


--
-- Name: idx_rpm_packages_deleted; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rpm_packages_deleted ON public.rpm_packages USING btree (deleted_at) WHERE (deleted_at IS NOT NULL);


--
-- Name: idx_rpm_packages_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rpm_packages_name ON public.rpm_packages USING btree (repository_id, name);


--
-- Name: idx_rpm_packages_name_trgm; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rpm_packages_name_trgm ON public.rpm_packages USING gin (name public.gin_trgm_ops);


--
-- Name: idx_rpm_packages_repo_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rpm_packages_repo_created ON public.rpm_packages USING btree (repository_id, created_at DESC, id);


--
-- Name: idx_rpm_packages_repo_filename; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rpm_packages_repo_filename ON public.rpm_packages USING btree (repository_id, filename) WHERE (deleted_at IS NULL);


--
-- Name: idx_rubygems_gems_blob_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rubygems_gems_blob_id ON public.rubygems_gems USING btree (blob_id);


--
-- Name: idx_rubygems_gems_deleted; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rubygems_gems_deleted ON public.rubygems_gems USING btree (deleted_at) WHERE (deleted_at IS NOT NULL);


--
-- Name: idx_rubygems_gems_name_trgm; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rubygems_gems_name_trgm ON public.rubygems_gems USING gin (name public.gin_trgm_ops);


--
-- Name: idx_rubygems_gems_repo_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rubygems_gems_repo_created ON public.rubygems_gems USING btree (repository_id, created_at DESC, id);


--
-- Name: idx_rubygems_gems_repo_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rubygems_gems_repo_name ON public.rubygems_gems USING btree (repository_id, name);


--
-- Name: idx_sbt_artifacts_blob_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sbt_artifacts_blob_id ON public.sbt_artifacts USING btree (blob_id);


--
-- Name: idx_sbt_artifacts_deleted; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sbt_artifacts_deleted ON public.sbt_artifacts USING btree (deleted_at) WHERE (deleted_at IS NOT NULL);


--
-- Name: idx_sbt_artifacts_name_trgm; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sbt_artifacts_name_trgm ON public.sbt_artifacts USING gin (((((group_id)::text || '/'::text) || (artifact_id)::text)) public.gin_trgm_ops);


--
-- Name: idx_sbt_artifacts_repo_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sbt_artifacts_repo_created ON public.sbt_artifacts USING btree (repository_id, created_at DESC, id);


--
-- Name: idx_sbt_artifacts_repo_group_artifact; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sbt_artifacts_repo_group_artifact ON public.sbt_artifacts USING btree (repository_id, group_id, artifact_id);


--
-- Name: idx_terraform_modules_blob_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_terraform_modules_blob_id ON public.terraform_modules USING btree (blob_id);


--
-- Name: idx_terraform_modules_deleted; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_terraform_modules_deleted ON public.terraform_modules USING btree (deleted_at) WHERE (deleted_at IS NOT NULL);


--
-- Name: idx_terraform_modules_repo_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_terraform_modules_repo_created ON public.terraform_modules USING btree (repository_id, created_at DESC, id) WHERE (deleted_at IS NULL);


--
-- Name: idx_terraform_providers_blob_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_terraform_providers_blob_id ON public.terraform_providers USING btree (blob_id);


--
-- Name: idx_terraform_providers_deleted; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_terraform_providers_deleted ON public.terraform_providers USING btree (deleted_at) WHERE (deleted_at IS NOT NULL);


--
-- Name: idx_terraform_providers_repo_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_terraform_providers_repo_created ON public.terraform_providers USING btree (repository_id, created_at DESC, id) WHERE (deleted_at IS NULL);


--
-- Name: ivy_artifacts_live_coords_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX ivy_artifacts_live_coords_key ON public.ivy_artifacts USING btree (repository_id, organisation, module, revision, artifact_name, artifact_ext) WHERE (deleted_at IS NULL);


--
-- Name: maven_artifacts_live_repo_path_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX maven_artifacts_live_repo_path_key ON public.maven_artifacts USING btree (repository_id, repo_path) WHERE (deleted_at IS NULL);


--
-- Name: nix_paths_live_hash_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX nix_paths_live_hash_key ON public.nix_paths USING btree (repository_id, store_hash) WHERE (deleted_at IS NULL);


--
-- Name: npm_versions_live_pkg_version_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX npm_versions_live_pkg_version_key ON public.npm_versions USING btree (package_id, version) WHERE (deleted_at IS NULL);


--
-- Name: nuget_packages_live_coords_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX nuget_packages_live_coords_key ON public.nuget_packages USING btree (repository_id, package_id, version) WHERE (deleted_at IS NULL);


--
-- Name: pacman_packages_live_coords_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX pacman_packages_live_coords_key ON public.pacman_packages USING btree (repository_id, name, version, arch) WHERE (deleted_at IS NULL);


--
-- Name: pypi_packages_live_coords_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX pypi_packages_live_coords_key ON public.pypi_packages USING btree (repository_id, name, version, filename) WHERE (deleted_at IS NULL);


--
-- Name: raw_artifacts_live_path_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX raw_artifacts_live_path_key ON public.raw_artifacts USING btree (repository_id, path) WHERE (deleted_at IS NULL);


--
-- Name: rpm_packages_live_coords_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX rpm_packages_live_coords_key ON public.rpm_packages USING btree (repository_id, name, version, release, epoch, arch) WHERE (deleted_at IS NULL);


--
-- Name: rubygems_gems_live_coords_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX rubygems_gems_live_coords_key ON public.rubygems_gems USING btree (repository_id, name, version, platform) WHERE (deleted_at IS NULL);


--
-- Name: sbt_artifacts_live_coords_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX sbt_artifacts_live_coords_key ON public.sbt_artifacts USING btree (repository_id, group_id, artifact_id, version, filename) WHERE (deleted_at IS NULL);


--
-- Name: terraform_modules_live_coords_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX terraform_modules_live_coords_key ON public.terraform_modules USING btree (repository_id, name, system, version) WHERE (deleted_at IS NULL);


--
-- Name: terraform_providers_live_coords_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX terraform_providers_live_coords_key ON public.terraform_providers USING btree (repository_id, type, version, os, arch) WHERE (deleted_at IS NULL);


--
-- Name: ansible_collections ansible_collections_blob_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ansible_collections
    ADD CONSTRAINT ansible_collections_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES public.blobs(id) ON DELETE RESTRICT;


--
-- Name: ansible_collections ansible_collections_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ansible_collections
    ADD CONSTRAINT ansible_collections_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: apk_packages apk_packages_blob_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.apk_packages
    ADD CONSTRAINT apk_packages_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES public.blobs(id) ON DELETE RESTRICT;


--
-- Name: apk_packages apk_packages_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.apk_packages
    ADD CONSTRAINT apk_packages_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: cargo_crates cargo_crates_blob_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.cargo_crates
    ADD CONSTRAINT cargo_crates_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES public.blobs(id) ON DELETE RESTRICT;


--
-- Name: cargo_crates cargo_crates_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.cargo_crates
    ADD CONSTRAINT cargo_crates_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: composer_packages composer_packages_blob_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.composer_packages
    ADD CONSTRAINT composer_packages_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES public.blobs(id) ON DELETE RESTRICT;


--
-- Name: composer_packages composer_packages_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.composer_packages
    ADD CONSTRAINT composer_packages_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: conan_packages conan_packages_blob_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.conan_packages
    ADD CONSTRAINT conan_packages_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES public.blobs(id) ON DELETE RESTRICT;


--
-- Name: conan_packages conan_packages_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.conan_packages
    ADD CONSTRAINT conan_packages_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: conda_packages conda_packages_blob_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.conda_packages
    ADD CONSTRAINT conda_packages_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES public.blobs(id) ON DELETE RESTRICT;


--
-- Name: conda_packages conda_packages_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.conda_packages
    ADD CONSTRAINT conda_packages_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: deb_packages deb_packages_blob_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.deb_packages
    ADD CONSTRAINT deb_packages_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES public.blobs(id) ON DELETE RESTRICT;


--
-- Name: deb_packages deb_packages_component_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.deb_packages
    ADD CONSTRAINT deb_packages_component_id_fkey FOREIGN KEY (component_id) REFERENCES public.debian_components(id) ON DELETE CASCADE;


--
-- Name: deb_packages deb_packages_distribution_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.deb_packages
    ADD CONSTRAINT deb_packages_distribution_id_fkey FOREIGN KEY (distribution_id) REFERENCES public.debian_distributions(id) ON DELETE CASCADE;


--
-- Name: deb_packages deb_packages_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.deb_packages
    ADD CONSTRAINT deb_packages_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: gomod_modules gomod_modules_mod_blob_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.gomod_modules
    ADD CONSTRAINT gomod_modules_mod_blob_id_fkey FOREIGN KEY (mod_blob_id) REFERENCES public.blobs(id) ON DELETE RESTRICT;


--
-- Name: gomod_modules gomod_modules_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.gomod_modules
    ADD CONSTRAINT gomod_modules_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: gomod_modules gomod_modules_zip_blob_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.gomod_modules
    ADD CONSTRAINT gomod_modules_zip_blob_id_fkey FOREIGN KEY (zip_blob_id) REFERENCES public.blobs(id) ON DELETE RESTRICT;


--
-- Name: helm_charts helm_charts_blob_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.helm_charts
    ADD CONSTRAINT helm_charts_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES public.blobs(id) ON DELETE RESTRICT;


--
-- Name: helm_charts helm_charts_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.helm_charts
    ADD CONSTRAINT helm_charts_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: ivy_artifacts ivy_artifacts_blob_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ivy_artifacts
    ADD CONSTRAINT ivy_artifacts_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES public.blobs(id) ON DELETE RESTRICT;


--
-- Name: ivy_artifacts ivy_artifacts_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ivy_artifacts
    ADD CONSTRAINT ivy_artifacts_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: maven_artifacts maven_artifacts_blob_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.maven_artifacts
    ADD CONSTRAINT maven_artifacts_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES public.blobs(id) ON DELETE RESTRICT;


--
-- Name: maven_artifacts maven_artifacts_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.maven_artifacts
    ADD CONSTRAINT maven_artifacts_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: nix_paths nix_paths_blob_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nix_paths
    ADD CONSTRAINT nix_paths_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES public.blobs(id) ON DELETE RESTRICT;


--
-- Name: nix_paths nix_paths_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nix_paths
    ADD CONSTRAINT nix_paths_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: npm_dist_tags npm_dist_tags_package_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.npm_dist_tags
    ADD CONSTRAINT npm_dist_tags_package_id_fkey FOREIGN KEY (package_id) REFERENCES public.npm_packages(id) ON DELETE CASCADE;


--
-- Name: npm_packages npm_packages_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.npm_packages
    ADD CONSTRAINT npm_packages_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: npm_versions npm_versions_blob_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.npm_versions
    ADD CONSTRAINT npm_versions_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES public.blobs(id) ON DELETE RESTRICT;


--
-- Name: npm_versions npm_versions_package_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.npm_versions
    ADD CONSTRAINT npm_versions_package_id_fkey FOREIGN KEY (package_id) REFERENCES public.npm_packages(id) ON DELETE CASCADE;


--
-- Name: nuget_packages nuget_packages_blob_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nuget_packages
    ADD CONSTRAINT nuget_packages_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES public.blobs(id) ON DELETE RESTRICT;


--
-- Name: nuget_packages nuget_packages_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nuget_packages
    ADD CONSTRAINT nuget_packages_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: pacman_packages pacman_packages_blob_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pacman_packages
    ADD CONSTRAINT pacman_packages_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES public.blobs(id) ON DELETE RESTRICT;


--
-- Name: pacman_packages pacman_packages_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pacman_packages
    ADD CONSTRAINT pacman_packages_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: pypi_packages pypi_packages_blob_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pypi_packages
    ADD CONSTRAINT pypi_packages_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES public.blobs(id) ON DELETE RESTRICT;


--
-- Name: pypi_packages pypi_packages_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pypi_packages
    ADD CONSTRAINT pypi_packages_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: raw_artifacts raw_artifacts_blob_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.raw_artifacts
    ADD CONSTRAINT raw_artifacts_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES public.blobs(id) ON DELETE RESTRICT;


--
-- Name: raw_artifacts raw_artifacts_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.raw_artifacts
    ADD CONSTRAINT raw_artifacts_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: rpm_packages rpm_packages_blob_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.rpm_packages
    ADD CONSTRAINT rpm_packages_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES public.blobs(id) ON DELETE RESTRICT;


--
-- Name: rpm_packages rpm_packages_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.rpm_packages
    ADD CONSTRAINT rpm_packages_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: rubygems_gems rubygems_gems_blob_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.rubygems_gems
    ADD CONSTRAINT rubygems_gems_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES public.blobs(id) ON DELETE RESTRICT;


--
-- Name: rubygems_gems rubygems_gems_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.rubygems_gems
    ADD CONSTRAINT rubygems_gems_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: sbt_artifacts sbt_artifacts_blob_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sbt_artifacts
    ADD CONSTRAINT sbt_artifacts_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES public.blobs(id) ON DELETE RESTRICT;


--
-- Name: sbt_artifacts sbt_artifacts_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sbt_artifacts
    ADD CONSTRAINT sbt_artifacts_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: terraform_modules terraform_modules_blob_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.terraform_modules
    ADD CONSTRAINT terraform_modules_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES public.blobs(id) ON DELETE RESTRICT;


--
-- Name: terraform_modules terraform_modules_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.terraform_modules
    ADD CONSTRAINT terraform_modules_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


--
-- Name: terraform_providers terraform_providers_blob_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.terraform_providers
    ADD CONSTRAINT terraform_providers_blob_id_fkey FOREIGN KEY (blob_id) REFERENCES public.blobs(id) ON DELETE RESTRICT;


--
-- Name: terraform_providers terraform_providers_repo_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.terraform_providers
    ADD CONSTRAINT terraform_providers_repo_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE;


