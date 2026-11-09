// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { AnnotatedVulnerability, SecurityFinding } from '../../api/generated/types.gen'
import type { SecretFinding } from '../../api/security'
import { ago } from './time'

/** A finding before it is tied to the artifact id and scan time of its package version. */
export type DemoFinding = Omit<SecurityFinding, 'id' | 'artifact_id' | 'format' | 'scanned_at' | 'quarantined'> & {
  /** Vulnerability findings: the advisories of the scan, worst first. */
  advisories?: AnnotatedVulnerability[]
  /** License findings: the SPDX ids the scan read. */
  licenses?: string[]
  /** Secret findings: what the scanner matched, redacted. */
  secrets?: SecretFinding[]
}

const none = { critical: 0, high: 0, medium: 0, low: 0 }
const SEVERITIES = ['critical', 'high', 'medium', 'low'] as const

type Advisory = Pick<AnnotatedVulnerability, 'id' | 'summary' | 'cvss' | 'fixed_in' | 'package'> & {
  severity: (typeof SEVERITIES)[number]
  published: string
  kev?: string
  epss?: number
}

function advisory(a: Advisory): AnnotatedVulnerability {
  const epss = a.epss ?? 0.0005
  return {
    id: a.id,
    cve: a.id,
    summary: a.summary,
    severity: a.severity,
    cvss: a.cvss,
    fixed_in: a.fixed_in,
    package: a.package,
    published_at: `${a.published}T00:00:00Z`,
    references: [`https://nvd.nist.gov/vuln/detail/${a.id}`],
    kev: a.kev !== undefined,
    kev_date_added: a.kev,
    epss,
    epss_percentile: Math.min(0.999, 0.4 + epss),
    epss_date: ago(30).slice(0, 10),
    priority: a.kev ? 'kev' : epss >= 0.1 ? 'epss' : a.severity,
  }
}

/** A vulnerability finding whose counts, exploit signals and headline come from its advisories. */
function vuln(
  base: Pick<SecurityFinding, 'repository' | 'package_name' | 'package_version' | 'scanner' | 'status' | 'vex_status'>,
  list: Advisory[],
): DemoFinding {
  const advisories = list.map(advisory)
  const counts = { ...none }
  for (const a of list) counts[a.severity]++
  const worst = SEVERITIES.find((s) => counts[s] > 0) ?? 'low'
  const head = advisories[0] as AnnotatedVulnerability
  const kev = advisories.filter((a) => a.kev).length
  const maxEpss = Math.max(...advisories.map((a) => a.epss ?? 0))
  return {
    ...base,
    type: 'vuln',
    severity: worst,
    counts,
    title: `${head.id} ${head.package}: ${head.summary}`,
    kev_count: kev,
    max_epss: maxEpss,
    priority: kev > 0 ? 'kev' : maxEpss >= 0.1 ? 'epss' : worst,
    advisories,
  }
}

const OPENSSL: Advisory = { id: 'CVE-2024-2511', package: 'libssl3', summary: 'Unbounded memory growth with session handling in TLSv1.3', severity: 'medium', cvss: 5.9, fixed_in: '3.0.14-1~deb12u1', published: '2024-04-08' }
const NCURSES: Advisory = { id: 'CVE-2023-50495', package: 'libncursesw6', summary: 'Segmentation fault via _nc_wrap_entry()', severity: 'medium', cvss: 6.5, fixed_in: '', published: '2023-12-12' }
const NCURSES_LOW: Advisory = { id: 'CVE-2023-45918', package: 'libtinfo6', summary: 'NULL pointer dereference in tgetstr', severity: 'low', cvss: 3.3, fixed_in: '', published: '2024-02-16' }

