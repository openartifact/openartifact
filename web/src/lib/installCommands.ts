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

/** Install/pull command for a package version, built from the real server origin. */
export function pullCommand(
  format: string,
  origin: string,
  repo: string,
  pkg: string,
  version: string,
): string {
  const base = repoBaseUrl(format, origin, repo)
  switch (format) {
    case 'docker':
      return `docker pull ${hostOf(origin)}/${repo}/${pkg}:${version}`
    case 'npm':
      return `npm install ${pkg}@${version} --registry=${base}/`
    case 'pypi':
      return `pip install ${pkg}==${version} --index-url ${base}/simple/`
    case 'maven':
      return `mvn dependency:get -Dartifact=${pkg}:${version} -DremoteRepositories=${base}`
    case 'helm':
      return `helm pull ${pkg} --repo ${base} --version ${version}`
    case 'cargo':
      return `cargo install ${pkg} --version ${version} --index sparse+${base}/index/`
    case 'nuget':
      return `dotnet add package ${pkg} --version ${version} --source ${base}/v3/index.json`
    case 'gomod':
      return `GOPROXY=${base} go get ${pkg}@${version}`
    case 'rubygems':
      return `gem install ${pkg} -v ${version} --source ${base}/`
    case 'conda':
      return `conda install -c ${base} ${pkg}=${version}`
    case 'conan':
      return `conan install --requires=${pkg}/${version} -r ${repo}`
    case 'deb':
      return `apt install ${pkg}=${version}`
    case 'rpm':
      return `dnf install ${pkg}-${version}`
    case 'raw':
      return `curl -fO ${base}/${pkg}`
    case 'sbt':
      return `resolvers += "${repo}" at "${base}"`
    case 'ivy':
      return `<ibiblio name="${repo}" m2compatible="true" root="${base}"/>`
    default:
      return `${base}/`
  }
}
