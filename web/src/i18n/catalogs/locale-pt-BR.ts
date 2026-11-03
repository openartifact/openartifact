// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// The whole pt-BR catalog as one module, so one request (one chunk, no per-file stubs) loads it.
// `bun run i18n:check` fails when a catalog file of the language is not imported here.

import { mergeAreas } from '../translate'
import admin from '../locales/pt-BR/admin.json'
import artifact from '../locales/pt-BR/artifact.json'
import auth from '../locales/pt-BR/auth.json'
import branding from '../locales/pt-BR/branding.json'
import bundles from '../locales/pt-BR/bundles.json'
import core from '../locales/pt-BR/core.json'
import dashboard from '../locales/pt-BR/dashboard.json'
import federation from '../locales/pt-BR/federation.json'
import projects from '../locales/pt-BR/projects.json'
import repolist from '../locales/pt-BR/repolist.json'
import repos from '../locales/pt-BR/repos.json'
import search from '../locales/pt-BR/search.json'
import security from '../locales/pt-BR/security.json'
import settings from '../locales/pt-BR/settings.json'
import settingsx from '../locales/pt-BR/settingsx.json'
import shell from '../locales/pt-BR/shell.json'
import tiering from '../locales/pt-BR/tiering.json'
import tokens from '../locales/pt-BR/tokens.json'
import ui from '../locales/pt-BR/ui.json'

export default mergeAreas([admin, artifact, auth, branding, bundles, core, dashboard, federation, projects, repolist, repos, search, security, settings, settingsx, shell, tiering, tokens, ui])
