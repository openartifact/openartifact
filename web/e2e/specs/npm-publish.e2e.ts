// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { mkdir, readFile } from 'node:fs/promises'
import { join } from 'node:path'
import { request, type APIRequestContext } from '@playwright/test'
import { ADMIN, requireEnv } from '../env'
import { expect, test } from '../fixtures'
import { adminApi, mintToken, uniqueName } from '../lib/api'
import { npmEnv, writePackage, type NpmEnv } from '../lib/npm'
import { activate, t } from '../lib/ui'

// The whole publish -> browse -> install -> quarantine story with the real npm
// client and an access token minted like a person would (session + CSRF). One
// repository and one package are shared by the serial tests below.
test.describe.configure({ mode: 'serial' })
test.use({ role: 'admin' })

const VERSION = '1.0.0'
let origin: string
let repo: string
let pkg: string
let token: string
let env: NpmEnv
let api: APIRequestContext
let packageId: string
let scanId: string

const registry = () => `${origin}/repos/${repo}/`
const tarballUrl = () => `${registry()}${pkg}/-/${pkg}-${VERSION}.tgz`

test.beforeAll(async () => {
  origin = requireEnv('OA_URL')
  repo = uniqueName('e2e-npm')
  pkg = uniqueName('e2e-pkg')
  api = await adminApi()
  const created = await api.post('/api/v1/repositories', { data: { name: repo, format: 'npm', public: false } })
  expect(created.ok(), await created.text()).toBe(true)
  token = await mintToken(ADMIN().username, ADMIN().password, uniqueName('e2e-token'))
  env = await npmEnv(origin, repo, token)
})

test.afterAll(async () => {
  await env?.dispose()
  await api?.dispose()
})

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
  const { packages } = (await list.json()) as { packages: Array<{ id: string; name: string; version: string }> }
  expect(packages.map((p) => `${p.name}@${p.version}`)).toEqual([`${pkg}@${VERSION}`])
  packageId = packages[0]!.id
})

test('the package page shows the version and the install instructions the docs describe', async ({ page }) => {
  await page.goto(`/repos/${repo}/packages/${pkg}`)
  await expect(page.getByRole('heading', { level: 1, name: pkg })).toBeVisible()
  await expect(page.getByTestId('version-select')).toContainText(VERSION)

  // The one-line install command and the registry it points at.
  const install = page.getByTestId('install-command')
  await expect(install).toHaveText(`npm install ${pkg}@${VERSION} --registry=${registry()}`)
  await expect(page.getByTestId('registry-url')).toHaveText(registry().replace(/\/$/, ''))

  // The client configuration is the docs' template with the real host and repository filled in.
  await page.getByText(t('artifact.install.configure')).click()
  const doc = await readFile(join(import.meta.dirname, '..', '..', '..', 'docs', 'formats', 'npm.md'), 'utf8')
  const hostPort = new URL(origin).host
  const expand = (s: string) => s.replaceAll('HOSTPORT', hostPort).replaceAll('HOST', origin).replaceAll('REPO', repo)
  const documented = doc
    .split('\n')
    .filter((l) => /^npm (config set|publish) /.test(l))
    .slice(0, 3)
    .map(expand)
  expect(documented).toHaveLength(3)
  const npmBlock = page.locator('pre').filter({ hasText: 'npm config set registry' }).first()
  await expect(npmBlock).toHaveText(`${documented[0]}\n${documented[1]}`)
  await expect(page.locator('pre').filter({ hasText: /^npm publish / }).first()).toHaveText(documented[2]!)
})

test('the install command shown in the UI installs the package with the real npm client', async ({ page }) => {
  await page.goto(`/repos/${repo}/packages/${pkg}`)
  const shown = (await page.getByTestId('install-command').innerText()).trim()
  expect(shown.startsWith('npm install ')).toBe(true)

  const project = join(env.dir, 'consumer')
  await mkdir(project, { recursive: true })
  await env.npm(['init', '-y'], project)
  // The command is "npm install <spec> --registry=<url>": run exactly its arguments.
  await env.npm(shown.slice('npm '.length).split(' '), project)
  const installed = await readFile(join(project, 'node_modules', pkg, 'index.js'), 'utf8')
  expect(installed).toContain(`${pkg}@${VERSION}`)
})

test.describe('quarantine', () => {
  const nativeDownload = async () => {
    const native = await request.newContext({ storageState: { cookies: [], origins: [] }, extraHTTPHeaders: { Authorization: `Bearer ${token}` } })
    try {
      const res = await native.get(tarballUrl())
      return { status: res.status(), body: await res.body() }
    } finally {
      await native.dispose()
    }
  }

  test('quarantining the version blocks every download path and the UI says so', async ({ page }) => {
    // A scan result anchors the quarantine; the license scan needs no external scanner.
    const scan = await api.post(`/api/v1/repositories/${repo}/license/scan`, { data: { artifact_id: packageId } })
    expect(scan.ok()).toBe(true)
    scanId = ((await scan.json()) as { id: string }).id
    expect((await nativeDownload()).status).toBe(200)

    await page.goto(`/repos/${repo}/packages/${pkg}`)
    await activate(page.getByTestId('artifact-menu'))
    await activate(page.getByTestId('action-quarantine'))
    await expect(page.getByText(tpVersion('artifact.quarantine.done', VERSION))).toBeVisible()

    // The npm client's tarball URL and the UI's download route both refuse.
    const blocked = await nativeDownload()
    expect(blocked.status).toBe(403)
    expect(JSON.parse(blocked.body.toString())).toMatchObject({ code: 'ARTIFACT_QUARANTINED' })
    const ui = await api.get(`/api/v1/repositories/${repo}/packages/${packageId}/download`)
    expect(ui.status()).toBe(403)

    // npm install itself fails.
    const project = join(env.dir, 'blocked-consumer')
    await mkdir(project, { recursive: true })
    await env.npm(['init', '-y'], project)
    await expect(env.npm(['install', `${pkg}@${VERSION}`, `--registry=${registry()}`, '--prefer-online'], project)).rejects.toThrow(/403|E403/)

    // The repository's security tab reports the quarantine.
    await page.goto(`/repos/${repo}?tab=security`)
    await expect(page.getByTestId('quarantined')).toHaveText(t('rl.security.quarantined'))
  })

  test('undoing the quarantine from its toast makes the version downloadable again', async ({ page }) => {
    await page.goto(`/repos/${repo}/packages/${pkg}`)
    // Release through the API, then quarantine again from the UI so its toast offers the way back.
    const release = await api.post(`/api/v1/repositories/${repo}/license/scans/${scanId}/quarantine`, { data: { quarantined: false } })
    expect(release.ok()).toBe(true)
    expect((await nativeDownload()).status).toBe(200)
    await activate(page.getByTestId('artifact-menu'))
    await activate(page.getByTestId('action-quarantine'))
    await expect(page.getByText(tpVersion('artifact.quarantine.done', VERSION))).toBeVisible()
    expect((await nativeDownload()).status).toBe(403)

    await activate(page.getByRole('button', { name: t('artifact.undo') }))
    await expect(page.getByText(tpVersion('artifact.quarantine.released', VERSION))).toBeVisible()
    expect((await nativeDownload()).status).toBe(200)
  })
})

function tpVersion(key: 'artifact.quarantine.done' | 'artifact.quarantine.released', version: string): string {
  return t(key).replace('{version}', version)
}
