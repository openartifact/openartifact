// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// The whole it catalog as one module, so one request (one chunk, no per-file stubs) loads it.
// `bun run i18n:check` fails when a catalog file of the language is not imported here.

import { mergeAreas } from '../translate'
import admin from '../locales/it/admin.json'
import artifact from '../locales/it/artifact.json'
import auth from '../locales/it/auth.json'
import branding from '../locales/it/branding.json'
import bundles from '../locales/it/bundles.json'
import core from '../locales/it/core.json'
import dashboard from '../locales/it/dashboard.json'
import federation from '../locales/it/federation.json'
import projects from '../locales/it/projects.json'
import repolist from '../locales/it/repolist.json'
import repos from '../locales/it/repos.json'
import search from '../locales/it/search.json'
import security from '../locales/it/security.json'
import settings from '../locales/it/settings.json'
import settingsx from '../locales/it/settingsx.json'
import shell from '../locales/it/shell.json'
import tiering from '../locales/it/tiering.json'
import tokens from '../locales/it/tokens.json'
import ui from '../locales/it/ui.json'

export default mergeAreas([admin, artifact, auth, branding, bundles, core, dashboard, federation, projects, repolist, repos, search, security, settings, settingsx, shell, tiering, tokens, ui])