export const FINDINGS: DemoFinding[] = [
  vuln({ repository: 'containers', package_name: 'payments/api', package_version: '2.3.0', scanner: 'trivy', status: 'quarantined' }, [
    { id: 'CVE-2023-44487', package: 'golang.org/x/net', summary: 'HTTP/2 rapid reset can cause excessive work', severity: 'high', cvss: 7.5, fixed_in: '0.17.0', published: '2023-10-10', kev: '2023-10-10', epss: 0.82 },
    { id: 'CVE-2024-45337', package: 'golang.org/x/crypto', summary: 'Misuse of ServerConfig.PublicKeyCallback may cause authorization bypass', severity: 'critical', cvss: 9.1, fixed_in: '0.31.0', published: '2024-12-12', epss: 0.024 },
    { id: 'CVE-2023-4863', package: 'libwebp7', summary: 'Heap buffer overflow in WebP lossless decoding', severity: 'high', cvss: 8.8, fixed_in: '1.2.4-0.2+deb12u1', published: '2023-09-12', kev: '2023-09-13', epss: 0.47 },
    OPENSSL,
    NCURSES,
    NCURSES_LOW,
  ]),
  vuln({ repository: 'containers', package_name: 'payments/api', package_version: '2.4.0', scanner: 'trivy', status: 'open' }, [OPENSSL, NCURSES, NCURSES_LOW]),
  vuln({ repository: 'npmjs', package_name: 'express', package_version: '4.18.2', scanner: 'osv', status: 'open' }, [
    { id: 'CVE-2024-45590', package: 'body-parser', summary: 'Denial of service when url encoding is enabled', severity: 'high', cvss: 7.5, fixed_in: '1.20.3', published: '2024-09-10', epss: 0.0012 },
    { id: 'CVE-2024-45296', package: 'path-to-regexp', summary: 'Backtracking regular expressions cause ReDoS', severity: 'high', cvss: 7.5, fixed_in: '0.1.10', published: '2024-09-09', epss: 0.0021 },
    { id: 'CVE-2024-29041', package: 'express', summary: 'Open redirect in malformed URLs', severity: 'medium', cvss: 6.1, fixed_in: '4.19.2', published: '2024-03-25' },
    { id: 'CVE-2024-43796', package: 'express', summary: 'XSS via response.redirect()', severity: 'medium', cvss: 5.0, fixed_in: '4.20.0', published: '2024-09-10' },
  ]),
  vuln({ repository: 'pypi-ml', package_name: 'acme-model-serving', package_version: '1.2.2', scanner: 'osv', status: 'open' }, [
    { id: 'CVE-2024-7254', package: 'protobuf', summary: 'Stack overflow parsing nested groups', severity: 'high', cvss: 7.5, fixed_in: '4.25.5', published: '2024-09-19', epss: 0.0009 },
    { id: 'CVE-2024-35195', package: 'requests', summary: 'Session keeps verify=False for later requests to the same host', severity: 'medium', cvss: 5.6, fixed_in: '2.32.0', published: '2024-05-20' },
    { id: 'CVE-2024-3651', package: 'idna', summary: 'Quadratic complexity in idna.encode()', severity: 'medium', cvss: 6.2, fixed_in: '3.7', published: '2024-07-07' },
  ]),
  vuln({ repository: 'maven-releases', package_name: 'com.acme.payments:payments-core', package_version: '2.3.0', scanner: 'grype', status: 'suppressed', vex_status: 'not_affected' }, [
    { id: 'CVE-2022-42889', package: 'org.apache.commons:commons-text', summary: 'Variable interpolation allows remote code execution (Text4Shell)', severity: 'critical', cvss: 9.8, fixed_in: '1.10.0', published: '2022-10-13', epss: 0.97 },
    { id: 'CVE-2022-25857', package: 'org.yaml:snakeyaml', summary: 'Denial of service through nested collections', severity: 'high', cvss: 7.5, fixed_in: '1.31', published: '2022-08-30', epss: 0.0064 },
  ]),
  {
    type: 'license',
    repository: 'npm-internal',
    package_name: '@acme/ui-kit',
    package_version: '2.9.1',
    severity: 'high',
    counts: { ...none, high: 1 },
    title: 'GPL-3.0-only dependency in a package licensed MIT',
    licenses: ['MIT', 'GPL-3.0-only'],
    scanner: 'license',
    status: 'open',
    kev_count: 0,
    max_epss: null,
    priority: 'high',
  },
  {
    type: 'license',
    repository: 'crates',
    package_name: 'acme-telemetry',
    package_version: '0.4.0',
    severity: 'medium',
    counts: { ...none, medium: 1 },
    title: 'License not on the allow list: MPL-2.0',
    licenses: ['Apache-2.0', 'MPL-2.0'],
    scanner: 'license',
    status: 'open',
    kev_count: 0,
    max_epss: null,
    priority: 'medium',
  },
  {
    type: 'secret',
    repository: 'pypi-ml',
    package_name: 'acme-features',
    package_version: '0.9.1',
    severity: 'critical',
    counts: { ...none, critical: 1 },
    title: 'AWS access key in tests/fixtures/settings.yaml',
    secrets: [
      { type: 'aws-access-key-id', description: 'AWS access key ID', severity: 'critical', redacted_value: 'AKIA************7Q2M', line_hint: 'tests/fixtures/settings.yaml:8' },
      { type: 'aws-secret-access-key', description: 'AWS secret access key', severity: 'critical', redacted_value: 'wJal********************************EKEY', line_hint: 'tests/fixtures/settings.yaml:9' },
    ],
    scanner: 'secrets',
    status: 'quarantined',
    kev_count: 0,
    max_epss: null,
    priority: 'critical',
  },
  {
    type: 'secret',
    repository: 'downloads',
    package_name: 'cli/install.sh',
    package_version: '',
    severity: 'high',
    counts: { ...none, high: 1 },
    title: 'GitHub personal access token on line 12',
    secrets: [{ type: 'github-pat', description: 'GitHub personal access token', severity: 'high', redacted_value: 'ghp_********************************x9Qk', line_hint: 'line 12' }],
    scanner: 'secrets',
    status: 'open',
    kev_count: 0,
    max_epss: null,
    priority: 'high',
  },
]
