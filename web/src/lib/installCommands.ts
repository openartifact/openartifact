// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Builds the native client URL / install command for a package. Repository
// routes mirror cmd/server/main.go: most formats live under /repos/{repo};
// ivy, maven, raw and sbt use an isolated /repos/{format}/{repo} prefix, and
// docker serves the registry API at the host root (/v2/). The isolated prefixes
// come from the Go registry (formats.generated.ts).

import { ISOLATED_FORMATS } from './formats.generated'

const ISOLATED_PREFIX: ReadonlySet<string> = new Set(ISOLATED_FORMATS)

/** Native base URL of a repository for its format (no trailing slash). */
export function repoBaseUrl(format: string, origin: string, repo: string): string {
  const name = encodeURIComponent(repo)
  if (format === 'docker') return origin
  if (ISOLATED_PREFIX.has(format)) return `${origin}/repos/${format}/${name}`
  return `${origin}/repos/${name}`
}

function hostOf(origin: string): string {
  try {
    return new URL(origin).host
  } catch {
    return origin.replace(/^https?:\/\//, '')
  }
}

// sbt matches credentials on the host alone, so the port is left out.
function hostnameOf(origin: string): string {
  try {
    return new URL(origin).hostname
  } catch {
    return hostOf(origin).replace(/:\d+$/, '')
  }
}

const SHELL_SAFE = /^[A-Za-z0-9._@/+:=-]+$/

/**
 * One shell word: left bare when it only has characters no shell treats
 * specially, single-quoted otherwise so a name copied from the page cannot
 * split, expand or chain commands.
 */
export function shellQuote(token: string): string {
  if (SHELL_SAFE.test(token)) return token
  return `'${token.replace(/'/g, `'\\''`)}'`
}

/** A raw artifact path as a URL path: every segment encoded, slashes kept. */
function encodePath(path: string): string {
  return path.split('/').map(encodeURIComponent).join('/')
}

/**
 * HCL usage block for a terraform row. The catalog names a module
 * `modules/NAME/SYSTEM` and a provider platform `providers/TYPE`; terraform's
 * registry namespace is the repository name.
 */
function terraformUsage(host: string, repo: string, pkg: string, version: string): string {
  const [kind, name, system] = pkg.split('/')
  if (kind === 'modules' && name && system) {
    return `module "${name}" {\n  source  = "${host}/${repo}/${name}/${system}"\n  version = "${version}"\n}`
  }
  if (kind === 'providers' && name) {
    return `terraform {\n  required_providers {\n    ${name} = {\n      source  = "${host}/${repo}/${name}"\n      version = "${version}"\n    }\n  }\n}`
  }
  return `${host}/${repo}`
}

/** Install/pull command for a package version, built from the real server origin. */
export function pullCommand(
  format: string,
  origin: string,
  repo: string,
  pkg: string,
  version: string,
  repoType = 'hosted',
): string {
  const base = repoBaseUrl(format, origin, repo)
  const q = shellQuote
  switch (format) {
    case 'ansible':
      return `ansible-galaxy collection install ${q(`${pkg}:${version}`)} --server ${q(`${base}/api/`)}`
    case 'apk':
      return `apk add ${q(`${pkg}=${version}`)}`
    case 'cargo':
      return `cargo install ${q(pkg)} --version ${q(version)} --index ${q(`sparse+${base}/index/`)}`
    case 'conan':
      return `conan install ${q(`--requires=${pkg}/${version}`)} -r ${q(repo)}`
    case 'conda':
      return `conda install -c ${q(base)} ${q(`${pkg}=${version}`)}`
    case 'deb':
      return `apt install ${q(`${pkg}=${version}`)}`
    case 'docker':
      // The listed package name already is the registry name, `<repo>` or
      // `<repo>/<image path>` (internal/ocipath), so it is the pull path.
      return `docker pull ${q(`${hostOf(origin)}/${pkg}:${version}`)}`
    case 'go':
      // A quoted value is still a valid environment assignment; a quoted word is not.
      return `GOPROXY=${q(base)} go get ${q(`${pkg}@${version}`)}`
    case 'helm':
      return `helm pull ${q(pkg)} --repo ${q(base)} --version ${q(version)}`
    case 'ivy':
      return `<ibiblio name="${repo}" m2compatible="true" root="${base}"/>`
    case 'maven':
      return `mvn dependency:get ${q(`-Dartifact=${pkg}:${version}`)} ${q(`-DremoteRepositories=${base}`)}`
    case 'nix':
      // The version is the 32 character store hash; the path is `<hash>-<name>`.
      return `nix copy --from ${q(base)} ${q(`/nix/store/${version}-${pkg}`)}`
    case 'npm':
      return `npm install ${q(`${pkg}@${version}`)} ${q(`--registry=${base}/`)}`
    case 'nuget':
      return `dotnet add package ${q(pkg)} --version ${q(version)} --source ${q(`${base}/v3/index.json`)}`
    case 'pacman':
      // A proxy mirrors the whole Arch mirror: the package is named without a repository qualifier.
      return repoType === 'proxy' ? `sudo pacman -Sy ${q(pkg)}` : `sudo pacman -Sy ${q(`${repo}/${pkg}`)}`
    case 'pypi':
      return `pip install ${q(`${pkg}==${version}`)} --index-url ${q(`${base}/simple/`)}`
    case 'raw':
      return `curl -fO ${q(`${base}/${encodePath(pkg)}`)}`
    case 'rpm':
      return `dnf install ${q(`${pkg}-${version}`)}`
    case 'rubygems':
      return `gem install ${q(pkg)} -v ${q(version)} --source ${q(`${base}/`)}`
    case 'sbt':
      return `resolvers += "${repo}" at "${base}"`
    case 'terraform':
      return terraformUsage(hostOf(origin), repo, pkg, version)
    default:
      return `${base}/`
  }
}

/**
 * Docker pull pinned to a manifest digest (`sha256:<hex>`; a bare hex digest is
 * accepted), or null when the digest is not a sha256 one. The pinned form does
 * not move when the tag is re-pushed.
 */
export function dockerDigestPull(origin: string, pkg: string, digest: string): string | null {
  const hex = digest.replace(/^sha256:/, '')
  if (!/^[0-9a-f]{64}$/.test(hex)) return null
  return `docker pull ${shellQuote(`${hostOf(origin)}/${pkg}@sha256:${hex}`)}`
}

interface ClientSetup {
  client: string
  configure: string
  publish?: string
}

/**
 * Per-client configure/publish snippets for a repository, built from the real
 * server origin. Credentials are always the literal `<TOKEN>` placeholder.
 */
export function clientSetup(format: string, origin: string, repo: string, repoType = 'hosted'): ClientSetup[] {
  const base = repoBaseUrl(format, origin, repo)
  const host = hostOf(origin)
  switch (format) {
    case 'ansible': {
      const api = `${base}/api/`
      return [
        {
          client: 'ansible-galaxy',
          configure:
            `# ansible.cfg\n[galaxy]\nserver_list = openartifact\n\n[galaxy_server.openartifact]\nurl = ${api}\ntoken = <TOKEN>` +
            `\n\n# install\nansible-galaxy collection install NAMESPACE.NAME --server openartifact` +
            `\n\n# requirements.yml\ncollections:\n  - name: NAMESPACE.NAME\n    version: 1.0.0\n    source: ${api}`,
          publish:
            repoType === 'proxy' || repoType === 'group'
              ? undefined
              : 'ansible-galaxy collection publish NAMESPACE-NAME-VERSION.tar.gz --server openartifact',
        },
      ]
    }
    case 'apk': {
      const keyFile = `/etc/apk/keys/openartifact-${repo}.rsa.pub`
      const authBase = base.replace(/^(https?:\/\/)/, '$1<USERNAME>:<TOKEN>@')
      if (repoType === 'proxy') {
        // The index keeps the upstream's signature, so no key comes from this server: the client verifies it with the keys it already trusts.
        return [
          {
            client: 'apk',
            configure:
              `# the mirror's own signatures are kept: Alpine's keys (alpine-keys) are already installed in official images\n` +
              `# private repository: put the credentials in the URL, ${authBase}/v3.20/main\n` +
              `echo "${base}/v3.20/main" >> /etc/apk/repositories\n` +
              `echo "${base}/v3.20/community" >> /etc/apk/repositories\napk update\napk add PACKAGE`,
          },
        ]
      }
      if (repoType === 'group') {
        // Read-only: the merged indexes are signed with the group's own key, not its members'.
        return [
          {
            client: 'apk',
            configure:
              `# the group signs its indexes with its own key, not its members'\n` +
              `# private repository: put the credentials in the URL, ${authBase}/v3.20/main, and fetch the key with wget --user <USERNAME> --password <TOKEN>\n` +
              `wget -O ${keyFile} ${base}/public.key\n` +
              `echo "${base}/v3.20/main" >> /etc/apk/repositories\napk update\napk add PACKAGE`,
          },
        ]
      }
      return [
        {
          client: 'apk',
          configure:
            `# private repository: put the credentials in the URL, ${authBase}/v3.20/main, and fetch the key with wget --user <USERNAME> --password <TOKEN>\n` +
            `wget -O ${keyFile} ${base}/public.key\n` +
            `echo "${base}/v3.20/main" >> /etc/apk/repositories\napk update\napk add PACKAGE`,
          publish:
            `curl -T my-pkg-1.0-r0.apk -H "Authorization: Bearer <TOKEN>" ${base}/v3.20/main/x86_64/my-pkg-1.0-r0.apk\n\n` +
            `# or through the REST API\ncurl -H "Authorization: Bearer <TOKEN>" -F file=@my-pkg-1.0-r0.apk -F branch=v3.20 -F repo=main ${origin}/api/v1/repositories/${encodeURIComponent(repo)}/apks`,
        },
      ]
    }
    case 'cargo':
      return [
        {
          client: 'cargo',
          configure: `# ~/.cargo/config.toml\n[registries.${repo}]\nindex = "sparse+${base}/index/"\ntoken = "Bearer <TOKEN>"`,
          publish: `cargo publish --registry ${repo}`,
        },
      ]
    case 'conan':
      return [
        {
          client: 'conan',
          configure: `conan remote add ${repo} ${base}\nconan remote login ${repo} <USERNAME> -p <TOKEN>`,
          publish: `conan upload "my-lib/1.0" -r ${repo} --confirm`,
        },
      ]
    case 'conda':
      return [
        {
          client: 'conda',
          configure: `conda config --add channels ${base}`,
          publish: `curl -u <USERNAME>:<TOKEN> -H "X-Filename: my-package-1.0-0.conda" --data-binary @my-package-1.0-0.conda ${base}/linux-64`,
        },
      ]
    case 'deb':
      return [
        {
          client: 'apt',
          configure: `curl -fsSL ${base}/public.key | sudo gpg --dearmor -o /etc/apt/keyrings/${repo}.gpg\necho "deb [signed-by=/etc/apt/keyrings/${repo}.gpg] ${base} stable main" | sudo tee /etc/apt/sources.list.d/${repo}.list\nsudo apt update`,
          publish: `curl -u <USERNAME>:<TOKEN> -F file=@my-package_1.0_amd64.deb -F distribution=stable -F component=main ${origin}/api/v1/repositories/${encodeURIComponent(repo)}/debs`,
        },
      ]
    case 'docker':
      return [
        {
          client: 'docker',
          configure: `docker login ${host} -u <USERNAME> -p <TOKEN>`,
          publish: `docker tag my-image:latest ${host}/${repo}:latest\ndocker push ${host}/${repo}:latest`,
        },
      ]
    case 'go':
      return [
        {
          client: 'go',
          configure: `go env -w GOPROXY=${base},direct\ngo env -w GONOSUMDB=*`,
        },
      ]
    case 'helm':
      return [
        {
          client: 'helm',
          configure: `helm repo add ${repo} ${base} --username <USERNAME> --password <TOKEN>\nhelm repo update`,
          publish: `curl -u <USERNAME>:<TOKEN> --data-binary @my-chart-0.1.0.tgz ${base}/charts`,
        },
      ]
    case 'ivy':
      return [
        {
          client: 'Ivy',
          configure: `<ibiblio name="${repo}" m2compatible="true" root="${base}"/>`,
          publish: `<ivy:publish resolver="${repo}" pubrevision="1.0" overwrite="true">\n  <artifacts pattern="dist/[artifact]-[revision].[ext]"/>\n</ivy:publish>`,
        },
      ]
    case 'maven':
      return [
        {
          client: 'Maven',
          configure: `<!-- ~/.m2/settings.xml -->\n<settings>\n  <servers>\n    <server>\n      <id>${repo}</id>\n      <username><USERNAME></username>\n      <password><TOKEN></password>\n    </server>\n  </servers>\n</settings>`,
          publish: `mvn deploy -DaltDeploymentRepository=${repo}::default::${base}`,
        },
      ]
    case 'nix': {
      const key = `${base}/public.key`
      // A proxy passes the upstream's signatures through; a group signs what its hosted members serve.
      const upstreamKey = 'cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY='
      let trusted = `$(curl -fsS ${key})`
      if (repoType === 'proxy') {
        trusted = `${upstreamKey}  # the upstream's key (cache.nixos.org by default): use the key of your upstream cache`
      } else if (repoType === 'group') {
        trusted =
          `$(curl -fsS ${key})  # the group's key, for narinfo its hosted members serve` +
          `\n#   also trust the upstream's key when the group has proxy members, for example ${upstreamKey}`
      }
      const snippet: ClientSetup = {
        client: 'nix',
        configure:
          `# /etc/nix/nix.conf\nextra-substituters = ${base}\nextra-trusted-public-keys = ${trusted}` +
          `\n\n# private repository: a netrc file with the token\n# /etc/nix/netrc\nmachine ${hostnameOf(origin)} login oa password <TOKEN>` +
          `\n# and in /etc/nix/nix.conf\nnetrc-file = /etc/nix/netrc` +
          `\n\n# one-off, without editing nix.conf\nnix build --substituters ${base} --option require-sigs true /nix/store/HASH-NAME`,
      }
      if (repoType !== 'proxy' && repoType !== 'group') snippet.publish = `nix copy --to '${base}' /nix/store/HASH-NAME`
      return [snippet]
    }
    case 'npm':
      return [
        {
          client: 'npm',
          configure: `npm config set registry ${base}/\nnpm config set //${host}/repos/${encodeURIComponent(repo)}/:_authToken <TOKEN>`,
          publish: `npm publish --registry ${base}/`,
        },
        {
          client: 'pnpm',
          configure: `pnpm config set registry ${base}/\npnpm config set //${host}/repos/${encodeURIComponent(repo)}/:_authToken <TOKEN>`,
          publish: `pnpm publish --registry ${base}/`,
        },
        {
          client: 'yarn',
          configure: `yarn config set npmRegistryServer ${base}/\nyarn config set npmAuthToken <TOKEN>`,
          publish: `yarn npm publish`,
        },
      ]
    case 'nuget':
      return [
        {
          client: 'dotnet',
          configure: `dotnet nuget add source ${base}/v3/index.json --name ${repo} --username <USERNAME> --password <TOKEN> --store-password-in-clear-text`,
          publish: `dotnet nuget push my-package.1.0.0.nupkg --source ${repo} --api-key <TOKEN>`,
        },
      ]
    case 'pacman': {
      if (repoType === 'proxy') {
        // A proxy mirrors an Arch mirror root: paths pass through, signatures are the upstream's.
        return [
          {
            client: 'pacman',
            configure:
              `# top of /etc/pacman.d/mirrorlist\nServer = ${base}/$repo/os/$arch` +
              `\n\n# private repository: Server = https://<USER>:<TOKEN>@${host}/repos/${encodeURIComponent(repo)}/$repo/os/$arch` +
              `\n\nsudo pacman -Syu`,
          },
        ]
      }
      const key = `${base}/public.key`
      const put = (file: string) => `curl -fsS -H "Authorization: Bearer $OA_TOKEN" -T ${file} ${base}/os/x86_64/${file}`
      const pkg = 'my-package-1.0-1-x86_64.pkg.tar.zst'
      const configure =
        `# /etc/pacman.conf\n[${repo}]\nSigLevel = Required\nServer = ${base}/os/$arch` +
        `\n\n# private repository: Server = https://<USER>:<TOKEN>@${host}/repos/${encodeURIComponent(repo)}/os/$arch` +
        `\n\n# trust the repository key\ncurl -fsSL ${key} | sudo pacman-key --add -` +
        `\nsudo pacman-key --lsign-key $(curl -fsSL ${key} | gpg --show-keys --with-colons | awk -F: '/^fpr/{print $10; exit}')`
      if (repoType === 'group') {
        // A group is read-only; its packages keep the signatures of the members that hold them.
        return [
          {
            client: 'pacman',
            configure:
              `${configure}\n\n# packages keep the signatures of the members that hold them: trust each hosted member's key` +
              ` too (it is the group's when they share the active key), for example\n# curl -fsSL ${origin}/repos/<member>/public.key` +
              ` | sudo pacman-key --add -`,
          },
        ]
      }
      return [
        {
          client: 'pacman',
          configure,
          publish: `${put(pkg)}\n\n# optional detached signature\n${put(`${pkg}.sig`)}`,
        },
      ]
    }
    case 'pypi':
      return [
        {
          client: 'pip',
          configure: `pip config set global.index-url ${base}/simple/`,
        },
        {
          client: 'twine',
          configure: `# ~/.pypirc\n[distutils]\nindex-servers = openartifact\n\n[openartifact]\nrepository = ${base}/legacy/\nusername = <USERNAME>\npassword = <TOKEN>`,
          publish: `twine upload --repository openartifact dist/*`,
        },
      ]
    case 'raw':
      return [
        {
          client: 'curl',
          configure: `curl -fO ${base}/path/to/file.bin`,
          publish: `curl -u <USERNAME>:<TOKEN> -T file.bin ${base}/path/to/file.bin`,
        },
      ]
    case 'rpm':
      return [
        {
          client: 'dnf / yum',
          configure: `# /etc/yum.repos.d/${repo}.repo\n[${repo}]\nname=${repo}\nbaseurl=${base}\nenabled=1\nrepo_gpgcheck=1\ngpgkey=${base}/public.key`,
          publish: `curl -u <USERNAME>:<TOKEN> -F file=@my-package-1.0.x86_64.rpm ${origin}/api/v1/repositories/${encodeURIComponent(repo)}/packages`,
        },
      ]
    case 'rubygems':
      return [
        {
          client: 'gem',
          configure: `gem sources --add ${base}/`,
          publish: `GEM_HOST_API_KEY="Bearer <TOKEN>" gem push my-gem-1.0.0.gem --host ${base}`,
        },
      ]
    case 'sbt':
      return [
        {
          client: 'sbt',
          configure: `// build.sbt\nresolvers += "${repo}" at "${base}"\ncredentials += Credentials("openartifact", "${hostnameOf(origin)}", "<USERNAME>", "<TOKEN>")`,
          publish: `ThisBuild / publishTo := Some("${repo}" at "${base}")\n// then: sbt publish`,
        },
      ]
    case 'terraform': {
      const host = hostOf(origin)
      const note = '# terraform and tofu need HTTPS and a hostname with a dot'
      const api = `${base}/_api`
      const creds = (file: string) => `${note}\n# ${file}\ncredentials "${host}" {\n  token = "<TOKEN>"\n}`
      const usage =
        `\n\n# modules from this repository\nmodule "x" {\n  source  = "${host}/${repo}/NAME/SYSTEM"\n  version = "1.0.0"\n}` +
        `\n\n# provider\nterraform {\n  required_providers {\n    TYPE = {\n      source  = "${host}/${repo}/TYPE"\n      version = "1.0.0"\n    }\n  }\n}`
      const publish =
        `# module archive (.tar.gz or .zip)\ncurl -X PUT -H "Authorization: Bearer <TOKEN>" --data-binary @module.tar.gz ${api}/modules/NAME/SYSTEM/1.0.0` +
        `\n\n# provider zip, one per platform\ncurl -X PUT -H "Authorization: Bearer <TOKEN>" --data-binary @provider.zip ${api}/providers/TYPE/1.0.0/linux/amd64`
      return [
        { client: 'terraform', configure: creds('~/.terraformrc') + usage, publish },
        { client: 'OpenTofu', configure: creds('~/.tofurc') + usage, publish },
      ]
    }
    default:
      return [{ client: 'URL', configure: `${base}/` }]
  }
}
