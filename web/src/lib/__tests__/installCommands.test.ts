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

describe('pacman', () => {
  const origin = 'https://oa.example:8443'

  it('installs through the repository section', () => {
    expect(pullCommand('pacman', origin, 'r1', 'tool', '1.0-1')).toBe('sudo pacman -Sy r1/tool')
  })

  it('configures pacman.conf, trusts the key and publishes with a token PUT', () => {
    const [setup] = clientSetup('pacman', origin, 'r1')
    expect(setup!.configure).toContain('[r1]\nSigLevel = Required\nServer = https://oa.example:8443/repos/r1/os/$arch')
    expect(setup!.configure).toContain('curl -fsSL https://oa.example:8443/repos/r1/public.key | sudo pacman-key --add -')
    expect(setup!.configure).toContain('pacman-key --lsign-key $(curl -fsSL https://oa.example:8443/repos/r1/public.key | gpg --show-keys')
    expect(setup!.publish).toContain(
      'curl -fsS -H "Authorization: Bearer $OA_TOKEN" -T my-package-1.0-1-x86_64.pkg.tar.zst ' +
        'https://oa.example:8443/repos/r1/os/x86_64/my-package-1.0-1-x86_64.pkg.tar.zst',
    )
    expect(setup!.publish).toContain('.pkg.tar.zst.sig')
  })
})

describe('pacman proxy', () => {
  const origin = 'https://oa.example:8443'

  it('prepends the mirror root URL to the mirrorlist and installs from it', () => {
    const setups = clientSetup('pacman', origin, 'mirror', 'proxy')
    expect(setups).toHaveLength(1)
    expect(setups[0]!.configure).toContain('# top of /etc/pacman.d/mirrorlist\nServer = https://oa.example:8443/repos/mirror/$repo/os/$arch')
    expect(setups[0]!.configure).toContain('sudo pacman -Syu')
    expect(setups[0]!.configure).not.toContain('pacman-key')
    expect(setups[0]!.publish).toBeUndefined()
    expect(pullCommand('pacman', origin, 'mirror', 'tool', '1.0-1', 'proxy')).toBe('sudo pacman -Sy tool')
  })
})

describe('pacman group', () => {
  const origin = 'https://oa.example:8443'

  it('is read-only and trusts the group key', () => {
    const [setup] = clientSetup('pacman', origin, 'all', 'group')
    expect(setup!.configure).toContain('[all]\nSigLevel = Required\nServer = https://oa.example:8443/repos/all/os/$arch')
    expect(setup!.configure).toContain('https://oa.example:8443/repos/<member>/public.key')
    expect(setup!.publish).toBeUndefined()
  })
})

describe('nix', () => {
  const origin = 'https://oa.example:8443'

  it('substitutes a store path from the repository', () => {
    expect(pullCommand('nix', origin, 'cache', 'hello-2.12', 'abcdfghijklmnpqrsvwxyz0123456789')).toBe(
      'nix copy --from https://oa.example:8443/repos/cache /nix/store/abcdfghijklmnpqrsvwxyz0123456789-hello-2.12',
    )
  })

  it('configures substituters, the trusted key and the netrc, and publishes with nix copy', () => {
    const [setup] = clientSetup('nix', origin, 'cache')
    expect(setup!.configure).toContain('extra-substituters = https://oa.example:8443/repos/cache')
    expect(setup!.configure).toContain('extra-trusted-public-keys = $(curl -fsS https://oa.example:8443/repos/cache/public.key)')
    expect(setup!.configure).toContain('machine oa.example login oa password <TOKEN>')
    expect(setup!.configure).toContain('--option require-sigs true')
    expect(setup!.publish).toBe("nix copy --to 'https://oa.example:8443/repos/cache' /nix/store/HASH-NAME")
  })

  it('trusts the upstream key for a proxy and the group key for a group, both read-only', () => {
    const [proxy] = clientSetup('nix', origin, 'cache', 'proxy')
    expect(proxy!.configure).toContain('extra-trusted-public-keys = cache.nixos.org-1:')
    expect(proxy!.publish).toBeUndefined()
    const [group] = clientSetup('nix', origin, 'cache', 'group')
    expect(group!.configure).toContain('extra-trusted-public-keys = $(curl -fsS https://oa.example:8443/repos/cache/public.key)')
    expect(group!.configure).toContain('cache.nixos.org-1:')
    expect(group!.publish).toBeUndefined()
  })
})

