<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# White-label branding

An instance can carry your own product name, logos, favicon, accent colour, login message and support link instead of the OpenArtifact ones. Every value is optional: with nothing set the UI shows the built-in OpenArtifact brand.

Branding changes what people **see**. It does not change protocol identifiers: the `openartifact` webhook payload format, the sbt realm, the `X-OA-*` headers, the `oa` command line client and the published documentation stay as they are, so clients and integrations keep working after a rename.

## What you can set

| Setting | Meaning | Rules |
|---|---|---|
| `productName` | Replaces "OpenArtifact" in the page title, the sidebar, the login page, the installed-app (PWA) name and the offline page | 1 to 64 characters, no control or bidirectional-override characters |
| `logoLight` | Logo shown on light surfaces | PNG or SVG, at most 512 KiB |
| `logoDark` | Logo shown on dark surfaces | PNG or SVG, at most 512 KiB. When only one logo is set it is used in both themes |
| `favicon` | Browser tab icon, and the installed-app icon | PNG or SVG, at most 512 KiB |
| `primaryColor` | Accent colour (buttons, links, focus rings, active navigation) | `#rrggbb` |
| `loginMessage` | Text under the sign-in form | Up to 2000 bytes; plain text with `**bold**`, `*italic*`, `` `code` `` and `[text](https://…)` links. No HTML |
| `supportUrl` | "Get help" link in the About dialog | `http(s)://…` or `mailto:…`; empty hides it |
| `sourceUrl` | Where the "Source code" link points (see [attribution](#attribution-agpl-13)) | `http(s)://…`; defaults to `https://github.com/openartifact/openartifact`; cannot be empty |

Images:

- PNG files are re-encoded on upload (metadata is dropped) and may be at most 2048 pixels on a side. The type must match the bytes: a file renamed to `.png` is rejected.
- SVG files are sanitised on upload. Scripts, event handlers, `foreignObject`, embedded or external images, external references, `@import`, DOCTYPE/entity declarations and animation elements are removed or the file is rejected. What you keep is plain vector artwork. The sanitised file is what is served, with `Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'` and `X-Content-Type-Options: nosniff`.
- Without a custom logo but with a custom name, the UI shows the built-in OpenArtifact mark next to your name as text, never the OpenArtifact wordmark as if it were yours.
- Pick a favicon of at least 192x192 pixels (512x512 recommended) if people will install the UI as an app: it is the only icon in the generated web manifest and browsers refuse to offer installation for smaller ones. An SVG favicon works too. With an SVG favicon the iOS home-screen icon is dropped instead of falling back to the OpenArtifact one.

### Accent colour in light and dark themes

The colour you pick is the starting point. The server derives one variant per theme so text stays readable (WCAG AA, 4.5:1) on the page background, on cards and on the tinted fill used for badges: a pale colour is darkened in the light theme, a dark one is lightened in the dark theme. The text colour on top of the accent (white or near-black) is chosen the same way. A colour that already reads well is left alone.

## Where to configure it

There are three layers, strongest first:

1. **Runtime, in the admin UI** (Administration, Branding) or through the API. Stored in the database, effective for every replica within seconds, no restart. Each field shows whether it is overridden and can be reset to the configured default.
2. **Configuration**: `branding.*` in the YAML file or `OPENARTIFACT_BRANDING_*` environment variables, read at startup. Logo and favicon values are **file paths**. An invalid value (bad colour, unreadable or unsafe image) stops the server at startup with a message naming the key.
3. **Built-in defaults**.

```yaml
branding:
  productName: Acme Artifacts
  logoLight: /etc/openartifact/brand/logo-light.svg
  logoDark: /etc/openartifact/brand/logo-dark.svg
  favicon: /etc/openartifact/brand/favicon.png
  primaryColor: "#0f766e"
  loginMessage: "Internal use only. Contact **IT** for access."
  supportUrl: https://support.example.com
  sourceUrl: https://git.example.com/acme/openartifact
```

Equivalent variables: `OPENARTIFACT_BRANDING_PRODUCT_NAME`, `_LOGO_LIGHT`, `_LOGO_DARK`, `_FAVICON`, `_PRIMARY_COLOR`, `_LOGIN_MESSAGE`, `_SUPPORT_URL`, `_SOURCE_URL`. All keys are listed in the [configuration reference](../reference/config.md#branding).

Resetting an override in the UI falls back to the configured value, not to OpenArtifact. "Reset all" removes every runtime override.

## API

| Method and path | Auth | Purpose |
|---|---|---|
| `GET /api/v1/branding` | none | The effective branding: `product_name`, `primary_color` and the per-theme `theme` palette, image URLs, `login_message`, `support_url`, `source_url` and a `revision`. Sent with an `ETag` (answers `304`); `Cache-Control: public` only when the request carries no `Authorization` header and no cookie |
| `GET /api/v1/branding/{logo-light,logo-dark,favicon}` | none | The image, `404` when none is set. The `v=<revision>` in the advertised URLs makes a changed image bypass caches |
| `GET /api/v1/admin/branding` | admin | `effective`, `defaults` (what a reset falls back to) and `overrides` (which fields are overridden) |
| `PUT /api/v1/admin/branding` | admin | Partial update of the text fields: a string sets the override, `null` removes it, an absent field is left alone. Validation errors are `400 validation_failed` |
| `DELETE /api/v1/admin/branding` | admin | Remove every override, images included |
| `PUT /api/v1/admin/branding/{logo-light,logo-dark,favicon}` | admin | Upload an image as the raw request body with `Content-Type: image/png` or `image/svg+xml`. `413` over 512 KiB |
| `DELETE /api/v1/admin/branding/{asset}` | admin | Remove one image override |

```bash
curl -X PUT -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"product_name":"Acme Artifacts","primary_color":"#0f766e"}' \
  https://artifacts.example.com/api/v1/admin/branding

curl -X PUT -H "Authorization: Bearer $TOKEN" -H 'Content-Type: image/svg+xml' \
  --data-binary @logo.svg https://artifacts.example.com/api/v1/admin/branding/logo-light
```

The public endpoints are reachable before sign-in because the login page needs them. Everything under `/api/v1/admin/branding` requires a global administrator.

## How it reaches the browser without a flash

The server rewrites `index.html` before it is sent: the `<title>`, the favicon links, the iOS home-screen title, the accent colour variables and a small JSON block with the branding. The page therefore never shows the OpenArtifact name or colours first. `/manifest.webmanifest` is also generated per request from the same settings. The service worker never caches either of them or any `/api` response, so a change is visible on the next page load. The offline page keeps a small copy of the name and colour in the browser's local storage, since it cannot ask the server while offline.

## Attribution (AGPL §13)

OpenArtifact is licensed under the GNU Affero General Public License v3.0. Section 13 says that if you modify the program and let users interact with it remotely over a network, you must offer those users the **corresponding source** of the version they are using. Changing the branding does not by itself remove that duty, and it must stay easy for any user of an instance to see which program they are using and where its source is. This page is not legal advice.

For that reason the UI always shows, and branding cannot remove or hide:

- **Powered by OpenArtifact vX.Y.Z**, in the About dialog (sidebar footer) and in the login page footer.
- A **Source code** link to `sourceUrl`.

If you run a modified build, point `sourceUrl` at the place where you publish the exact source of that build (a fork, a release tarball). If you run an unmodified release the default is correct. `sourceUrl` cannot be empty.

## Trademarks

The branding feature exists so that you do not have to use the OpenArtifact name or logo for your own product. See [TRADEMARKS.md](../../TRADEMARKS.md) for when the name and logo may be used. The brand assets shipped with the UI are licensed CC-BY-SA-4.0, which grants copyright permissions only and no trademark rights.
