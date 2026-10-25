// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// White-label branding: admin page (`adm.branding.*`), attribution (`brand.*`) and About dialog (`about.*`).
// Strings with {product} take the instance's product name; the attribution strings never do.
export const enBranding = {
  // Login page
  'login.headingProduct': 'Sign in to {product}',

  // Attribution: always names the upstream software, whatever the instance is called
  'brand.poweredBy': 'Powered by OpenArtifact',
  'brand.sourceCode': 'Source code',
  'brand.license': 'Licensed under the GNU AGPL-3.0-or-later',
  'brand.licenseShort': 'AGPL-3.0-or-later',

  // About dialog
  'about.open': 'About',
  'about.title': 'About {product}',
  'about.upstream': 'OpenArtifact project',
  'about.docs': 'Documentation',
  'about.apiReference': 'API reference',
  'about.support': 'Support',
  'about.sourceHint': 'The source code of the version running here is available under the AGPL.',

  // Admin page
  'nav.branding': 'Branding',
  'adm.branding.title': 'Branding',
  'adm.branding.description': 'Name, colour, logos and login message of this instance. Changes apply to everyone immediately.',
  'adm.branding.loading': 'Loading branding…',
  'adm.branding.loadFailed': 'Failed to load the branding settings',
  'adm.branding.save': 'Save changes',
  'adm.branding.saved': 'Branding saved',
  'adm.branding.saveFailed': 'Failed to save the branding',
  'adm.branding.fixErrors': 'Fix the highlighted fields first',
  'adm.branding.resetAll': 'Reset to defaults',
  'adm.branding.resetAllTitle': 'Reset all branding?',
  'adm.branding.resetAllBody':
    'Every override and uploaded image is removed and the instance goes back to its configured defaults. This applies to all users at once.',
  'adm.branding.resetAllDone': 'Branding reset to defaults',
  'adm.branding.resetFailed': 'Failed to reset the branding',
  'adm.branding.state.overridden': 'Overridden',
  'adm.branding.state.default': 'Default',
  'adm.branding.resetField': 'Reset to default',
  'adm.branding.defaultValue': 'Default: {value}',
  'adm.branding.defaultBuiltin': 'built-in',
  'adm.branding.defaultNone': 'none',

  'adm.branding.section.identity': 'Name and colour',
  'adm.branding.section.login': 'Login page',
  'adm.branding.section.links': 'Links',
  'adm.branding.section.images': 'Logos and icon',

  'adm.branding.field.productName': 'Product name',
  'adm.branding.field.productNameHint': 'Shown in the window title, the sidebar, the login page and the installed app. 1 to 64 characters.',
  'adm.branding.field.color': 'Primary colour',
  'adm.branding.field.colorHint': 'Buttons, links and highlights. The colour is adjusted per theme so text stays readable. Leave empty for the built-in colour.',
  'adm.branding.field.colorHex': 'Primary colour as #rrggbb',
  'adm.branding.field.colorPicker': 'Pick the primary colour',
  'adm.branding.field.message': 'Login message',
  'adm.branding.field.messageHint':
    'Shown above the sign-in form. Supports **bold**, *italic*, `code` and [links](https://example.com) (http, https, mailto). A blank line starts a new paragraph.',
  'adm.branding.field.support': 'Support link',
  'adm.branding.field.supportHint': 'http(s) or mailto URL, shown in the About dialog. Empty hides it.',
  'adm.branding.field.source': 'Source code URL',
  'adm.branding.field.sourceHint':
    'Must point at the corresponding source of the version you run (AGPL-3.0 section 13). The "Source code" link and the OpenArtifact attribution are always shown and cannot be hidden.',

  'adm.branding.preview.title': 'Preview',
  'adm.branding.preview.approx': 'Approximate while editing; the saved colours are adjusted by the server.',
  'adm.branding.preview.light': 'Light',
  'adm.branding.preview.dark': 'Dark',
  'adm.branding.preview.button': 'Button',
  'adm.branding.preview.link': 'Link',
  'adm.branding.preview.chip': 'Highlight',
  'adm.branding.preview.messageEmpty': 'No login message.',
  'adm.branding.swatch.title': 'Saved colours',
  'adm.branding.swatch.primary': 'Primary',
  'adm.branding.swatch.contrast': 'Text on primary',
  'adm.branding.swatch.soft': 'Tint',
  'adm.branding.swatch.none': 'No custom colour: the built-in palette is used.',

  'adm.branding.image.logoLight': 'Logo for light theme',
  'adm.branding.image.logoDark': 'Logo for dark theme',
  'adm.branding.image.favicon': 'Favicon',
  'adm.branding.image.logoLightHint': 'Replaces the built-in lockup. If only one logo is set it is used for both themes.',
  'adm.branding.image.logoDarkHint': 'Shown on dark surfaces.',
  'adm.branding.image.faviconHint': 'Browser tab and installed app icon; also the sidebar mark of a collapsed menu.',
  'adm.branding.image.formats': 'PNG or SVG, up to 512 KiB.',
  'adm.branding.image.upload': 'Upload',
  'adm.branding.image.replace': 'Replace',
  'adm.branding.image.remove': 'Remove',
  'adm.branding.image.notSet': 'Not set: the built-in artwork is used.',
  'adm.branding.image.uploaded': 'Image uploaded',
  'adm.branding.image.removed': 'Image removed',
  'adm.branding.image.uploadFailed': 'Failed to upload the image',
  'adm.branding.image.removeFailed': 'Failed to remove the image',
  'adm.branding.image.alt': 'Current {slot}',

  'adm.branding.error.nameRequired': 'The product name is required.',
  'adm.branding.error.nameLength': 'The product name can have at most 64 characters.',
  'adm.branding.error.nameControl': 'The product name cannot contain control characters.',
  'adm.branding.error.color': 'Use a colour in the form #rrggbb.',
  'adm.branding.error.messageLength': 'The login message can have at most 2000 characters.',
  'adm.branding.error.supportUrl': 'Use an http(s) or mailto URL, or leave it empty.',
  'adm.branding.error.sourceRequired': 'The source code URL cannot be empty.',
  'adm.branding.error.sourceUrl': 'Use an http(s) URL.',
  'adm.branding.error.imageType': 'Only PNG and SVG images are accepted.',
  'adm.branding.error.imageSize': 'The image is larger than 512 KiB.',
  'adm.branding.error.imageEmpty': 'The file is empty.',
} as const
