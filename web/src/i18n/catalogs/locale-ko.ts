// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// The whole ko catalog as one module, so one request (one chunk, no per-file stubs) loads it.
// `bun run i18n:check` fails when a catalog file of the language is not imported here.

import { mergeAreas } from '../translate'
import admin from '../locales/ko/admin.json'
import artifact from '../locales/ko/artifact.json'
import auth from '../locales/ko/auth.json'
import branding from '../locales/ko/branding.json'
import bundles from '../locales/ko/bundles.json'
import core from '../locales/ko/core.json'
import dashboard from '../locales/ko/dashboard.json'
import federation from '../locales/ko/federation.json'
import projects from '../locales/ko/projects.json'
import repolist from '../locales/ko/repolist.json'
import repos from '../locales/ko/repos.json'
import search from '../locales/ko/search.json'
import security from '../locales/ko/security.json'
import settings from '../locales/ko/settings.json'
import settingsx from '../locales/ko/settingsx.json'
import shell from '../locales/ko/shell.json'
import tiering from '../locales/ko/tiering.json'
import tokens from '../locales/ko/tokens.json'
import ui from '../locales/ko/ui.json'

export default mergeAreas([admin, artifact, auth, branding, bundles, core, dashboard, federation, projects, repolist, repos, search, security, settings, settingsx, shell, tiering, tokens, ui])
