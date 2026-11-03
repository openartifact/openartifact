// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// The whole uk catalog as one module, so one request (one chunk, no per-file stubs) loads it.
// `bun run i18n:check` fails when a catalog file of the language is not imported here.

import { mergeAreas } from '../translate'
import admin from '../locales/uk/admin.json'
import artifact from '../locales/uk/artifact.json'
import auth from '../locales/uk/auth.json'
import branding from '../locales/uk/branding.json'
import bundles from '../locales/uk/bundles.json'
import core from '../locales/uk/core.json'
import dashboard from '../locales/uk/dashboard.json'
import federation from '../locales/uk/federation.json'
import projects from '../locales/uk/projects.json'
import repolist from '../locales/uk/repolist.json'
import repos from '../locales/uk/repos.json'
import search from '../locales/uk/search.json'
import security from '../locales/uk/security.json'
import settings from '../locales/uk/settings.json'
import settingsx from '../locales/uk/settingsx.json'
import shell from '../locales/uk/shell.json'
import tiering from '../locales/uk/tiering.json'
import tokens from '../locales/uk/tokens.json'
import ui from '../locales/uk/ui.json'

export default mergeAreas([admin, artifact, auth, branding, bundles, core, dashboard, federation, projects, repolist, repos, search, security, settings, settingsx, shell, tiering, tokens, ui])
