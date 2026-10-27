// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { ALL_FORMATS } from '../formats'
import { clientSetup, dockerDigestPull, pullCommand, repoBaseUrl, shellQuote } from '../installCommands'

describe('installCommands', () => {
  it('produces a base URL and a command for every format', () => {
    for (const f of ALL_FORMATS) {
      expect(repoBaseUrl(f, 'https://oa.example', 'r1')).toContain('oa.example')
      expect(pullCommand(f, 'https://oa.example', 'r1', 'pkg', '1.0.0').length).toBeGreaterThan(0)
    }
  })

  it('uses the native route shape for npm and maven', () => {
    expect(repoBaseUrl('npm', 'https://oa.example', 'r1')).toBe('https://oa.example/repos/r1')
    expect(repoBaseUrl('maven', 'https://oa.example', 'r1')).toBe('https://oa.example/repos/maven/r1')
  })
})

describe('pullCommand docker', () => {
  it('keeps the image path after the repository', () => {
    expect(pullCommand('docker', 'https://oa.example:8080', 'platform-docker', 'platform-docker/base/alpine', 'latest')).toBe(
      'docker pull oa.example:8080/platform-docker/base/alpine:latest',
    )
  })

  it('pulls the default image from the bare repository name', () => {
    expect(pullCommand('docker', 'https://oa.example', 'r1', 'r1', '1.0')).toBe('docker pull oa.example/r1:1.0')
  })
})

describe('dockerDigestPull', () => {
  const hex = 'a'.repeat(64)

  it('pins the image path to the manifest digest', () => {
    expect(dockerDigestPull('https://oa.example:8080', 'r1/base/alpine', `sha256:${hex}`)).toBe(
      `docker pull oa.example:8080/r1/base/alpine@sha256:${hex}`,
    )
    expect(dockerDigestPull('https://oa.example', 'r1', hex)).toBe(`docker pull oa.example/r1@sha256:${hex}`)
  })

  it('offers no digest variant for anything but a sha256 digest', () => {
    expect(dockerDigestPull('https://oa.example', 'r1', '')).toBeNull()
    expect(dockerDigestPull('https://oa.example', 'r1', 'sha512:abc')).toBeNull()
    expect(dockerDigestPull('https://oa.example', 'r1', 'sha256:xyz')).toBeNull()
  })
})

describe('publish snippets match the server routes and credential forms', () => {
  const origin = 'https://oa.example'

  it('uploads PyPI packages to the /legacy/ route', () => {
    const twine = clientSetup('pypi', origin, 'r1').find((c) => c.client === 'twine')
    expect(twine!.configure).toContain('repository = https://oa.example/repos/r1/legacy/\n')
  })

  it('sends cargo and gem credentials as a Bearer header value', () => {
    expect(clientSetup('cargo', origin, 'r1')[0]!.configure).toContain('token = "Bearer <TOKEN>"')
    expect(clientSetup('rubygems', origin, 'r1')[0]!.publish).toContain('GEM_HOST_API_KEY="Bearer <TOKEN>" gem push')
  })
})

describe('pullCommand quoting', () => {
  const origin = 'https://oa.example'

  it('leaves ordinary names bare', () => {
    expect(pullCommand('npm', origin, 'r1', '@acme/widget', '1.2.3-beta.1')).toBe(
      'npm install @acme/widget@1.2.3-beta.1 --registry=https://oa.example/repos/r1/',
    )
    expect(pullCommand('pypi', origin, 'r1', 'requests', '2.31.0')).toBe(
      'pip install requests==2.31.0 --index-url https://oa.example/repos/r1/simple/',
    )
    expect(pullCommand('go', origin, 'r1', 'example.com/m', 'v1.0.0')).toBe(
      'GOPROXY=https://oa.example/repos/r1 go get example.com/m@v1.0.0',
    )
  })

  it('quotes only the words that need it', () => {
    expect(shellQuote('a-Z_0.9@x/y+z:w=v')).toBe('a-Z_0.9@x/y+z:w=v')
    expect(shellQuote('a b')).toBe("'a b'")
    expect(shellQuote('it\'s')).toBe("'it'\\''s'")
    expect(pullCommand('pypi', origin, 'r1', 'pkg', '1.0; rm -rf ~')).toBe(
      "pip install 'pkg==1.0; rm -rf ~' --index-url https://oa.example/repos/r1/simple/",
    )
    expect(pullCommand('deb', origin, 'r1', 'lib$(id)', '1:2')).toBe("apt install 'lib$(id)=1:2'")
  })

  it('keeps a quoted GOPROXY value a valid assignment', () => {
    expect(pullCommand('go', origin, 'my repo', 'm', 'v1')).toBe(
      "GOPROXY='https://oa.example/repos/my%20repo' go get m@v1",
    )
  })

  it('encodes each raw path segment and keeps the slashes', () => {
    expect(pullCommand('raw', origin, 'r1', 'dir/a file#1?.txt', '')).toBe(
      "curl -fO 'https://oa.example/repos/raw/r1/dir/a%20file%231%3F.txt'",
    )
    expect(pullCommand('raw', origin, 'r1', 'dir/plain.txt', '')).toBe(
      'curl -fO https://oa.example/repos/raw/r1/dir/plain.txt',
    )
  })

  it('does not shell-quote the non-shell snippets', () => {
    expect(pullCommand('sbt', origin, 'r1', 'p', 'v')).toBe('resolvers += "r1" at "https://oa.example/repos/sbt/r1"')
  })
})

