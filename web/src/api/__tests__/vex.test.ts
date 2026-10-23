// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, type Mock } from 'bun:test'
import {
  deleteVexStatement,
  downloadVexDocument,
  listVexStatements,
  putVexStatement,
  vexDocumentUrl,
  VEX_JUSTIFICATIONS,
  VEX_STATUSES,
} from '../vex'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

let spy: Mock<typeof fetch>
afterEach(() => spy?.mockRestore())

const lastUrl = (): string => String(spy.mock.calls.at(-1)![0])
const lastInit = (): RequestInit => spy.mock.calls.at(-1)![1] as RequestInit

describe('vex api', () => {
  it('lists the decisions of a repository, optionally of one artifact', async () => {
    const stmt = { id: 's1', artifact_id: 'a', vuln_id: 'CVE-1', status: 'affected' }
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => json({ statements: [stmt] }))
    expect(await listVexStatements('my repo')).toEqual([stmt as never])
    expect(lastUrl()).toBe('/api/v1/repositories/my%20repo/vex/statements')

    await listVexStatements('r', 'left-pad@1.0.0')
    expect(lastUrl()).toBe('/api/v1/repositories/r/vex/statements?artifact_id=left-pad%401.0.0')

    spy.mockImplementation(async () => json({}))
    expect(await listVexStatements('r')).toEqual([])
  })

  it('records a decision with PUT and the JSON body unchanged', async () => {
    spy = spyOn(globalThis, 'fetch').mockResolvedValue(json({ id: 's1' }))
    const decision = {
      artifact_id: 'art-1',
      vuln_id: 'CVE-2024-1',
      status: 'not_affected' as const,
      justification: 'component_not_present' as const,
      impact_statement: 'unused',
    }
    await putVexStatement('r', decision)
    expect(lastUrl()).toBe('/api/v1/repositories/r/vex/statements')
    expect(lastInit().method).toBe('PUT')
    expect(JSON.parse(lastInit().body as string)).toEqual(decision)
    expect(new Headers(lastInit().headers).get('content-type')).toBe('application/json')
  })

  it('deletes a decision by id', async () => {
    spy = spyOn(globalThis, 'fetch').mockResolvedValue(new Response(null, { status: 204 }))
    await deleteVexStatement('my repo', 'id/1')
    expect(lastUrl()).toBe('/api/v1/repositories/my%20repo/vex/statements/id%2F1')
    expect(lastInit().method).toBe('DELETE')
  })

  it('surfaces the server validation message', async () => {
    spy = spyOn(globalThis, 'fetch').mockResolvedValue(json({ error: 'justification is required', code: 'validation_failed' }, 400))
    await expect(
      putVexStatement('r', { artifact_id: 'a', vuln_id: 'v', status: 'not_affected' }),
    ).rejects.toThrow('justification is required')
  })

  it('builds document URLs per format and scope', () => {
    expect(vexDocumentUrl('my repo', 'openvex')).toBe('/api/v1/repositories/my%20repo/vex?format=openvex')
    expect(vexDocumentUrl('r', 'cyclonedx', { specVersion: '1.6', artifactId: 'a@1' })).toBe(
      '/api/v1/repositories/r/vex?format=cyclonedx&spec_version=1.6&artifact_id=a%401',
    )
  })

  it('downloads through a preflight, then hands the file to the browser', async () => {
    spy = spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response('{}', { status: 200, headers: { 'Content-Disposition': 'attachment; filename=vex.openvex.json' } }),
    )
    const clicked: HTMLAnchorElement[] = []
    const click = spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      clicked.push(this)
    })
    try {
      await downloadVexDocument('r', 'openvex', { artifactId: 'art-1' })
    } finally {
      click.mockRestore()
    }
    expect(lastUrl()).toBe('/api/v1/repositories/r/vex?format=openvex&artifact_id=art-1')
    expect(clicked).toHaveLength(1)
    expect(clicked[0]!.getAttribute('href')).toBe('/api/v1/repositories/r/vex?format=openvex&artifact_id=art-1')
    expect(clicked[0]!.download).toBe('vex.openvex.json')
  })

  it('reports a refused download instead of saving a file', async () => {
    spy = spyOn(globalThis, 'fetch').mockResolvedValue(json({ error: 'admin access required' }, 403))
    const click = spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    try {
      await expect(downloadVexDocument('r', 'cyclonedx')).rejects.toThrow('admin access required')
      expect(click).not.toHaveBeenCalled()
    } finally {
      click.mockRestore()
    }
  })

  it('offers the statuses and OpenVEX justifications the server accepts', () => {
    expect([...VEX_STATUSES]).toEqual(['affected', 'not_affected', 'fixed', 'under_investigation'])
    expect(VEX_JUSTIFICATIONS).toHaveLength(5)
  })
})
