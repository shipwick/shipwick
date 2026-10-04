import { describe, expect, it } from 'vitest'

// The mock agent's writer of a handed-out deploy.yaml, and its reader: what
// the one writes, the other reads back as the same configuration.
const { documentOf, maskedFields, referencesOf, MASK } = await import('../mock/document.mjs' as string) as {
  documentOf: (spec: Record<string, unknown>, references?: { env?: Record<string, string>, basic_auth?: Record<number, string> }) => { document: string, masked: string[] }
  maskedFields: (body: unknown) => string[]
  referencesOf: (body: unknown) => { env: Record<string, string>, basic_auth: Record<number, string> }
  MASK: string
}
const { parseYaml } = await import('../mock/yaml.mjs' as string) as { parseYaml: (text: string) => Record<string, unknown> }

const spec = {
  name: 'my-api',
  image: 'ghcr.io/acme/my-api:1.4.2',
  port: 8080,
  domain: 'api.example.com',
  aliases: ['*.api.example.com'],
  replicas: 2,
  init: true,
  env: { LOG_LEVEL: MASK, DATABASE_URL: MASK },
  health: { path: '/health', interval: '10s', timeout: '3s', retries: 3, start_period: '2m0s' },
  resources: { cpu: 0.5, memory_bytes: 512 * 1024 ** 2 },
  volumes: [{ name: 'data', path: '/data' }],
  command: ['--queue', 'default', '--log-format', 'json lines'],
  user: '1000:1000',
  pre_deploy: { command: ['dotnet', 'Migrate.dll'], timeout: '10m0s' },
  jobs: [{ name: 'nightly', schedule: '0 3 * * *', command: ['node', 'report.js'], timeout: '1h0m0s' }],
  proxy: { headers: { 'X-Frame-Options': 'DENY' }, basic_auth: [{ path: '/admin', username: 'ops', password: MASK }] },
  backups: { schedule: '0 3 * * *', keep: 7, before: ['pg_dump', '-h', 'localhost'], before_timeout: '2h0m0s', before_in: 'container' },
  restart: { policy: 'always' },
  deploy: { strategy: 'recreate', stop_timeout: '30s' },
}

describe('documentOf', () => {
  const references = { env: { DATABASE_URL: 'postgres://api:${DB_PASSWORD}@db:5432/api' }, basic_auth: {} }
  const { document, masked } = documentOf(spec, references)

  it('writes a reference back and masks every other secret value, naming them in document order', () => {
    expect(masked).toEqual(['env.LOG_LEVEL', 'proxy.basic_auth[0].password'])
    expect(document).toContain('  DATABASE_URL: postgres://api:${DB_PASSWORD}@db:5432/api\n')
    expect(document).toContain('  LOG_LEVEL: "********" # not handed out: write the value again, or refer to a secret as ${NAME}\n')
  })

  it('opens with the notice only when something is masked', () => {
    expect(document.startsWith('# A value shown as "********" was given when the application was deployed and\n')).toBe(true)
    const plain = documentOf({ name: 'docs', image: 'nginx:1.27-alpine', replicas: 1, resources: {}, restart: { policy: 'always' }, deploy: { strategy: 'rolling' } })
    expect(plain.masked).toEqual([])
    expect(plain.document).toBe('name: docs\n\nimage: nginx:1.27-alpine\n\nreplicas: 1\n\nrestart:\n  policy: always\n')
  })

  it('writes durations without the zero units they end in, sizes in the largest exact unit, argv on one line', () => {
    expect(document).toContain('  start_period: 2m\n')
    expect(document).toContain('  timeout: 10m\n')
    expect(document).toContain('  before_timeout: 2h\n')
    expect(document).toContain('  memory: 512mb\n')
    expect(document).toContain('command: ["--queue", "default", "--log-format", "json lines"]\n')
    expect(document).toContain('  before_in: container\n')
  })

  it('is read back by the mock\'s own reader as the configuration it was written from', () => {
    const read = parseYaml(document)
    expect(read.name).toBe('my-api')
    expect(read.image).toBe('ghcr.io/acme/my-api:1.4.2')
    expect(read.port).toBe(8080)
    expect(read.init).toBe(true)
    expect(read.aliases).toEqual(['*.api.example.com'])
    expect(read.user).toBe('1000:1000')
    expect(read.command).toEqual(spec.command)
    expect(read.env).toEqual({ DATABASE_URL: references.env.DATABASE_URL, LOG_LEVEL: MASK })
    expect(read.volumes).toEqual([{ name: 'data', path: '/data' }])
    expect(read.jobs).toEqual([{ name: 'nightly', schedule: '0 3 * * *', command: ['node', 'report.js'], timeout: '1h' }])
    expect(read.proxy).toEqual({ headers: { 'X-Frame-Options': 'DENY' }, basic_auth: [{ path: '/admin', username: 'ops', password: MASK }] })
    expect(read.backups).toEqual({ schedule: '0 3 * * *', keep: 7, before: ['pg_dump', '-h', 'localhost'], before_timeout: '2h', before_in: 'container' })
    expect(read.deploy).toEqual({ strategy: 'recreate', stop_timeout: '30s' })
  })

  it('writes a static application as its folder, without replicas or a restart policy', () => {
    const written = documentOf({ name: 'landing', image: '', domain: 'acme.example.com', static: { dir: 'dist', fallback: 'index.html' }, replicas: 1, resources: {}, restart: { policy: 'always' }, deploy: { strategy: 'rolling' } })
    expect(written.document).toBe('name: landing\n\nstatic:\n  dir: dist\n  fallback: index.html\n\ndomain: acme.example.com\n')
  })
})

describe('what a document that arrives says about its secret values', () => {
  const body = parseYaml('name: x\nenv:\n  A: "********"\n  B: ${B}\n  C: $${C}\n  D: plain\nproxy:\n  basic_auth:\n    - username: u\n      password: "********"\n    - username: v\n      password: ${PW}\n')

  it('finds the masks, variables first and sorted', () => {
    expect(maskedFields(body)).toEqual(['env.A', 'proxy.basic_auth[0].password'])
    expect(maskedFields({ name: 'x' })).toEqual([])
  })

  it('keeps the values that refer to a stored secret, and not an escaped placeholder', () => {
    expect(referencesOf(body)).toEqual({ env: { B: '${B}' }, basic_auth: { 1: '${PW}' } })
  })
})
