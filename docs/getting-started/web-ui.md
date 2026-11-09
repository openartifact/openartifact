<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Web UI

This page is a map of the web UI: the seven areas, the command palette, repository and artifact pages, the security inbox and the settings hub. Every page shows a breadcrumb trail, and every filter, tab and page is part of the URL, so a link copied from the address bar opens the same view.

## The shell

The sidebar has seven areas. Areas that need more rights than you have stay visible with a "Requires admin" note instead of disappearing.

| Area | URL | What it is for |
|---|---|---|
| Home | `/` | Health strip, what needs attention, recent named activity and small totals |
| Repositories | `/repos` | The repository list; one repository is `/repos/NAME` |
| Packages | `/packages` | Search across every repository you can read |
| Projects | `/projects` | The list of projects and `/projects/NAME` |
| Bundles | `/bundles` | [Release bundles](../repositories/release-bundles.md); administrators only |
| Security | `/security` | One inbox for vulnerability, license, secret and AI findings |
| Settings | `/settings` | Your account, and for administrators everything configurable on the instance |

Details:

- **Home** lists critical findings, failing services and repositories near their quota. It shows only what the API reports.
- **Repositories:** **New repository** (or `/repos?new=1`) opens the create drawer.
- **Packages:** filters by format, repository and project, and **Shared with me** keeps the projects where you are a viewer (`?project=NAME&shared=1`). The project filters apply to name searches, not to checksum lookups.
- **Projects:** the list takes `?scope=mine` or `?scope=all`. A project has the tabs Overview, Repositories, Members, Activity, Usage and Settings.
- **Security:** see [Security findings](../security/findings.md).

The header holds the command palette trigger and the user menu: profile, access tokens, theme, [language](language.md), **Install app** when the browser offers it, About and sign out.

## Command palette

Press **Ctrl+K** (**Cmd+K** on macOS) anywhere, or use the search field in the header. Arrow keys move, Enter opens and Esc closes. Type `>` first to list actions only.

### What it searches

- Repositories, packages (one hit per package), projects, bundles, settings sections and users, in one list.
- A hex string of 32, 40 or 64 characters offers **Look up checksum in Packages**.
- A query that matches too many packages asks you to refine it with `name:`, `format:`, `repo:` or `project:`.
- Settings you may not open are listed with "Requires admin".

### Actions

The palette runs these actions from the keyboard alone:

- **Create repository**, **Create project**, **Create access token**, **Create user** and **Create release bundle**.
- **Switch to dark/light theme** and **Change language to …**.
- **Sign out**.

When the text matches a repository name, two more actions join the list:

- **Open settings of NAME**.
- **Copy install command for NAME**: the first client's configure snippet, built from the address you are using. Credentials stay `<USERNAME>` and `<TOKEN>` placeholders.

An action you may not run is not shown:

- Creating repositories needs an administrator, or a maintainer or owner role in a project.
- Creating a project needs an administrator or self-service project creation.
- Users and bundles need an administrator.
- A repository's settings need an administrator, or a maintainer or owner role in its project.

Actions you ran (except signing out and changing language) appear under **Recent** the next time you open the palette with an empty field.

## Repositories

`/repos` lists repositories with their size, last publish, security counts and quota. It filters by name, format, type and state (has findings, near quota, unscanned). **New repository** opens a drawer: choose the format first, then the type (hosted, proxy or group) and its fields.

A repository page (`/repos/NAME`) has these tabs, selected with `?tab=`. Tabs you may not use are hidden.

| Tab | Content | Who sees it |
|---|---|---|
| Members | The ordered member repositories | Group repositories; maintainers and administrators |
| Packages (Cached items for a proxy) | The package list, grouped by package, with search and sort | Hosted and proxy repositories |
| Activity | The events recorded for the repository | Everyone who can read it |
| Security | Scan counts and the scans of the repository | Everyone who can read it |
| Trash | Deleted items, who deleted them and when they are purged; restore | Users who can delete in the repository |
| Settings | The repository's configuration, see below | Maintainers and administrators |

