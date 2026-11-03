// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// The whole fr catalog as one module, so one request (one chunk, no per-file stubs) loads it.
// `bun run i18n:check` fails when a catalog file of the language is not imported here.

import { mergeAreas } from '../translate'
import admin from '../locales/fr/admin.json'
import artifact from '../locales/fr/artifact.json'
import auth from '../locales/fr/auth.json'
import branding from '../locales/fr/branding.json'
import bundles from '../locales/fr/bundles.json'
import core from '../locales/fr/core.json'
import dashboard from '../locales/fr/dashboard.json'
import federation from '../locales/fr/federation.json'
import projects from '../locales/fr/projects.json'
import repolist from '../locales/fr/repolist.json'
import repos from '../locales/fr/repos.json'
import search from '../locales/fr/search.json'
import security from '../locales/fr/security.json'
import settings from '../locales/fr/settings.json'
import settingsx from '../locales/fr/settingsx.json'
import shell from '../locales/fr/shell.json'
import tiering from '../locales/fr/tiering.json'
import tokens from '../locales/fr/tokens.json'
import ui from '../locales/fr/ui.json'

export default mergeAreas([admin, artifact, auth, branding, bundles, core, dashboard, federation, projects, repolist, repos, search, security, settings, settingsx, shell, tiering, tokens, ui])
