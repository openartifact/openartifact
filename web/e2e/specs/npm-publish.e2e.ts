// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import { mkdir, readFile } from 'node:fs/promises'
import { join } from 'node:path'
import { ADMIN, requireEnv } from '../env'
import { adminApi, mintToken, tokenApi, uniqueName } from '../lib/api'
import { testAs } from '../lib/browser'
import { npmEnv, writePackage, type NpmEnv } from '../lib/npm'
import { expectLoc, t } from '../lib/ui'

// The whole publish -> browse -> install -> quarantine story with the real npm
// client and an access token minted like a person would (session + CSRF). One
// repository and one package are shared by the tests below, which run in order.
const VERSION = '1.0.0'
let origin: string
let repo: string
let pkg: string
let token: string
let env: NpmEnv
let packageId: string
let scanId: string

const api = adminApi()
const registry = () => `${origin}/repos/${repo}/`
const tarballUrl = () => `${registry()}${pkg}/-/${pkg}-${VERSION}.tgz`

beforeAll(async () => {
  origin = requireEnv('OA_URL')
  repo = uniqueName('e2e-npm')
  pkg = uniqueName('e2e-pkg')
  const created = await api.post('/api/v1/repositories', { data: { name: repo, format: 'npm', public: false } })
  expect(created.ok, created.text()).toBe(true)
  token = await mintToken(ADMIN().username, ADMIN().password, uniqueName('e2e-token'))
  env = await npmEnv(origin, repo, token)
})

afterAll(async () => {
  await env?.dispose()
})

describe('npm client', () => {
  test('npm publish with an access token is accepted, a missing or wrong token is refused', async () => {
    const dir = await writePackage(env, pkg, VERSION)

    const anon = await npmEnv(origin, repo, 'oa_pat_not-a-real-token')
    try {
      await expect(anon.npm(['publish', '--registry', registry()], dir)).rejects.toThrow(/40[13]|E401|E403|ENEEDAUTH/)
    } finally {
      await anon.dispose()
    }

    await env.npm(['publish', '--registry', registry()], dir)
    const view = await env.npm(['view', pkg, 'version', '--registry', registry()])
    expect(view.trim()).toBe(VERSION)

    const list = await api.get(`/api/v1/repositories/${repo}/packages`)
    const { packages } = list.json<{ packages: Array<{ id: string; name: string; version: string }> }>()
    expect(packages.map((p) => `${p.name}@${p.version}`)).toEqual([`${pkg}@${VERSION}`])
    packageId = packages[0]!.id
  }, 120_000)
})

describe('package page', () => {
  const it = testAs('admin')

  it('shows the version and the install instructions the docs describe', async (ui) => {
    await ui.goto(`/repos/${repo}/packages/${pkg}`)
    await expectLoc(ui.getByRole('heading', { level: 1, name: pkg })).toBeVisible()
    await expectLoc(ui.getByTestId('version-select')).toContainText(VERSION)

    // The one-line install command and the registry it points at.
    await expectLoc(ui.getByTestId('install-command')).toHaveText(`npm install ${pkg}@${VERSION} --registry=${registry()}`)
    await expectLoc(ui.getByTestId('registry-url')).toHaveText(registry().replace(/\/$/, ''))

    // The client configuration is the docs' template with the real host and repository filled in.
    await ui.getByText(t('artifact.install.configure')).click()
    const doc = await readFile(join(import.meta.dirname, '..', '..', '..', 'docs', 'formats', 'npm.md'), 'utf8')
    const hostPort = new URL(origin).host
    const expand = (s: string) => s.replaceAll('HOSTPORT', hostPort).replaceAll('HOST', origin).replaceAll('REPO', repo)
    const documented = doc
      .split('\n')
      .filter((l) => /^npm (config set|publish) /.test(l))
      .slice(0, 3)
      .map(expand)
    expect(documented).toHaveLength(3)
    await expectLoc(ui.locator('pre').filter({ hasText: 'npm config set registry' }).first()).toHaveText(`${documented[0]}\n${documented[1]}`)
    await expectLoc(ui.locator('pre').filter({ hasText: /^npm publish / }).first()).toHaveText(documented[2]!)
  })

  it('has an install command that installs the package with the real npm client', async (ui) => {
    await ui.goto(`/repos/${repo}/packages/${pkg}`)
    await expectLoc(ui.getByTestId('install-command')).toContainText('npm install ')
    const shown = await ui.getByTestId('install-command').text()
    expect(shown.startsWith('npm install ')).toBe(true)

    const project = join(env.dir, 'consumer')
    await mkdir(project, { recursive: true })
    await env.npm(['init', '-y'], project)
    // The command is "npm install <spec> --registry=<url>": run exactly its arguments.
    await env.npm(shown.slice('npm '.length).split(' '), project)
    const installed = await readFile(join(project, 'node_modules', pkg, 'index.js'), 'utf8')
    expect(installed).toContain(`${pkg}@${VERSION}`)
  })
})

