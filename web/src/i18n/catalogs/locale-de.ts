// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// The whole de catalog as one module, so one request (one chunk, no per-file stubs) loads it.
// `bun run i18n:check` fails when a catalog file of the language is not imported here.

import { mergeAreas } from '../translate'
import admin from '../locales/de/admin.json'
import artifact from '../locales/de/artifact.json'
import auth from '../locales/de/auth.json'
import branding from '../locales/de/branding.json'
import bundles from '../locales/de/bundles.json'
import core from '../locales/de/core.json'
import dashboard from '../locales/de/dashboard.json'
import federation from '../locales/de/federation.json'
import projects from '../locales/de/projects.json'
import repolist from '../locales/de/repolist.json'
import repos from '../locales/de/repos.json'
import search from '../locales/de/search.json'
import security from '../locales/de/security.json'
import settings from '../locales/de/settings.json'
import settingsx from '../locales/de/settingsx.json'
import shell from '../locales/de/shell.json'
import tiering from '../locales/de/tiering.json'
import tokens from '../locales/de/tokens.json'
import ui from '../locales/de/ui.json'

export default mergeAreas([admin, artifact, auth, branding, bundles, core, dashboard, federation, projects, repolist, repos, search, security, settings, settingsx, shell, tiering, tokens, ui])
