// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// The command palette's action registry. Every action is declared once: id,
// keywords, icon, the permission predicate (the rule the page behind it
// applies, so the palette never offers what the page would refuse), its label
// and what it does. An action is global ("Create access token"), scoped to a
// repository ("Open settings of web-npm") or to a language ("Change language
// to Italiano"). `palette.ts` turns the allowed ones into list items; the
// component only supplies the side effects through `ActionEnv`, so the
// registry is tested without a DOM.

import { clientSetup } from './installCommands'
import { atLeast } from './projectAccess'
import { SUPPORTED_LOCALES } from '../i18n/locales'
import { t, tp } from '../i18n'
import type { LanguageChoice } from '../composables/useLanguage'
import type { PaletteIcon } from './palette'

export type ActionId =
  | 'create-repo'
  | 'create-project'
  | 'create-token'
  | 'create-user'
  | 'create-bundle'
  | 'switch-theme'
  | 'sign-out'
  | 'change-language'
  | 'repo-settings'
  | 'repo-copy-install'

/** What the palette needs to know about a repository to offer actions on it; also what Recent stores. */
export interface RepoSubject {
  name: string
  format: string
  repoType: string
  /** Name of the owning project; `system` and empty never grant anything. */
  project?: string
}

/** The caller, as the permission predicates see it. */
export interface PaletteContext {
  isAdmin: boolean
  /** Admins, and maintainers or owners of a project, may create repositories. */
  canCreateRepo: boolean
  /** Admins, and everybody while self-service project creation is on (the API's `can_create`). */
  canCreateProject: boolean
  /** The caller's role per project name. */
  projectRoles: Readonly<Record<string, string>>
  theme: 'light' | 'dark'
  /** The language picker's current value. */
  language: LanguageChoice
}

/** The side effects an action may have; the palette component provides them. */
export interface ActionEnv {
  /** Origin the client snippets are rendered for. */
  origin: string
  go(path: string): Promise<unknown> | void
  copy(text: string): Promise<boolean>
  toggleTheme(): void
  setLanguage(choice: LanguageChoice): Promise<boolean>
  signOut(): Promise<void>
  toast: { success(title: string): void; error(title: string, detail?: string): void }
}

interface ActionDef<S> {
  id: ActionId
  /** Extra search terms; never shown. */
  keywords: string
  icon: PaletteIcon
  /** Whether running it is worth repeating from the Recent list. */
  remember: boolean
  /** The permission predicate: false hides the action. */
  allowed(ctx: PaletteContext, subject: S): boolean
  label(ctx: PaletteContext, subject: S): string
  description?(ctx: PaletteContext, subject: S): string | undefined
  run(env: ActionEnv, subject: S): void | Promise<void>
}

/** An action with its subject chosen: what a palette item runs. */
export interface BoundAction {
  id: ActionId
  /** Unique per subject; the item id is derived from it. */
  key: string
  title: string
  description?: string
  keywords: string
  icon: PaletteIcon
  remember: boolean
  /** The repository of a repository action, kept in Recent. */
  repo?: RepoSubject
  run(env: ActionEnv): void | Promise<void>
}

/** A remembered action run, as stored: ids and subjects only, never labels (they follow the language). */
export interface ActionRecent {
  kind: 'action'
  action: string
  repo?: RepoSubject
}

const enc = encodeURIComponent

// ---------------------------------------------------------------------------
// Registry

