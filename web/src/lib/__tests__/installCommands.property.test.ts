// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { ALL_FORMATS } from '../formats'
import { clientSetup, dockerDigestPull, pullCommand, repoBaseUrl, shellQuote } from '../installCommands'
import { fc, hostileString, hostileWord } from './propertySetup'

/**
 * A strict word splitter for the subset of POSIX shell the snippets use. Bare
 * text may only hold characters no shell treats specially; everything else
 * has to sit inside single quotes (or be the `\'` escape of a quote). Any
 * other unquoted character (space, `$`, backtick, `;`, newline, ...) is a
 * break-out and throws, so "splits into exactly these words" proves a value
 * cannot split, expand or chain.
 */
function shellWords(command: string): string[] {
  const words: string[] = []
  let cur: string | null = null
  let i = 0
  while (i < command.length) {
    const ch = command[i]!
    if (ch === "'") {
      const end = command.indexOf("'", i + 1)
      if (end < 0) throw new Error(`unterminated quote in ${JSON.stringify(command)}`)
      cur = (cur ?? '') + command.slice(i + 1, end)
      i = end + 1
    } else if (ch === '\\' && command[i + 1] === "'") {
      cur = (cur ?? '') + "'"
      i += 2
    } else if (ch === ' ') {
      if (cur !== null) words.push(cur)
      cur = null
      i++
    } else if (/[A-Za-z0-9._@/+:=-]/.test(ch)) {
      cur = (cur ?? '') + ch
      i++
    } else {
      throw new Error(`unquoted ${JSON.stringify(ch)} in ${JSON.stringify(command)}`)
    }
  }
  if (cur !== null) words.push(cur)
  return words
}

// Ports 80 and 443 are dropped from URL.host, so leave them out to keep the
// expected host independent of the implementation.
const origin = fc
  .tuple(fc.constantFrom('http', 'https'), fc.domain(), fc.option(fc.integer({ min: 1024, max: 65535 }), { nil: undefined }))
  .map(([scheme, host, port]) => ({ url: `${scheme}://${host}${port ? `:${port}` : ''}`, host: `${host}${port ? `:${port}` : ''}` }))

/** Repository names as the server allows them (internal/api/repositories.go). */
const repoName = fc.stringMatching(/^[a-zA-Z0-9_-]{1,40}$/)
const imagePath = fc.array(fc.stringMatching(/^[a-z0-9][a-z0-9._-]{0,15}$/), { minLength: 1, maxLength: 4 }).map((s) => s.join('/'))

const ISOLATED = ['maven', 'sbt', 'ivy', 'raw']
const encode = encodeURIComponent
const encodePath = (p: string) => p.split('/').map(encode).join('/')

/** The argv each shell-based format must produce, written out independently of the implementation. */
function expectedArgv(format: string, o: { url: string; host: string }, repo: string, pkg: string, version: string): string[] | null {
  const base = repoBaseUrl(format, o.url, repo)
  switch (format) {
    case 'docker': return ['docker', 'pull', `${o.host}/${pkg}:${version}`]
    case 'npm': return ['npm', 'install', `${pkg}@${version}`, `--registry=${base}/`]
    case 'pypi': return ['pip', 'install', `${pkg}==${version}`, '--index-url', `${base}/simple/`]
    case 'maven': return ['mvn', 'dependency:get', `-Dartifact=${pkg}:${version}`, `-DremoteRepositories=${base}`]
    case 'helm': return ['helm', 'pull', pkg, '--repo', base, '--version', version]
    case 'cargo': return ['cargo', 'install', pkg, '--version', version, '--index', `sparse+${base}/index/`]
    case 'nuget': return ['dotnet', 'add', 'package', pkg, '--version', version, '--source', `${base}/v3/index.json`]
    case 'go': return [`GOPROXY=${base}`, 'go', 'get', `${pkg}@${version}`]
    case 'rubygems': return ['gem', 'install', pkg, '-v', version, '--source', `${base}/`]
    case 'conda': return ['conda', 'install', '-c', base, `${pkg}=${version}`]
    case 'conan': return ['conan', 'install', `--requires=${pkg}/${version}`, '-r', repo]
    case 'deb': return ['apt', 'install', `${pkg}=${version}`]
    case 'rpm': return ['dnf', 'install', `${pkg}-${version}`]
    case 'raw': return ['curl', '-fO', `${base}/${encodePath(pkg)}`]
    default: return null // sbt and ivy emit build-file fragments, not shell
  }
}