Readers see only the artifact events in **Activity**; maintainers and administrators see everything and get more filters ([project activity](../security/projects.md#project-activity)).

### Proxy settings

A proxy repository shows its upstream and caching settings above the tabs. Maintainers and administrators open **Edit proxy settings**:

- **Upstream:** the URL, an optional username and the password (keep, replace or clear it).
- **Cache TTL**, **Negative cache TTL** and **Serve stale for**: see [cache TTLs](../repositories/hosted-proxy-group.md#cache-ttls) and [serve-stale](../repositories/hosted-proxy-group.md#upstream-failures-and-serve-stale).
- **Minimum age**: hours a version must be old before clients see it; `0` disables it. See [minimum age](../repositories/hosted-proxy-group.md#minimum-age).
- **Blocked**: refuses every request. Only an administrator can change it.

### Repository settings

The **Settings** tab is one page with these sections:

- **General:** description and the same fields as the create drawer.
- **Quota:** usage and the storage limit.
- **Key panel:** hosted APK and Nix repositories show their signing key here.
- **Curation:** proxy repositories, for maintainers and administrators. Choose **Off**, **Denylist** or **Allowlist**, then add rules that block or allow a package pattern and optional version pattern. A matching allow rule also skips the minimum age. See [package curation](../repositories/hosted-proxy-group.md#package-curation).
- **Cleanup policies:** see [cleanup policies and quotas](../repositories/cleanup-and-quotas.md).
- **Federation:** administrators only, for federated formats.
- **Danger zone:** delete the repository.

Repository settings live on the repository; the **Settings** area is only about the instance and your account.

## The artifact page

One page for every format: `/repos/REPO/packages/NAME` for the latest version and `/repos/REPO/packages/NAME/versions/VERSION` for a specific one. Names may contain slashes (Docker image paths, Go modules, npm scopes) and stay readable in the path. The version switcher keeps the current tab. Tabs are selected with `?tab=`:

| Tab | Content | Who sees it |
|---|---|---|
| Overview | Install command for the format and the version's metadata; [properties](../repositories/properties.md) open in a drawer | Everyone |
| Versions | All versions with their security status | Everyone |
| Files | The files of the version, with download and checksums | Everyone |
| Dependencies | The dependencies the package file declares and the dependency graph | Everyone |
| Security | Vulnerabilities, licenses, secrets, quarantine and [VEX decisions](../security/sbom.md#in-the-web-ui) of the version | Everyone |
| SBOM | Generate, view and download SBOM documents | Administrators |
| Activity | Events for this package | Maintainers and administrators |

### Versions of a proxied package

On a proxy repository, the page lists every version the cached upstream metadata names, newest first:

- **Available:** clients can install it.
- **Held:** the repository keeps it from clients, for example by minimum age or curation. The row shows when it opens or why it is held, and curation rules link to the rule.
- **Cached:** a file of this version is already stored in the repository. It appears next to Available or Held.

Each available row can copy its install command.

## Security inbox

`/security` lists the findings of all repositories you may see, worst first. Switch the type with `?type=vuln`, `license`, `secret` or `ai`, and narrow by `severity`, `status` (open, quarantined, suppressed) and `repo`.

- The summary strip on top applies filters when you click it.
- A notice shows scanners that are not ready.
- **Export** downloads the current selection as CSV, JSON or VEX.

Administrators see everything. Project maintainers and owners see the findings of their projects' repositories, which is why Security is in their navigation too. Everybody else gets only the per-repository counts on the repository and artifact pages.

## Settings hub

`/settings` is a searchable hub (search by what you want to change, for example "retention" or "s3 bucket") of seven groups. Non-administrators see only their own account sections: profile, password and their access tokens. A section is `/settings/SECTION`.

| Group | Sections (id) |
|---|---|
| Your account | Profile (`profile`), Password (`password`) |
| Access | Access tokens (`tokens`), Users (`users`), Service accounts (`service-accounts`), Groups (`groups`), Permissions (`permissions`), Single sign-on (`sso`), Project creation policy (`project-policy`) |
| Storage | Storage backends (`backends`), Storage tiers (`tiers`), Quotas (`quotas`), Cleanup policies (`cleanup`), Trash (`trash`), Backup and restore (`backup`), Recovery (`recovery`) |
| Security | Scanners (`scanners`), Policies (`policies`), GPG and signing (`signing`), Dependency-Track (`dependency-track`), Attestations (`attestation`), Scan gate (`scan-gate`) |
| Integrations | Webhooks (`webhooks`), Federation (`federation`), AI analysis (`ai`) |
| Appearance | Branding (`branding`) |
| System | Audit log (`audit`), Declarative config (`declarative`), System health (`health`) |

AI analysis is not listed on a server build made without it. Every section except your account sections and Access tokens needs an administrator.

**Access tokens** is the single credentials list: personal tokens, [project tokens](../security/projects.md#project-tokens) and federation tokens, filtered with `?type=personal`, `project` or `federation`, with expiry and last use. Administrators see every token and project maintainers see the tokens of their projects. Everybody sees their own personal tokens. The user menu opens your own at `/settings/tokens?type=personal`.

Pages with their own guides:

- **Service accounts:** non-human principals and OIDC trust policies for CI; see [CI](../integrations/ci.md).
- **Declarative config:** review and apply YAML, or download the live configuration; see [declarative configuration](../integrations/declarative.md).
- **Recovery:** journal health and the rebuild commands; see [rebuild from storage](../operations/rebuild-from-storage.md).
- **Attestations:** see [signed SBOM and VEX attestations](../security/sbom.md#signed-sbom-and-vex-attestations).
- **Scan gate:** see [exploitation signals](../security/exploit-signals.md).

## Where the old URLs went

Every URL of earlier releases still works: it redirects to the new place and keeps the query string, so bookmarks and links in documents survive.

| Before | Now |
|---|---|
| `/stats` | `/` |
| `/search?q=...` | `/packages?q=...` |
| `/profile` | `/settings/profile` |
| `/repos/new` | `/repos?new=1` |
| `/repos/NAME/edit`, `/repos/NAME/settings` | `/repos/NAME?tab=settings` |
| `/repos/NAME/browse`, `/repos/NAME/graph` | `/repos/NAME?tab=packages` |
| `/repos/NAME/trash` | `/repos/NAME?tab=trash` |
| `/repos/NAME/packages/FORMAT/PACKAGE?version=V` | `/repos/NAME/packages/PACKAGE/versions/V` |
| `/security/vulns`, `/security/licenses`, `/security/secrets`, `/security/ai-analysis` | `/security?type=vuln`, `license`, `secret`, `ai` |
| `/admin` | `/settings` |
| `/admin/users`, `groups`, `permissions`, `sso`, `webhooks`, `backup`, `ai`, `branding`, `audit`, `dependency-track`, `trash` | `/settings/` plus the same name |
| `/admin/system` | `/settings/health` |
| `/admin/gpg` | `/settings/signing` |
| `/admin/storage`, `/admin/storage/tiers` | `/settings/backends`, `/settings/tiers` |
| `/admin/tokens` | `/settings/tokens?type=all` |
| `/admin/projects` | `/projects?scope=all` (the creation policy is `/settings/project-policy`) |
