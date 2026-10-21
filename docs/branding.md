<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Logo

The "open crate" mark: a crate with its lid swung open and an artifact rising out.

| File | Use |
|---|---|
| [`logo.svg`](logo.svg) | Master, two-tone. Edit this one. |
| [`logo-mono.svg`](logo-mono.svg) | One colour (`currentColor`): print, single-colour backgrounds. |
| [`logo.png`](logo.png) | 144 px raster for the README and the Helm chart icon. |
| [`../web/public/favicon.svg`](../web/public/favicon.svg) | Browser tab icon, same geometry. |
| [`../web/src/components/AppLogo.vue`](../web/src/components/AppLogo.vue) | In-app mark (white on the primary tile). |

Colours: frame `#4F46E5`, artifact `#818CF8`. Geometry is a 32×32 grid; keep it unchanged so the mark stays crisp at 16 px.

After changing `logo.svg`, update the copies above and regenerate the PNG:

```sh
rsvg-convert -w 144 -h 144 docs/logo.svg -o docs/logo.png
```
