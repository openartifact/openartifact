// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { execFile } from 'node:child_process'
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { promisify } from 'node:util'

const run = promisify(execFile)

/** An isolated npm environment: its own user config (token) and cache, never ~/.npmrc. */
export interface NpmEnv {
  dir: string
  /** Runs the real `npm` client with the given arguments; rejects with npm's output on failure. */
  npm(args: string[], cwd?: string): Promise<string>
  dispose(): Promise<void>
}

/**
 * Writes the `.npmrc` the docs prescribe (`//<host>/repos/<repo>/:_authToken=<token>`)
 * into a private directory and returns a runner for the real npm client.
 */
export async function npmEnv(origin: string, repo: string, token: string): Promise<NpmEnv> {
  const dir = await mkdtemp(join(tmpdir(), 'oa-e2e-npm-'))
  const url = new URL(origin)
  const userconfig = join(dir, 'npmrc')
  await writeFile(userconfig, `//${url.host}/repos/${encodeURIComponent(repo)}/:_authToken=${token}\n`, { mode: 0o600 })
  return {
    dir,
    async npm(args, cwd = dir) {
      try {
        const { stdout } = await run('npm', args, {
          cwd,
          timeout: 60_000,
          env: {
            PATH: process.env.PATH,
            HOME: dir,
            npm_config_userconfig: userconfig,
            npm_config_cache: join(dir, 'cache'),
            npm_config_update_notifier: 'false',
            npm_config_fund: 'false',
            npm_config_audit: 'false',
          },
        })
        return stdout
      } catch (err) {
        const e = err as { stdout?: string; stderr?: string; message: string }
        throw new Error(`npm ${args.join(' ')} failed: ${e.message}\n${e.stdout ?? ''}\n${e.stderr ?? ''}`)
      }
    },
    dispose: () => rm(dir, { recursive: true, force: true }),
  }
}

/** Creates a minimal publishable npm package directory and returns its path. */
export async function writePackage(env: NpmEnv, name: string, version: string): Promise<string> {
  const pkgDir = join(env.dir, 'src', name)
  await mkdir(pkgDir, { recursive: true })
  await writeFile(join(pkgDir, 'package.json'), JSON.stringify({ name, version, description: 'OpenArtifact e2e fixture', main: 'index.js', license: 'MIT' }))
  await writeFile(join(pkgDir, 'index.js'), `module.exports = ${JSON.stringify(`${name}@${version}`)}\n`)
  return pkgDir
}