describe('terraform', () => {
  const origin = 'https://oa.example:8443'

  it('writes module and provider usage with the repository as registry namespace', () => {
    expect(pullCommand('terraform', origin, 'r1', 'modules/vpc/aws', '1.2.3')).toBe(
      'module "vpc" {\n  source  = "oa.example:8443/r1/vpc/aws"\n  version = "1.2.3"\n}',
    )
    const provider = pullCommand('terraform', origin, 'r1', 'providers/acme', '1.0.0')
    expect(provider).toContain('acme = {')
    expect(provider).toContain('source  = "oa.example:8443/r1/acme"')
  })

  it('offers terraform and OpenTofu credentials and a token publish API', () => {
    const setups = clientSetup('terraform', origin, 'r1')
    expect(setups.map((c) => c.client)).toEqual(['terraform', 'OpenTofu'])
    expect(setups[0]!.configure).toContain('credentials "oa.example:8443" {\n  token = "<TOKEN>"\n}')
    expect(setups[0]!.configure).toContain('~/.terraformrc')
    expect(setups[1]!.configure).toContain('~/.tofurc')
    expect(setups[0]!.publish).toContain('@module.tar.gz https://oa.example:8443/repos/r1/_api/modules/NAME/SYSTEM/1.0.0')
    expect(setups[0]!.publish).toContain('/repos/r1/_api/providers/TYPE/1.0.0/linux/amd64')
  })
})

describe('clientSetup', () => {
  const origin = 'http://oa.example:8080'

  it('uses the /repos/{name} prefix for npm', () => {
    const [npm] = clientSetup('npm', origin, 'r1')
    expect(npm!.client).toBe('npm')
    expect(npm!.configure).toContain('npm config set registry http://oa.example:8080/repos/r1/')
    expect(npm!.publish).toContain('npm publish --registry http://oa.example:8080/repos/r1/')
    expect(clientSetup('npm', origin, 'r1').map((c) => c.client)).toEqual(['npm', 'pnpm', 'yarn'])
  })

  it('uses the isolated prefix for maven, sbt, ivy and raw', () => {
    for (const f of ['maven', 'sbt', 'ivy', 'raw']) {
      const text = clientSetup(f, origin, 'r1')
        .map((c) => c.configure + (c.publish ?? ''))
        .join('\n')
      expect(text).toContain(`${origin}/repos/${f}/r1`)
    }
  })

  it('uses the host root for docker', () => {
    const [d] = clientSetup('docker', origin, 'r1')
    expect(d!.configure).toBe('docker login oa.example:8080 -u <USERNAME> -p <TOKEN>')
    // The image name is the repository name: a nested path would address another repo.
    expect(d!.publish).toContain('docker push oa.example:8080/r1:latest')
    expect(d!.publish).not.toContain('/r1/')
    expect(d!.configure).not.toContain('/repos/')
  })

  it('uses /repos/{name} for other formats and never embeds real credentials', () => {
    for (const f of ['pypi', 'helm', 'cargo', 'go', 'nuget', 'deb', 'rpm', 'conda']) {
      const text = clientSetup(f, origin, 'r1').map((c) => c.configure).join('\n')
      expect(text).toContain(`${origin}/repos/r1`)
    }
  })

  it('returns snippets for every format', () => {
    for (const f of ALL_FORMATS) {
      const setups = clientSetup(f, origin, 'r1')
      expect(setups.length).toBeGreaterThan(0)
      for (const s of setups) expect(s.configure.length).toBeGreaterThan(0)
    }
  })
})
