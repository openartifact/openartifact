// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { canUpload, fieldError, goEscape, initialValues, parsePythonDist, readGoModule, uploadMode, uploadPlan } from '../uploadPlans'

describe('uploadMode', () => {
  it('uploads the formats the browser can publish, shows commands for nix and nothing for the rest', () => {
    for (const f of ['apk', 'deb', 'rpm', 'pacman', 'raw', 'npm', 'pypi', 'maven', 'helm', 'rubygems', 'nuget', 'conda', 'composer', 'terraform', 'ansible', 'go']) {
      expect(uploadMode(f)).toBe('upload')
      expect(canUpload(f)).toBe(true)
    }
    expect(uploadMode('nix')).toBe('publish')
    expect(canUpload('nix')).toBe(true)
    for (const f of ['cargo', 'conan', 'docker', 'ivy', 'sbt', 'toString', '__proto__', '', undefined]) {
      expect(uploadMode(f)).toBe('none')
      expect(canUpload(f)).toBe(false)
      expect(uploadPlan(f)).toBeUndefined()
    }
  })
})

describe('fieldError', () => {
  const field = (id: string) => uploadPlan('maven')!.fields.find((f) => f.key === id)!

  it('requires a value unless the field is optional', () => {
    expect(fieldError(field('group'), { group: '  ' })).toBe('upload.invalid')
    expect(fieldError(uploadPlan('raw')!.fields[0], { dir: '' })).toBe('')
  })

  it('validates the trimmed value and names the field specific message', () => {
    expect(fieldError(field('group'), { group: ' com.example ' })).toBe('')
    expect(fieldError(field('group'), { group: 'com..example' })).toBe('upload.invalid')
    expect(fieldError(uploadPlan('npm')!.fields[0], { tag: '1.0.0' })).toBe('upload.npm.tagInvalid')
  })

  it('skips a field that does not apply to the other values', () => {
    const version = uploadPlan('terraform')!.fields.find((f) => f.key === 'version')!
    expect(fieldError(version, { kind: 'provider', version: '' })).toBe('')
    expect(fieldError(version, { kind: 'module', version: '1.0' })).toBe('upload.terraform.versionInvalid')
    expect(fieldError(version, { kind: 'module', version: '1.0.0-rc.1' })).toBe('')
  })

  it('accepts exactly the composer names the server accepts', () => {
    const pkg = uploadPlan('composer')!.fields[0]
    for (const ok of ['acme/widgets', 'a/b', 'vendor-x/pkg.name', 'v_1/p--2']) expect(fieldError(pkg, { package: ok })).toBe('')
    for (const bad of ['Acme/widgets', 'widgets', 'a/b/c', '-a/b', 'a/b-', 'a/b__c']) expect(fieldError(pkg, { package: bad })).toBe('upload.composer.packageInvalid')
  })

  it('starts every plan from its defaults', () => {
    expect(initialValues(uploadPlan('apk')!)).toEqual({ branch: 'edge', repo: 'main', arch: '' })
    expect(initialValues(uploadPlan('conda')!)).toEqual({ platform: 'noarch' })
    expect(initialValues(uploadPlan('helm')!)).toEqual({})
  })
})

describe('parsePythonDist', () => {
  it('reads a wheel: name, version and the python tag', () => {
    expect(parsePythonDist('upl_pkg-0.4.2-py3-none-any.whl')).toEqual({ name: 'upl_pkg', version: '0.4.2', filetype: 'bdist_wheel', pyversion: 'py3' })
    expect(parsePythonDist('numpy-2.0.0-1-cp312-cp312-manylinux_2_17_x86_64.whl')).toMatchObject({ name: 'numpy', version: '2.0.0', pyversion: 'cp312' })
  })

  it('reads a source archive, whose name may itself contain dashes', () => {
    expect(parsePythonDist('my-cool-pkg-1.0.0.tar.gz')).toEqual({ name: 'my-cool-pkg', version: '1.0.0', filetype: 'sdist', pyversion: 'source' })
    expect(parsePythonDist('pkg-2.0rc1.zip')).toMatchObject({ name: 'pkg', version: '2.0rc1' })
  })

  it.each(['readme.txt', 'pkg.tar.gz', 'a-b.whl', '-1.0.tar.gz'])('rejects %s', (name) => {
    expect(() => parsePythonDist(name)).toThrow()
  })
})

describe('go modules', () => {
  const zip = (entry: string, signature = [0x50, 0x4b, 3, 4]) => {
    const head = new Uint8Array(30)
    head.set(signature)
    const name = new TextEncoder().encode(entry)
    head[26] = name.length & 0xff
    head[27] = name.length >> 8
    return new Blob([head, name, 'rest'])
  }

  it('escapes capitals the way the module proxy protocol does', () => {
    expect(goEscape('github.com/Azure/azure-sdk')).toBe('github.com/!azure/azure-sdk')
    expect(goEscape('v1.0.0-RC1')).toBe('v1.0.0-!r!c1')
  })

  it('reads the module path and version from the first entry of the zip', async () => {
    expect(await readGoModule(zip('example.com/a/b@v1.2.3/go.mod'))).toEqual({ module: 'example.com/a/b', version: 'v1.2.3' })
    expect(await readGoModule(zip('example.com/m/v2@v2.0.0+incompatible/x.go'))).toEqual({ module: 'example.com/m/v2', version: 'v2.0.0+incompatible' })
  })

  it.each([
    ['an entry without a version', zip('example.com/a/go.mod')],
    ['a file that is no zip', zip('example.com/a@v1.0.0/go.mod', [1, 2, 3, 4])],
    ['a short file', new Blob(['PK'])],
  ])('rejects %s', async (_label, blob) => {
    await expect(readGoModule(blob)).rejects.toMatchObject({ key: 'upload.error.goModule' })
  })
})