const GLOBAL_ACTIONS: readonly ActionDef<void>[] = [
  {
    id: 'create-repo',
    keywords: 'new add repo',
    icon: 'plus',
    remember: true,
    allowed: (ctx) => ctx.canCreateRepo,
    label: () => t('shell.action.createRepo'),
    run: (env) => void env.go('/repos?new=1'),
  },
  {
    id: 'create-project',
    keywords: 'new add team',
    icon: 'projects',
    remember: true,
    allowed: (ctx) => ctx.canCreateProject,
    label: () => t('shell.action.createProject'),
    run: (env) => void env.go('/projects?new=1'),
  },
  {
    id: 'create-token',
    keywords: 'new personal ci pat api key',
    icon: 'ticket',
    remember: true,
    allowed: () => true,
    label: () => t('shell.action.createToken'),
    run: (env) => void env.go('/settings/tokens?new=1'),
  },
  {
    id: 'create-user',
    keywords: 'new invite add account',
    icon: 'user-plus',
    remember: true,
    allowed: (ctx) => ctx.isAdmin,
    label: () => t('shell.action.createUser'),
    run: (env) => void env.go('/settings/users?new=1'),
  },
  {
    id: 'create-bundle',
    keywords: 'new seal release',
    icon: 'tag',
    remember: true,
    allowed: (ctx) => ctx.isAdmin,
    label: () => t('shell.action.createBundle'),
    run: (env) => void env.go('/bundles?new=1'),
  },
  {
    id: 'switch-theme',
    keywords: 'theme appearance dark light mode toggle',
    icon: 'theme',
    remember: true,
    allowed: () => true,
    label: (ctx) => t(ctx.theme === 'dark' ? 'shell.action.themeLight' : 'shell.action.themeDark'),
    run: (env) => env.toggleTheme(),
  },
  {
    id: 'sign-out',
    keywords: 'log out logout',
    icon: 'sign-out',
    remember: false,
    allowed: () => true,
    label: () => t('shell.action.signOut'),
    run: (env) => env.signOut(),
  },
]

const REPO_ACTIONS: readonly ActionDef<RepoSubject>[] = [
  {
    id: 'repo-settings',
    keywords: 'settings configure configuration edit repo',
    icon: 'settings',
    remember: true,
    // The repository page shows its Settings tab to `session.isAdmin || permissions.admin`, and the server
    // grants `admin` to global administrators and to maintainers or owners of the repository's project.
    allowed: (ctx, repo) => ctx.isAdmin || isProjectMaintainer(ctx, repo),
    label: (_ctx, repo) => tp('shell.action.repoSettings', { name: repo.name }),
    run: (env, repo) => void env.go(`/repos/${enc(repo.name)}?tab=settings`),
  },
  {
    id: 'repo-copy-install',
    keywords: 'copy install configure setup client command snippet repo',
    icon: 'clipboard',
    remember: true,
    // Everything that lists a repository lets its reader see how to connect to it.
    allowed: () => true,
    label: (_ctx, repo) => tp('shell.action.repoCopyInstall', { name: repo.name }),
    // The client's name does not depend on the origin the snippet is rendered for.
    description: (_ctx, repo) => firstSetup(repo, '')?.client,
    async run(env, repo) {
      const setup = firstSetup(repo, env.origin)
      if (setup && (await env.copy(setup.configure))) {
        env.toast.success(tp('shell.action.repoCopied', { name: repo.name }))
      } else {
        env.toast.error(t('ui.copy.failed'), t('ui.copy.failedHint'))
      }
    },
  },
]

const LANGUAGE_ACTION: ActionDef<LanguageChoice> = {
  id: 'change-language',
  keywords: 'language locale translation lingua',
  icon: 'language',
  remember: false,
  allowed: (ctx, choice) => choice !== ctx.language,
  label: (_ctx, choice) => (choice === 'auto' ? t('shell.action.languageAuto') : tp('shell.action.language', { language: languageName(choice) })),
  run: async (env, choice) => {
    await env.setLanguage(choice)
  },
}

function isProjectMaintainer(ctx: PaletteContext, repo: RepoSubject): boolean {
  const project = repo.project
  if (!project || project === 'system') return false
  return atLeast(ctx.projectRoles[project], 'maintainer')
}

/** The first client's configure snippet: what the repository page's Connect panel shows first. */
function firstSetup(repo: RepoSubject, origin: string) {
  return clientSetup(repo.format, origin, repo.name, repo.repoType)[0]
}

function languageName(choice: LanguageChoice): string {
  return SUPPORTED_LOCALES.find((l) => l.tag === choice)?.name ?? choice
}

// ---------------------------------------------------------------------------
// Binding