describe('quarantine', () => {
  const it = testAs('admin')
  const nativeDownload = async () => {
    const res = await tokenApi(token).get(tarballUrl())
    return { status: res.status, body: res.body }
  }

  it('quarantining the version blocks every download path and the UI says so', async (ui) => {
    // A scan result anchors the quarantine; the license scan needs no external scanner.
    const scan = await api.post(`/api/v1/repositories/${repo}/license/scan`, { data: { artifact_id: packageId } })
    expect(scan.ok).toBe(true)
    scanId = scan.json<{ id: string }>().id
    expect((await nativeDownload()).status).toBe(200)

    await ui.goto(`/repos/${repo}/packages/${pkg}`)
    await ui.getByTestId('artifact-menu').click()
    await ui.getByTestId('action-quarantine').click()
    await expectLoc(ui.getByText(tpVersion('artifact.quarantine.done', VERSION))).toBeVisible()

    // The npm client's tarball URL and the UI's download route both refuse.
    const blocked = await nativeDownload()
    expect(blocked.status).toBe(403)
    expect(JSON.parse(blocked.body.toString())).toMatchObject({ code: 'ARTIFACT_QUARANTINED' })
    const download = await api.get(`/api/v1/repositories/${repo}/packages/${packageId}/download`)
    expect(download.status).toBe(403)

    // npm install itself fails.
    const project = join(env.dir, 'blocked-consumer')
    await mkdir(project, { recursive: true })
    await env.npm(['init', '-y'], project)
    await expect(env.npm(['install', `${pkg}@${VERSION}`, `--registry=${registry()}`, '--prefer-online'], project)).rejects.toThrow(/403|E403/)

    // The repository's security tab reports the quarantine.
    await ui.goto(`/repos/${repo}?tab=security`)
    await expectLoc(ui.getByTestId('quarantined')).toHaveText(t('rl.security.quarantined'))
  }, 120_000)

  it('undoing the quarantine from its toast makes the version downloadable again', async (ui) => {
    await ui.goto(`/repos/${repo}/packages/${pkg}`)
    // Release through the API, then quarantine again from the UI so its toast offers the way back.
    const release = await api.post(`/api/v1/repositories/${repo}/license/scans/${scanId}/quarantine`, { data: { quarantined: false } })
    expect(release.ok).toBe(true)
    expect((await nativeDownload()).status).toBe(200)
    await ui.getByTestId('artifact-menu').click()
    await ui.getByTestId('action-quarantine').click()
    await expectLoc(ui.getByText(tpVersion('artifact.quarantine.done', VERSION))).toBeVisible()
    expect((await nativeDownload()).status).toBe(403)

    await ui.getByRole('button', { name: t('artifact.undo') }).click()
    await expectLoc(ui.getByText(tpVersion('artifact.quarantine.released', VERSION))).toBeVisible()
    expect((await nativeDownload()).status).toBe(200)
  })
})

function tpVersion(key: 'artifact.quarantine.done' | 'artifact.quarantine.released', version: string): string {
  return t(key).replace('{version}', version)
}