describe('apk', () => {
  const origin = 'https://oa.example:8443'

  it('serves from /repos/REPO, not isolated', () => {
    expect(repoBaseUrl('apk', origin, 'my repo')).toBe('https://oa.example:8443/repos/my%20repo')
  })

  it('pins the version with = and quotes hostile names', () => {
    expect(pullCommand('apk', origin, 'r1', 'curl', '8.5.0-r0')).toBe('apk add curl=8.5.0-r0')
    expect(pullCommand('apk', origin, 'r1', 'a$(id)', '1')).toBe("apk add 'a$(id)=1'")
  })

  it('fetches the key, adds the mirror line and publishes with a bearer token', () => {
    const [apk, ...rest] = clientSetup('apk', origin, 'r1')
    expect(rest).toHaveLength(0)
    expect(apk!.client).toBe('apk')
    expect(apk!.configure).toContain('wget -O /etc/apk/keys/openartifact-r1.rsa.pub https://oa.example:8443/repos/r1/public.key')
    expect(apk!.configure).toContain('echo "https://oa.example:8443/repos/r1/v3.20/main" >> /etc/apk/repositories')
    expect(apk!.configure).toContain('https://<USERNAME>:<TOKEN>@oa.example:8443/repos/r1/v3.20/main')
    expect(apk!.configure).toContain('apk update\napk add PACKAGE')
    expect(apk!.publish).toContain(
      'curl -T my-pkg-1.0-r0.apk -H "Authorization: Bearer <TOKEN>" https://oa.example:8443/repos/r1/v3.20/main/x86_64/my-pkg-1.0-r0.apk',
    )
    expect(apk!.publish).toContain(
      'curl -H "Authorization: Bearer <TOKEN>" -F file=@my-pkg-1.0-r0.apk -F branch=v3.20 -F repo=main https://oa.example:8443/api/v1/repositories/r1/apks',
    )
  })

  it('configures a group with the key of the group and nothing to publish', () => {
    const [apk, ...rest] = clientSetup('apk', origin, 'r1', 'group')
    expect(rest).toHaveLength(0)
    expect(apk!.configure).toContain('wget -O /etc/apk/keys/openartifact-r1.rsa.pub https://oa.example:8443/repos/r1/public.key')
    expect(apk!.configure).toContain('echo "https://oa.example:8443/repos/r1/v3.20/main" >> /etc/apk/repositories')
    expect(apk!.publish).toBeUndefined()
  })

  it('configures a proxy with the repository lines only: no key download, nothing to publish', () => {
    const [apk, ...rest] = clientSetup('apk', origin, 'r1', 'proxy')
    expect(rest).toHaveLength(0)
    expect(apk!.configure).toContain('echo "https://oa.example:8443/repos/r1/v3.20/main" >> /etc/apk/repositories')
    expect(apk!.configure).toContain('echo "https://oa.example:8443/repos/r1/v3.20/community" >> /etc/apk/repositories')
    expect(apk!.configure).toContain('alpine-keys')
    expect(apk!.configure).not.toContain('public.key')
    expect(apk!.configure).not.toContain('/etc/apk/keys')
    expect(apk!.publish).toBeUndefined()
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

describe('ansible', () => {
  const origin = 'https://oa.example:8443'

  it('installs a collection from the Galaxy API root of the repository', () => {
    expect(pullCommand('ansible', origin, 'r1', 'community.general', '1.2.3')).toBe(
      'ansible-galaxy collection install community.general:1.2.3 --server https://oa.example:8443/repos/r1/api/',
    )
  })

  it('writes ansible.cfg, requirements.yml and the publish command', () => {
    const [setup] = clientSetup('ansible', origin, 'r1')
    expect(setup!.client).toBe('ansible-galaxy')
    expect(setup!.configure).toContain('[galaxy_server.openartifact]\nurl = https://oa.example:8443/repos/r1/api/\ntoken = <TOKEN>')
    expect(setup!.configure).toContain('source: https://oa.example:8443/repos/r1/api/')
    expect(setup!.publish).toBe('ansible-galaxy collection publish NAMESPACE-NAME-VERSION.tar.gz --server openartifact')
  })
})

describe('composer', () => {
  it('requires a package', () => {
    expect(pullCommand('composer', 'https://oa.example', 'r1', 'acme/lib', '1.2.3')).toBe('composer require acme/lib:1.2.3')
  })

  it('configures the repository, auth and publish for https', () => {
    const [setup] = clientSetup('composer', 'https://oa.example:8443', 'r1')
    expect(setup!.client).toBe('composer')
    expect(setup!.configure).toContain('composer config repositories.openartifact composer https://oa.example:8443/repos/r1')
    expect(setup!.configure).toContain('composer config http-basic.oa.example:8443 token <TOKEN>')
    expect(setup!.configure).not.toContain('secure-http')
    expect(setup!.publish).toBe(
      'curl -T vendor-name-1.0.0.zip -H "Authorization: Bearer <TOKEN>" https://oa.example:8443/repos/r1/packages/upload/vendor/name/1.0.0',
    )
  })

  it('disables secure-http for a plain http origin', () => {
    const [setup] = clientSetup('composer', 'http://oa.example:8080', 'r1')
    expect(setup!.configure).toContain('composer config secure-http false')
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

  it('uses the isolated prefix for ivy, maven, raw and sbt', () => {
    for (const f of ['ivy', 'maven', 'raw', 'sbt']) {
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
    for (const f of ['cargo', 'conda', 'deb', 'go', 'helm', 'nuget', 'pypi', 'rpm']) {
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
