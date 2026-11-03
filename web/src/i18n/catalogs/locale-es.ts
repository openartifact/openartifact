// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// The whole es catalog as one module, so one request (one chunk, no per-file stubs) loads it.
// `bun run i18n:check` fails when a catalog file of the language is not imported here.

import { mergeAreas } from '../translate'
import admin from '../locales/es/admin.json'
import artifact from '../locales/es/artifact.json'
import auth from '../locales/es/auth.json'
import branding from '../locales/es/branding.json'
import bundles from '../locales/es/bundles.json'
import core from '../locales/es/core.json'
import dashboard from '../locales/es/dashboard.json'
import federation from '../locales/es/federation.json'
import projects from '../locales/es/projects.json'
import repolist from '../locales/es/repolist.json'
import repos from '../locales/es/repos.json'
import search from '../locales/es/search.json'
import security from '../locales/es/security.json'
import settings from '../locales/es/settings.json'
import settingsx from '../locales/es/settingsx.json'
import shell from '../locales/es/shell.json'
import tiering from '../locales/es/tiering.json'
import tokens from '../locales/es/tokens.json'
import ui from '../locales/es/ui.json'

export default mergeAreas([admin, artifact, auth, branding, bundles, core, dashboard, federation, projects, repolist, repos, search, security, settings, settingsx, shell, tiering, tokens, ui])
