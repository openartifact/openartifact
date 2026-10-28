<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Web UI

How the web UI is organised: the seven areas, the command palette, the settings hub, the artifact page and where the URLs of earlier releases went. Every page shows a breadcrumb trail, and every filter, tab and page is part of the URL, so a link copied from the address bar opens the same view.

## The shell

The sidebar has seven areas. Areas that need more rights than you have stay visible with a "Requires admin" note instead of disappearing.

| Area | URL | What it is for |
|---|---|---|
| Home | `/` | Health strip, what needs attention (critical findings, failing services, repositories near their quota), recent named activity and small totals. It shows only what the API reports. |
| Repositories | `/repos` | The repository list; **New repository** (or `/repos?new=1`) opens the create drawer. One repository is `/repos/NAME`. |
| Packages | `/packages` | Search across every repository you can read. |
| Projects | `/projects` | The one place for projects: the list (`?scope=mine` or `?scope=all`), and `/projects/NAME` with the tabs Overview, Repositories, Members, Activity, Usage and Settings. |
| Bundles | `/bundles` | [Release bundles](../repositories/release-bundles.md); administrators only. |
| Security | `/security` | One inbox for vulnerability, license, secret and AI findings; see [Security findings](../security/findings.md). |
| Settings | `/settings` | Your account, and for administrators everything configurable on the instance. |

The header holds the command palette trigger and the user menu (profile, access tokens, theme, [language](language.md), **Install app** when the browser offers it, About, sign out).

## Command palette

Press **Ctrl+K** (**Cmd+K** on macOS) anywhere, or use the search field in the header. It searches repositories, packages (one hit per package), projects, bundles, settings sections and users in one list; arrow keys move, Enter opens, Esc closes. Type `>` first to list actions only. A hex string of 32, 40 or 64 characters offers **Look up checksum in Packages**. A query that matches too many packages asks you to refine it with `name:`, `format:` or `repo:`. Settings you may not open are listed with "Requires admin".

## Repositories

`/repos` lists repositories with their size, last publish, security counts and quota, and filters by name, format, type and state (has findings, near quota, unscanned). **New repository** opens a drawer: choose the format first, then the type (hosted, proxy or group) and its fields.

A repository page (`/repos/NAME`) has these tabs, selected with `?tab=`:

| Tab | Content |
|---|---|
| Members | Group repositories only: the ordered member repositories |
| Packages (Cached for a proxy) | The package list, grouped by package, with search and sort |
| Activity | The events recorded for the repository (maintainers and administrators) |
| Security | Scan counts and the scans of the repository |
| Trash | Deleted items, who deleted them and when they are purged; restore for users who can delete there |
| Settings | **General**, **Quota**, **Cleanup policies**, **Federation** (federated formats) and the **Danger zone** (delete); maintainers and administrators |

Repository settings live on the repository; the **Settings** area is only about the instance and your account.

## The artifact page

One page for every format: `/repos/REPO/packages/NAME` for the latest version and `/repos/REPO/packages/NAME/versions/VERSION` for a specific one. Names may contain slashes (Docker image paths, Go modules, npm scopes), they stay readable in the path. The version switcher keeps the current tab. Tabs, selected with `?tab=`:

| Tab | Content |
|---|---|
| Overview | Install command for the format and the version's metadata; [properties](../repositories/properties.md) open in a drawer |
| Versions | All versions with their security status |
| Files | The files of the version, with download and checksums |
| Dependencies | The dependencies the package file declares and the dependency graph |
| Security | Vulnerabilities, licenses, secrets, quarantine and [VEX decisions](../security/sbom.md#in-the-web-ui) of the version |
| SBOM | Generate, view and download SBOM documents |
| Activity | Events for this package (maintainers and administrators) |

## Security inbox

`/security` lists the findings of all repositories you may see, worst first. Switch the type with `?type=vuln`, `license`, `secret` or `ai` (one list, one filter set), and narrow by `severity`, `status` (open, quarantined, suppressed) and `repo`. The summary strip on top applies filters when you click it, a notice shows scanners that are not ready, and **Export** downloads the current selection as CSV, JSON or VEX. Administrators see everything; project maintainers and owners see the findings of their projects' repositories, which is why Security is in the navigation for them too. Everybody else gets only the per-repository counts on the repository and artifact pages.

## Settings hub

`/settings` is a searchable hub (search by what you want to change, for example "retention" or "s3 bucket") of seven groups. Non-administrators see only their own account sections. A section is `/settings/SECTION`.

| Group | Sections (id) |
|---|---|
| Your account | Profile (`profile`), Password (`password`), Access tokens (`tokens?type=personal`) |
| Access | Users (`users`), Groups (`groups`), Permissions (`permissions`), Single sign-on (`sso`), Access tokens (`tokens`), Project creation policy (`project-policy`) |
| Storage | Storage backends (`backends`), Storage tiers (`tiers`), Quotas (`quotas`), Cleanup policies (`cleanup`), Trash (`trash`), Backup and restore (`backup`) |
| Security | Scanners (`scanners`), Policies (`policies`), GPG and signing (`signing`), Dependency-Track (`dependency-track`) |
| Integrations | Webhooks (`webhooks`), Federation (`federation`), AI analysis (`ai`) |
| Appearance | Branding (`branding`) |
| System | Audit log (`audit`), System health (`health`) |

**Access tokens** is the single credentials list: personal tokens, [project tokens](../security/projects.md#project-tokens) and federation tokens, filtered with `?type=personal`, `project` or `federation`, with expiry and last use. Administrators see every token; project maintainers see the tokens of their projects; everybody sees their own personal tokens.

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
| `/admin/tokens` | `/settings/tokens` |
| `/admin/projects` | `/projects?scope=all` (the creation policy is `/settings/project-policy`) |
