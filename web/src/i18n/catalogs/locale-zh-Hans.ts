// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// The whole zh-Hans catalog as one module, so one request (one chunk, no per-file stubs) loads it.
// `bun run i18n:check` fails when a catalog file of the language is not imported here.

import { mergeAreas } from '../translate'
import admin from '../locales/zh-Hans/admin.json'
import artifact from '../locales/zh-Hans/artifact.json'
import auth from '../locales/zh-Hans/auth.json'
import branding from '../locales/zh-Hans/branding.json'
import bundles from '../locales/zh-Hans/bundles.json'
import core from '../locales/zh-Hans/core.json'
import dashboard from '../locales/zh-Hans/dashboard.json'
import federation from '../locales/zh-Hans/federation.json'
import projects from '../locales/zh-Hans/projects.json'
import repolist from '../locales/zh-Hans/repolist.json'
import repos from '../locales/zh-Hans/repos.json'
import search from '../locales/zh-Hans/search.json'
import security from '../locales/zh-Hans/security.json'
import settings from '../locales/zh-Hans/settings.json'
import settingsx from '../locales/zh-Hans/settingsx.json'
import shell from '../locales/zh-Hans/shell.json'
import tiering from '../locales/zh-Hans/tiering.json'
import tokens from '../locales/zh-Hans/tokens.json'
import ui from '../locales/zh-Hans/ui.json'

export default mergeAreas([admin, artifact, auth, branding, bundles, core, dashboard, federation, projects, repolist, repos, search, security, settings, settingsx, shell, tiering, tokens, ui])