function bind<S>(def: ActionDef<S>, ctx: PaletteContext, subject: S, key: string, extra: Partial<BoundAction> = {}): BoundAction | null {
  if (!def.allowed(ctx, subject)) return null
  return {
    id: def.id,
    key,
    title: def.label(ctx, subject),
    description: def.description?.(ctx, subject),
    keywords: def.keywords,
    icon: def.icon,
    remember: def.remember,
    run: (env) => def.run(env, subject),
    ...extra,
  }
}

const present = (b: BoundAction | null): b is BoundAction => b !== null

/** The global actions the caller may run, in registry order. */
export function globalActions(ctx: PaletteContext): BoundAction[] {
  return GLOBAL_ACTIONS.map((def) => bind(def, ctx, undefined, def.id)).filter(present)
}

/** "Change language to …" for every language but the current one. */
export function languageActions(ctx: PaletteContext): BoundAction[] {
  const choices: LanguageChoice[] = ['auto', ...SUPPORTED_LOCALES.map((l) => l.tag)]
  return choices
    .map((choice) =>
      bind(LANGUAGE_ACTION, ctx, choice, `${LANGUAGE_ACTION.id}:${choice}`, {
        keywords: `${LANGUAGE_ACTION.keywords} ${choice} ${choice === 'auto' ? 'browser default' : languageName(choice)}`,
      }),
    )
    .filter(present)
}

/** The actions the caller may run on `repo`. */
export function repoActions(ctx: PaletteContext, repo: RepoSubject): BoundAction[] {
  return REPO_ACTIONS.map((def) =>
    bind(def, ctx, repo, `${def.id}:${repo.name}`, { keywords: `${def.keywords} ${repo.name}`, repo }),
  ).filter(present)
}

/** What Recent stores of a run action; null for the ones not worth repeating. */
export function toActionRecent(action: BoundAction): ActionRecent | null {
  if (!action.remember) return null
  return action.repo ? { kind: 'action', action: action.id, repo: action.repo } : { kind: 'action', action: action.id }
}

/** The key that makes a remembered run unique, so repeating it moves it to the top instead of adding a copy. */
export function actionRecentKey(r: ActionRecent): string {
  return `action:${r.action}:${r.repo?.name ?? ''}`
}

/** A remembered run as an action again; null when it is unknown or the caller may not run it any more. */
export function bindRecentAction(r: ActionRecent, ctx: PaletteContext): BoundAction | null {
  if (r.repo) {
    const def = REPO_ACTIONS.find((d) => d.id === r.action)
    return def ? bind(def, ctx, r.repo, `${def.id}:${r.repo.name}`, { keywords: `${def.keywords} ${r.repo.name}`, repo: r.repo }) : null
  }
  const def = GLOBAL_ACTIONS.find((d) => d.id === r.action)
  return def ? bind(def, ctx, undefined, def.id) : null
}

/** Whether `v` has the shape of a stored repository subject. */
export function isRepoSubject(v: unknown): v is RepoSubject {
  if (typeof v !== 'object' || v === null) return false
  const r = v as Record<string, unknown>
  return (
    typeof r.name === 'string' &&
    r.name !== '' &&
    typeof r.format === 'string' &&
    typeof r.repoType === 'string' &&
    (r.project === undefined || typeof r.project === 'string')
  )
}

// ---------------------------------------------------------------------------
// Query

function words(s: string): string[] {
  return s.toLowerCase().split(/[\s\-_/.:@]+/).filter((w) => w.length > 1)
}

/**
 * The part of the query that names a repository: the text without the words
 * that belong to repository actions ("settings web" → "web"), so a verb typed
 * before the name still finds the repository. Text made of such words only is
 * kept whole (a repository may be called "settings").
 */
export function repoSearchTerm(text: string, ctx: PaletteContext): string {
  const vocabulary = new Set<string>()
  const blank: RepoSubject = { name: '', format: '', repoType: 'hosted' }
  for (const def of REPO_ACTIONS) for (const w of words(`${def.keywords} ${def.label(ctx, blank)}`)) vocabulary.add(w)
  const rest = text.split(/\s+/).filter((w) => w !== '' && !vocabulary.has(w.toLowerCase()))
  return rest.length > 0 ? rest.join(' ') : text
}
