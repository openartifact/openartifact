// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// The whole ja catalog as one module, so one request (one chunk, no per-file stubs) loads it.
// `bun run i18n:check` fails when a catalog file of the language is not imported here.

import { mergeAreas } from '../translate'
import admin from '../locales/ja/admin.json'
import artifact from '../locales/ja/artifact.json'
import auth from '../locales/ja/auth.json'
import branding from '../locales/ja/branding.json'
import bundles from '../locales/ja/bundles.json'
import core from '../locales/ja/core.json'
import dashboard from '../locales/ja/dashboard.json'
import federation from '../locales/ja/federation.json'
import projects from '../locales/ja/projects.json'
import repolist from '../locales/ja/repolist.json'
import repos from '../locales/ja/repos.json'
import search from '../locales/ja/search.json'
import security from '../locales/ja/security.json'
import settings from '../locales/ja/settings.json'
import settingsx from '../locales/ja/settingsx.json'
import shell from '../locales/ja/shell.json'
import tiering from '../locales/ja/tiering.json'
import tokens from '../locales/ja/tokens.json'
import ui from '../locales/ja/ui.json'

export default mergeAreas([admin, artifact, auth, branding, bundles, core, dashboard, federation, projects, repolist, repos, search, security, settings, settingsx, shell, tiering, tokens, ui])