describe('shellQuote (property)', () => {
  it('always yields exactly one word equal to the input, whatever it contains', () => {
    fc.assert(fc.property(fc.oneof(hostileString, fc.string({ unit: 'grapheme' })), (token) => {
      expect(shellWords(shellQuote(token))).toEqual([token])
    }))
  })

  it('leaves tokens made only of shell-inert characters bare', () => {
    fc.assert(fc.property(fc.stringMatching(/^[A-Za-z0-9._@/+:=-]+$/), (token) => {
      expect(shellQuote(token)).toBe(token)
    }))
  })

  it('quotes the empty string so it stays an argument', () => {
    expect(shellWords(shellQuote(''))).toEqual([''])
  })
})

describe('repoBaseUrl (property)', () => {
  it('stays under the origin, keeps the repo in one path segment and never leaks query or fragment', () => {
    fc.assert(fc.property(fc.constantFrom(...ALL_FORMATS), origin, hostileWord, (format, o, repo) => {
      fc.pre(repo !== '.' && repo !== '..') // dot segments are normalised away by URL parsing; the server never allows such names
      const base = repoBaseUrl(format, o.url, repo)
      expect(base.endsWith('/')).toBe(false)
      if (format === 'docker') {
        expect(base).toBe(o.url)
        return
      }
      const url = new URL(base)
      expect(url.origin).toBe(new URL(o.url).origin)
      expect(url.search).toBe('')
      expect(url.hash).toBe('')
      const segments = url.pathname.split('/').slice(1)
      expect(segments).toHaveLength(ISOLATED.includes(format) ? 3 : 2)
      expect(segments[0]).toBe('repos')
      if (ISOLATED.includes(format)) expect(segments[1]).toBe(format)
      expect(decodeURIComponent(segments.at(-1)!)).toBe(repo)
    }))
  })

  it('never produces an empty value, even for an unknown format', () => {
    fc.assert(fc.property(fc.oneof(fc.constantFrom(...ALL_FORMATS), fc.string()), origin, repoName, (format, o, repo) => {
      expect(repoBaseUrl(format, o.url, repo).length).toBeGreaterThan(0)
    }))
  })
})

describe('pullCommand (property)', () => {
  it('yields a non-empty command for every format and for unknown ones', () => {
    fc.assert(fc.property(fc.oneof(fc.constantFrom(...ALL_FORMATS), fc.string()), origin, repoName, hostileWord, hostileWord, (format, o, repo, pkg, version) => {
      const cmd = pullCommand(format, o.url, repo, pkg, version)
      expect(cmd.trim().length).toBeGreaterThan(0)
      expect(cmd).not.toContain('undefined')
    }))
  })

  it('splits into exactly the intended words however hostile the names are', () => {
    const shellFormats = ALL_FORMATS.filter((f) => f !== 'sbt' && f !== 'ivy')
    fc.assert(fc.property(fc.constantFrom(...shellFormats), origin, hostileWord, hostileWord, hostileWord, (format, o, repo, pkg, version) => {
      fc.pre(repo !== '.' && repo !== '..')
      fc.pre(format !== 'raw' || pkg.split('/').every((s) => s !== '.' && s !== '..'))
      expect(shellWords(pullCommand(format, o.url, repo, pkg, version))).toEqual(expectedArgv(format, o, repo, pkg, version)!)
    }))
  })

  it('points every URL argument under the repository base URL (deb, rpm and conan commands carry none)', () => {
    fc.assert(fc.property(fc.constantFrom(...ALL_FORMATS.filter((f) => f !== 'docker' && f !== 'deb' && f !== 'rpm' && f !== 'conan')), origin, repoName, hostileWord, hostileWord, (format, o, repo, pkg, version) => {
      const base = repoBaseUrl(format, o.url, repo)
      const cmd = pullCommand(format, o.url, repo, pkg, version)
      const words = format === 'sbt' || format === 'ivy' ? [cmd] : shellWords(cmd)
      const withOrigin = words.filter((w) => w.includes(o.url))
      expect(withOrigin.length).toBeGreaterThan(0)
      for (const w of withOrigin) expect(w).toContain(base)
    }))
  })

  it('keeps the whole image path of a docker pull, after the registry host', () => {
    fc.assert(fc.property(origin, repoName, imagePath, fc.stringMatching(/^[A-Za-z0-9_][A-Za-z0-9._-]{0,20}$/), (o, repo, image, tag) => {
      const pkg = `${repo}/${image}`
      const [, , ref] = shellWords(pullCommand('docker', o.url, repo, pkg, tag))
      expect(ref).toBe(`${o.host}/${repo}/${image}:${tag}`)
    }))
  })

  it('keeps the bare repository name as the pull path when the image has no sub-path', () => {
    fc.assert(fc.property(origin, repoName, (o, repo) => {
      expect(shellWords(pullCommand('docker', o.url, repo, repo, 'latest'))).toEqual(['docker', 'pull', `${o.host}/${repo}:latest`])
    }))
  })

  it('writes sbt and ivy resolvers for the repository base URL', () => {
    fc.assert(fc.property(fc.constantFrom('sbt', 'ivy'), origin, repoName, (format, o, repo) => {
      const cmd = pullCommand(format, o.url, repo, 'pkg', '1.0')
      expect(cmd).toContain(`"${repo}"`)
      expect(cmd).toContain(`"${repoBaseUrl(format, o.url, repo)}"`)
    }))
  })
})

