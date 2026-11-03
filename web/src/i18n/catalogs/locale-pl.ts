// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// The whole pl catalog as one module, so one request (one chunk, no per-file stubs) loads it.
// `bun run i18n:check` fails when a catalog file of the language is not imported here.

import { mergeAreas } from '../translate'
import admin from '../locales/pl/admin.json'
import artifact from '../locales/pl/artifact.json'
import auth from '../locales/pl/auth.json'
import branding from '../locales/pl/branding.json'
import bundles from '../locales/pl/bundles.json'
import core from '../locales/pl/core.json'
import dashboard from '../locales/pl/dashboard.json'
import federation from '../locales/pl/federation.json'
import projects from '../locales/pl/projects.json'
import repolist from '../locales/pl/repolist.json'
import repos from '../locales/pl/repos.json'
import search from '../locales/pl/search.json'
import security from '../locales/pl/security.json'
import settings from '../locales/pl/settings.json'
import settingsx from '../locales/pl/settingsx.json'
import shell from '../locales/pl/shell.json'
import tiering from '../locales/pl/tiering.json'
import tokens from '../locales/pl/tokens.json'
import ui from '../locales/pl/ui.json'

export default mergeAreas([admin, artifact, auth, branding, bundles, core, dashboard, federation, projects, repolist, repos, search, security, settings, settingsx, shell, tiering, tokens, ui])
