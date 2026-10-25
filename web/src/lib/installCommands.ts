// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Builds the native client URL / install command for a package. Repository
// routes mirror cmd/server/main.go: most formats live under /repos/{repo};
// maven, sbt, ivy and raw use an isolated /repos/{format}/{repo} prefix, and
// docker serves the registry API at the host root (/v2/).

const ISOLATED_PREFIX = new Set(['maven', 'sbt', 'ivy', 'raw'])

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

/** Install/pull command for a package version, built from the real server origin. */
export function pullCommand(
  format: string,
  origin: string,
  repo: string,
  pkg: string,
  version: string,
): string {
  const base = repoBaseUrl(format, origin, repo)
  const q = shellQuote
  switch (format) {
    case 'docker':
      // The registry resolves the whole image name to a repository.
      return `docker pull ${q(`${hostOf(origin)}/${repo}:${version}`)}`
    case 'npm':
      return `npm install ${q(`${pkg}@${version}`)} ${q(`--registry=${base}/`)}`
    case 'pypi':
      return `pip install ${q(`${pkg}==${version}`)} --index-url ${q(`${base}/simple/`)}`
    case 'maven':
      return `mvn dependency:get ${q(`-Dartifact=${pkg}:${version}`)} ${q(`-DremoteRepositories=${base}`)}`
    case 'helm':
      return `helm pull ${q(pkg)} --repo ${q(base)} --version ${q(version)}`
    case 'cargo':
      return `cargo install ${q(pkg)} --version ${q(version)} --index ${q(`sparse+${base}/index/`)}`
    case 'nuget':
      return `dotnet add package ${q(pkg)} --version ${q(version)} --source ${q(`${base}/v3/index.json`)}`
    case 'go':
      // A quoted value is still a valid environment assignment; a quoted word is not.
      return `GOPROXY=${q(base)} go get ${q(`${pkg}@${version}`)}`
    case 'rubygems':
      return `gem install ${q(pkg)} -v ${q(version)} --source ${q(`${base}/`)}`
    case 'conda':
      return `conda install -c ${q(base)} ${q(`${pkg}=${version}`)}`
    case 'conan':
      return `conan install ${q(`--requires=${pkg}/${version}`)} -r ${q(repo)}`
    case 'deb':
      return `apt install ${q(`${pkg}=${version}`)}`
    case 'rpm':
      return `dnf install ${q(`${pkg}-${version}`)}`
    case 'raw':
      return `curl -fO ${q(`${base}/${encodePath(pkg)}`)}`
    case 'sbt':
      return `resolvers += "${repo}" at "${base}"`
    case 'ivy':
      return `<ibiblio name="${repo}" m2compatible="true" root="${base}"/>`
    default:
      return `${base}/`
  }
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
export function clientSetup(format: string, origin: string, repo: string): ClientSetup[] {
  const base = repoBaseUrl(format, origin, repo)
  const host = hostOf(origin)
  switch (format) {
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
    case 'docker':
      return [
        {
          client: 'docker',
          configure: `docker login ${host} -u <USERNAME> -p <TOKEN>`,
          publish: `docker tag my-image:latest ${host}/${repo}:latest\ndocker push ${host}/${repo}:latest`,
        },
      ]
    case 'pypi':
      return [
        {
          client: 'pip',
          configure: `pip config set global.index-url ${base}/simple/`,
        },
        {
          client: 'twine',
          configure: `# ~/.pypirc\n[distutils]\nindex-servers = openartifact\n\n[openartifact]\nrepository = ${base}/\nusername = <USERNAME>\npassword = <TOKEN>`,
          publish: `twine upload --repository openartifact dist/*`,
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
    case 'helm':
      return [
        {
          client: 'helm',
          configure: `helm repo add ${repo} ${base} --username <USERNAME> --password <TOKEN>\nhelm repo update`,
          publish: `curl -u <USERNAME>:<TOKEN> --data-binary @my-chart-0.1.0.tgz ${base}/charts`,
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
    case 'rpm':
      return [
        {
          client: 'dnf / yum',
          configure: `# /etc/yum.repos.d/${repo}.repo\n[${repo}]\nname=${repo}\nbaseurl=${base}\nenabled=1\nrepo_gpgcheck=1\ngpgkey=${base}/public.key`,
          publish: `curl -u <USERNAME>:<TOKEN> -F file=@my-package-1.0.x86_64.rpm ${origin}/api/v1/repositories/${encodeURIComponent(repo)}/packages`,
        },
      ]
    case 'cargo':
      return [
        {
          client: 'cargo',
          configure: `# ~/.cargo/config.toml\n[registries.${repo}]\nindex = "sparse+${base}/index/"\ntoken = "<TOKEN>"`,
          publish: `cargo publish --registry ${repo}`,
        },
      ]
    case 'go':
      return [
        {
          client: 'go',
          configure: `go env -w GOPROXY=${base},direct\ngo env -w GONOSUMDB=*`,
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
    case 'rubygems':
      return [
        {
          client: 'gem',
          configure: `gem sources --add ${base}/`,
          publish: `GEM_HOST_API_KEY=<TOKEN> gem push my-gem-1.0.0.gem --host ${base}`,
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
    case 'conda':
      return [
        {
          client: 'conda',
          configure: `conda config --add channels ${base}`,
          publish: `curl -u <USERNAME>:<TOKEN> -H "X-Filename: my-package-1.0-0.conda" --data-binary @my-package-1.0-0.conda ${base}/linux-64`,
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
    case 'sbt':
      return [
        {
          client: 'sbt',
          configure: `// build.sbt\nresolvers += "${repo}" at "${base}"\ncredentials += Credentials("openartifact", "${hostnameOf(origin)}", "<USERNAME>", "<TOKEN>")`,
          publish: `ThisBuild / publishTo := Some("${repo}" at "${base}")\n// then: sbt publish`,
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
    default:
      return [{ client: 'URL', configure: `${base}/` }]
  }
}