describe('dockerDigestPull (property)', () => {
  it('pins any sha256 digest, with or without the prefix, as one word', () => {
    fc.assert(fc.property(origin, imagePath, fc.stringMatching(/^[0-9a-f]{64}$/), fc.boolean(), (o, image, hex, prefixed) => {
      const cmd = dockerDigestPull(o.url, image, prefixed ? `sha256:${hex}` : hex)
      expect(shellWords(cmd!)).toEqual(['docker', 'pull', `${o.host}/${image}@sha256:${hex}`])
    }))
  })

  it('refuses anything that is not a sha256 hex digest', () => {
    fc.assert(fc.property(origin, hostileString, (o, digest) => {
      fc.pre(!/^(sha256:)?[0-9a-f]{64}$/.test(digest))
      expect(dockerDigestPull(o.url, 'r/img', digest)).toBeNull()
    }))
  })
})

describe('clientSetup (property)', () => {
  it('has at least one complete, non-empty snippet for every format and for unknown ones', () => {
    fc.assert(fc.property(fc.oneof(fc.constantFrom(...ALL_FORMATS), fc.string()), origin, repoName, (format, o, repo) => {
      const setups = clientSetup(format, o.url, repo)
      expect(setups.length).toBeGreaterThan(0)
      for (const s of setups) {
        expect(s.client.trim().length).toBeGreaterThan(0)
        expect(s.configure.trim().length).toBeGreaterThan(0)
        for (const text of [s.configure, s.publish ?? '']) {
          expect(text).not.toContain('undefined')
          expect(text).not.toContain('[object')
        }
      }
    }))
  })

  it('only ever addresses URLs under the repository base URL or its upload endpoint', () => {
    fc.assert(fc.property(fc.constantFrom(...ALL_FORMATS.filter((f) => f !== 'docker')), origin, repoName, (format, o, repo) => {
      const base = repoBaseUrl(format, o.url, repo)
      const upload = `${o.url}/api/v1/repositories/${encode(repo)}/`
      const urls = clientSetup(format, o.url, repo).flatMap((s) => `${s.configure}\n${s.publish ?? ''}`.match(/https?:\/\/[^\s"'<>\\]+/g) ?? [])
      expect(urls.length).toBeGreaterThan(0)
      for (const u of urls) expect(u.startsWith(base) || u.startsWith(upload)).toBe(true)
    }))
  })

  it('logs in to and pushes docker images by registry host, not by URL', () => {
    fc.assert(fc.property(origin, repoName, (o, repo) => {
      const [s] = clientSetup('docker', o.url, repo)
      expect(s!.configure).toBe(`docker login ${o.host} -u <USERNAME> -p <TOKEN>`)
      expect(s!.publish).toContain(`${o.host}/${repo}:latest`)
    }))
  })
})
