// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { PackageDetail } from '../../api/generated/types.gen'

const KiB = 1024
const MiB = 1024 * KiB

/** One version: when it was published (hours before load), by whom, and its files. */
export interface DemoVersion {
  version: string
  hoursAgo: number
  by: string
  files: { filename: string; size: number; arch?: string }[]
}

/** One package of a repository with the metadata its detail page shows. */
export interface DemoPackage extends Partial<Pick<PackageDetail, 'summary' | 'description' | 'license' | 'homepage'>> {
  repository: string
  name: string
  dependencies: PackageDetail['dependencies']
  metadata: PackageDetail['metadata']
  versions: DemoVersion[]
}

const npmTgz = (name: string, v: string) => `${name.replace(/^@[^/]+\//, '')}-${v}.tgz`

function npm(repository: string, name: string, by: string, versions: [string, number, number][], rest: Omit<DemoPackage, 'repository' | 'name' | 'versions'>): DemoPackage {
  return {
    repository,
    name,
    ...rest,
    versions: versions.map(([version, hoursAgo, size]) => ({ version, hoursAgo, by, files: [{ filename: npmTgz(name, version), size }] })),
  }
}

export const PACKAGES: DemoPackage[] = [
  npm(
    'npm-internal',
    '@acme/ui-kit',
    'carla.rossi',
    [
      ['3.2.0', 4, 412 * KiB],
      ['3.1.4', 24 * 6, 409 * KiB],
      ['3.1.0', 24 * 21, 398 * KiB],
      ['2.9.1', 24 * 70, 377 * KiB],
    ],
    {
      summary: 'Vue 3 components of the Acme design system',
      description: 'Buttons, forms, tables and layout primitives with light and dark themes. Tree-shakable ES modules with TypeScript types.',
      license: 'MIT',
      homepage: 'https://design.acme.example',
      dependencies: [
        { name: 'vue', version: '^3.5.0', kind: 'peer' },
        { name: '@acme/tokens', version: '^1.4.0', kind: 'runtime' },
        { name: '@floating-ui/dom', version: '^1.6.0', kind: 'runtime' },
      ],
      metadata: [
        { key: 'dist-tags.latest', value: '3.2.0' },
        { key: 'engines.node', value: '>=20' },
      ],
    },
  ),
  npm(
    'npm-internal',
    '@acme/api-client',
    'alice.chen',
    [
      ['1.8.2', 30, 86 * KiB],
      ['1.8.0', 24 * 12, 85 * KiB],
      ['1.7.5', 24 * 40, 81 * KiB],
    ],
    {
      summary: 'Typed client for the Acme public API',
      license: 'Apache-2.0',
      dependencies: [{ name: 'ky', version: '^1.7.0', kind: 'runtime' }],
      metadata: [{ key: 'dist-tags.latest', value: '1.8.2' }],
    },
  ),
  npm(
    'npm-internal',
    '@acme/eslint-config',
    'alice.chen',
    [
      ['5.0.1', 24 * 9, 12 * KiB],
      ['5.0.0', 24 * 33, 12 * KiB],
    ],
    {
      summary: 'Shared ESLint flat config',
      license: 'MIT',
      dependencies: [{ name: 'eslint', version: '^9.0.0', kind: 'peer' }],
      metadata: [],
    },
  ),
  npm(
    'npmjs',
    'express',
    'system',
    [
      ['4.21.2', 24 * 2, 54 * KiB],
      ['4.18.2', 24 * 120, 53 * KiB],
    ],
    {
      summary: 'Fast, unopinionated, minimalist web framework',
      license: 'MIT',
      homepage: 'https://expressjs.com/',
      dependencies: [
        { name: 'body-parser', version: '1.20.3', kind: 'runtime' },
        { name: 'cookie', version: '0.7.1', kind: 'runtime' },
      ],
      metadata: [],
    },
  ),
  npm('npmjs', 'lodash', 'system', [['4.17.21', 24 * 15, 311 * KiB]], {
    summary: 'Lodash modular utilities',
    license: 'MIT',
    homepage: 'https://lodash.com/',
    dependencies: [],
    metadata: [],
  }),
  npm('npmjs', 'vue', 'system', [['3.5.13', 24 * 4, 2 * MiB]], {
    summary: 'The progressive JavaScript framework for building modern web UI',
    license: 'MIT',
    homepage: 'https://vuejs.org/',
    dependencies: [{ name: '@vue/runtime-dom', version: '3.5.13', kind: 'runtime' }],
    metadata: [],
  }),
  {
    repository: 'maven-releases',
    name: 'com.acme.payments:payments-core',
    summary: 'Ledger, settlement and refund domain of the payments platform',
    license: 'Proprietary',
    dependencies: [
      { name: 'org.springframework.boot:spring-boot-starter', version: '3.4.1', kind: 'compile' },
      { name: 'com.fasterxml.jackson.core:jackson-databind', version: '2.18.2', kind: 'compile' },
      { name: 'org.junit.jupiter:junit-jupiter', version: '5.11.4', kind: 'test' },
    ],
    metadata: [
      { key: 'groupId', value: 'com.acme.payments' },
      { key: 'artifactId', value: 'payments-core' },
      { key: 'packaging', value: 'jar' },
    ],
    versions: [
      ['2.4.0', 6],
      ['2.3.1', 24 * 8],
      ['2.3.0', 24 * 29],
    ].map(([v, h]) => ({
      version: String(v),
      hoursAgo: Number(h),
      by: 'bob.martin',
      files: [
        { filename: `payments-core-${v}.jar`, size: 1.8 * MiB },
        { filename: `payments-core-${v}.pom`, size: 6 * KiB },
        { filename: `payments-core-${v}-sources.jar`, size: 940 * KiB },
      ],
    })),
  },
  {
    repository: 'maven-releases',
    name: 'com.acme.payments:ledger-api',
    summary: 'Public API contract of the ledger service',
    license: 'Proprietary',
    dependencies: [{ name: 'jakarta.validation:jakarta.validation-api', version: '3.1.0', kind: 'compile' }],
    metadata: [
      { key: 'groupId', value: 'com.acme.payments' },
      { key: 'artifactId', value: 'ledger-api' },
    ],
    versions: [
      { version: '1.12.0', hoursAgo: 24 * 3, by: 'fatima.khan', files: [{ filename: 'ledger-api-1.12.0.jar', size: 210 * KiB }, { filename: 'ledger-api-1.12.0.pom', size: 4 * KiB }] },
      { version: '1.11.3', hoursAgo: 24 * 25, by: 'fatima.khan', files: [{ filename: 'ledger-api-1.11.3.jar', size: 205 * KiB }, { filename: 'ledger-api-1.11.3.pom', size: 4 * KiB }] },
    ],
  },
  {
    repository: 'pypi-ml',
    name: 'acme-features',
    summary: 'Feature store client with pandas and Polars adapters',
    license: 'BSD-3-Clause',
    homepage: 'https://docs.acme.example/features',
    dependencies: [
      { name: 'pandas', version: '>=2.1', kind: 'requires_dist' },
      { name: 'pyarrow', version: '>=15', kind: 'requires_dist' },
      { name: 'requests', version: '>=2.31', kind: 'requires_dist' },
    ],
    metadata: [{ key: 'requires_python', value: '>=3.10' }],
    versions: ['0.9.2', '0.9.1', '0.8.0'].map((v, i) => ({
      version: v,
      hoursAgo: [10, 24 * 14, 24 * 60][i] ?? 0,
      by: 'fatima.khan',
      files: [
        { filename: `acme_features-${v}-py3-none-any.whl`, size: 148 * KiB },
        { filename: `acme_features-${v}.tar.gz`, size: 121 * KiB },
      ],
    })),
  },
  {
    repository: 'pypi-ml',
    name: 'acme-model-serving',
    summary: 'gRPC model server with ONNX and Torch runtimes',
    license: 'BSD-3-Clause',
    dependencies: [
      { name: 'grpcio', version: '>=1.62', kind: 'requires_dist' },
      { name: 'onnxruntime', version: '>=1.18', kind: 'requires_dist' },
    ],
    metadata: [{ key: 'requires_python', value: '>=3.11' }],
    versions: [
      { version: '1.3.0', hoursAgo: 24 * 5, by: 'fatima.khan', files: [{ filename: 'acme_model_serving-1.3.0-py3-none-any.whl', size: 2.4 * MiB }] },
      { version: '1.2.2', hoursAgo: 24 * 41, by: 'fatima.khan', files: [{ filename: 'acme_model_serving-1.2.2-py3-none-any.whl', size: 2.3 * MiB }] },
    ],
  },
  {
    repository: 'containers',
    name: 'payments/api',
    summary: 'Payments API service',
    dependencies: [],
    metadata: [
      { key: 'org.opencontainers.image.source', value: 'https://git.acme.example/payments/api' },
      { key: 'architecture', value: 'linux/amd64, linux/arm64' },
    ],
    versions: [
      { version: '2.4.0', hoursAgo: 5, by: 'bob.martin', files: [{ filename: 'sha256:manifest', size: 84 * MiB, arch: 'multi' }] },
      { version: '2.3.1', hoursAgo: 24 * 8, by: 'bob.martin', files: [{ filename: 'sha256:manifest', size: 83 * MiB, arch: 'multi' }] },
      { version: '2.3.0', hoursAgo: 24 * 29, by: 'bob.martin', files: [{ filename: 'sha256:manifest', size: 83 * MiB, arch: 'multi' }] },
    ],
  },
  {
    repository: 'containers',
    name: 'web/checkout',
    summary: 'Checkout front end (nginx)',
    dependencies: [],
    metadata: [{ key: 'architecture', value: 'linux/amd64' }],
    versions: [
      { version: '1.15.0', hoursAgo: 20, by: 'carla.rossi', files: [{ filename: 'sha256:manifest', size: 31 * MiB, arch: 'amd64' }] },
      { version: '1.14.2', hoursAgo: 24 * 16, by: 'carla.rossi', files: [{ filename: 'sha256:manifest', size: 31 * MiB, arch: 'amd64' }] },
    ],
  },
  {
    repository: 'charts',
    name: 'payments-api',
    summary: 'Deploys the payments API with its HPA and network policies',
    license: 'Apache-2.0',
    dependencies: [{ name: 'redis', version: '20.x.x', kind: 'chart' }],
    metadata: [
      { key: 'appVersion', value: '2.4.0' },
      { key: 'apiVersion', value: 'v2' },
    ],
    versions: [
      { version: '0.7.0', hoursAgo: 5, by: 'carla.rossi', files: [{ filename: 'payments-api-0.7.0.tgz', size: 18 * KiB }] },
      { version: '0.6.2', hoursAgo: 24 * 8, by: 'carla.rossi', files: [{ filename: 'payments-api-0.6.2.tgz', size: 17 * KiB }] },
    ],
  },
  {
    repository: 'charts',
    name: 'ingress-gateway',
    summary: 'Envoy gateway with the shared TLS and rate-limit settings',
    license: 'Apache-2.0',
    dependencies: [],
    metadata: [{ key: 'appVersion', value: '1.32.1' }],
    versions: [{ version: '1.2.0', hoursAgo: 24 * 11, by: 'alice.chen', files: [{ filename: 'ingress-gateway-1.2.0.tgz', size: 22 * KiB }] }],
  },
  {
    repository: 'crates',
    name: 'acme-telemetry',
    summary: 'OpenTelemetry setup shared by the Rust services',
    license: 'MIT OR Apache-2.0',
    dependencies: [
      { name: 'opentelemetry', version: '^0.27', kind: 'normal' },
      { name: 'tracing', version: '^0.1', kind: 'normal' },
    ],
    metadata: [{ key: 'edition', value: '2021' }],
    versions: [
      { version: '0.4.1', hoursAgo: 24 * 2, by: 'erik.nilsson', files: [{ filename: 'acme-telemetry-0.4.1.crate', size: 28 * KiB }] },
      { version: '0.4.0', hoursAgo: 24 * 19, by: 'erik.nilsson', files: [{ filename: 'acme-telemetry-0.4.0.crate', size: 27 * KiB }] },
    ],
  },
  {
    repository: 'go-modules',
    name: 'go.acme.example/platform/auth',
    summary: 'Token verification and request signing middleware',
    license: 'BSD-3-Clause',
    dependencies: [{ name: 'github.com/golang-jwt/jwt/v5', version: 'v5.2.1', kind: 'require' }],
    metadata: [{ key: 'go', value: '1.23' }],
    versions: [
      { version: 'v1.6.0', hoursAgo: 24 * 1.5, by: 'bob.martin', files: [{ filename: 'v1.6.0.zip', size: 96 * KiB }, { filename: 'v1.6.0.mod', size: 1 * KiB }] },
      { version: 'v1.5.2', hoursAgo: 24 * 36, by: 'bob.martin', files: [{ filename: 'v1.5.2.zip', size: 93 * KiB }, { filename: 'v1.5.2.mod', size: 1 * KiB }] },
    ],
  },
  {
    repository: 'debian',
    name: 'acme-agent',
    summary: 'Host agent that ships logs and metrics',
    license: 'Apache-2.0',
    dependencies: [
      { name: 'libc6', version: '>= 2.36', kind: 'depends' },
      { name: 'systemd', kind: 'recommends' },
    ],
    metadata: [
      { key: 'Section', value: 'admin' },
      { key: 'Distribution', value: 'bookworm' },
    ],
    versions: [
      {
        version: '2.1.0-1',
        hoursAgo: 24 * 3,
        by: 'erik.nilsson',
        files: [
          { filename: 'acme-agent_2.1.0-1_amd64.deb', size: 7.2 * MiB, arch: 'amd64' },
          { filename: 'acme-agent_2.1.0-1_arm64.deb', size: 6.8 * MiB, arch: 'arm64' },
        ],
      },
      { version: '2.0.3-1', hoursAgo: 24 * 45, by: 'erik.nilsson', files: [{ filename: 'acme-agent_2.0.3-1_amd64.deb', size: 7.1 * MiB, arch: 'amd64' }] },
    ],
  },
  {
    repository: 'rpm',
    name: 'acme-agent',
    summary: 'Host agent that ships logs and metrics',
    license: 'ASL 2.0',
    dependencies: [{ name: 'glibc', version: '>= 2.34', kind: 'requires' }],
    metadata: [{ key: 'Group', value: 'System Environment/Daemons' }],
    versions: [
      {
        version: '2.1.0-1.el9',
        hoursAgo: 24 * 3,
        by: 'erik.nilsson',
        files: [
          { filename: 'acme-agent-2.1.0-1.el9.x86_64.rpm', size: 7.4 * MiB, arch: 'x86_64' },
          { filename: 'acme-agent-2.1.0-1.el9.aarch64.rpm', size: 7.0 * MiB, arch: 'aarch64' },
        ],
      },
    ],
  },
  {
    repository: 'downloads',
    name: 'cli/acme-cli-linux-amd64.tar.gz',
    dependencies: [],
    metadata: [],
    versions: [{ version: '', hoursAgo: 24 * 2, by: 'alice.chen', files: [{ filename: 'acme-cli-linux-amd64.tar.gz', size: 11.6 * MiB }] }],
  },
  {
    repository: 'downloads',
    name: 'cli/install.sh',
    dependencies: [],
    metadata: [],
    versions: [{ version: '', hoursAgo: 24 * 2, by: 'alice.chen', files: [{ filename: 'install.sh', size: 4 * KiB }] }],
  },
]
