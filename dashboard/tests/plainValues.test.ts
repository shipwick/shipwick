import { describe, expect, it } from 'vitest'
import { keptPlain } from '../app/utils/deployDocument'

const { documentOf, plainOf } = await import('../mock/document.mjs' as string) as {
  documentOf: (spec: Record<string, unknown>, references?: { env?: Record<string, string>, plain?: Record<string, string> }) => { document: string, masked: string[], plain: string[] }
  plainOf: (body: unknown, fields: string[]) => { plain: Record<string, string>, refused: string }
}
const { parseYaml } = await import('../mock/yaml.mjs' as string) as { parseYaml: (text: string) => Record<string, unknown> }

// What GET /applications/:name/config hands out for an application deployed
// with two plain values, one reference and one value the CLI filled in.
const DOCUMENT = `name: my-api

image: ghcr.io/acme/my-api:1.4.2

env:
  API_KEY: "********" # not handed out: write the value again, or refer to a secret as \${NAME}
  DATABASE_URL: postgres://api:\${POSTGRES_PASSWORD}@postgres:5432/api
  LOG_LEVEL: info
  MOTD: |-
    one
    two
  REGION: eu-central

logging:
  driver: json-file
  options:
    REGION: not-a-variable

restart:
  policy: always
`

const PLAIN = ['env.LOG_LEVEL', 'env.MOTD', 'env.REGION']

describe('the plain values a deployment from the editor vouches for', () => {
  it('are all of them while the document is as it was handed out', () => {
    expect(keptPlain(DOCUMENT, DOCUMENT, PLAIN)).toEqual(PLAIN)
  })

  it('are still all of them after a change elsewhere in the document', () => {
    const edited = DOCUMENT.replace('my-api:1.4.2', 'my-api:1.4.3').replace('API_KEY: "********" # not handed out: write the value again, or refer to a secret as ${NAME}', 'API_KEY: ${API_KEY}')
    expect(keptPlain(edited, DOCUMENT, PLAIN)).toEqual(PLAIN)
  })

  it('leave out a value that was edited: what was typed here is not what the agent showed', () => {
    expect(keptPlain(DOCUMENT.replace('LOG_LEVEL: info', 'LOG_LEVEL: debug'), DOCUMENT, PLAIN)).toEqual(['env.MOTD', 'env.REGION'])
    expect(keptPlain(DOCUMENT.replace('    two', '    three'), DOCUMENT, PLAIN)).toEqual(['env.LOG_LEVEL', 'env.REGION'])
  })

  it('leave out a variable that is gone, so that the agent is not told about a value the document does not have', () => {
    expect(keptPlain(DOCUMENT.replace('  REGION: eu-central\n', ''), DOCUMENT, PLAIN)).toEqual(['env.LOG_LEVEL', 'env.MOTD'])
    expect(keptPlain('name: my-api\nimage: nginx\n', DOCUMENT, PLAIN)).toEqual([])
  })

  it('look for a variable in the env block only', () => {
    const moved = DOCUMENT.replace('  REGION: eu-central\n', '')
    expect(moved).toContain('    REGION: not-a-variable')
    expect(keptPlain(moved, DOCUMENT, ['env.REGION'])).toEqual([])
  })

  it('never include a field the agent did not name, or one that is not a variable', () => {
    expect(keptPlain(DOCUMENT, DOCUMENT, [])).toEqual([])
    expect(keptPlain(DOCUMENT, DOCUMENT, ['env.LOG_LEVEL', 'proxy.basic_auth[0].password', 'name'])).toEqual(['env.LOG_LEVEL'])
  })

  it('say nothing about a document rewritten as JSON', () => {
    expect(keptPlain('{"name": "my-api", "image": "nginx", "env": {"LOG_LEVEL": "info"}}', DOCUMENT, PLAIN)).toEqual([])
  })
})

describe('the mock agent and plain values', () => {
  const spec = { name: 'my-api', image: 'nginx:1', replicas: 1, env: { API_KEY: '********', DATABASE_URL: '********', LOG_LEVEL: '********', WORKERS: '********' }, resources: {}, restart: { policy: 'always' }, deploy: { strategy: 'rolling' } }

  it('writes a value its deployment called plain as it is, and names it', () => {
    const written = documentOf(spec, { env: { DATABASE_URL: 'postgres://api:${DB_PASSWORD}@db/api' }, plain: { LOG_LEVEL: 'info', WORKERS: '4', DATABASE_URL: 'ignored: it is a reference' } })
    expect(written.plain).toEqual(['env.LOG_LEVEL', 'env.WORKERS'])
    expect(written.masked).toEqual(['env.API_KEY'])
    expect(written.document).toContain('  LOG_LEVEL: info\n')
    expect(written.document).toContain('  WORKERS: "4"\n')
    expect(parseYaml(written.document).env).toEqual({ API_KEY: '********', DATABASE_URL: 'postgres://api:${DB_PASSWORD}@db/api', LOG_LEVEL: 'info', WORKERS: '4' })
  })

  it('masks every value of a deployment that said nothing', () => {
    const written = documentOf(spec, { env: {} })
    expect(written.plain).toEqual([])
    expect(written.masked).toEqual(['env.API_KEY', 'env.DATABASE_URL', 'env.LOG_LEVEL', 'env.WORKERS'])
  })

  it('takes the statement for env values of the document and refuses any other field', () => {
    const body = parseYaml('name: my-api\nimage: nginx:1\nenv:\n  LOG_LEVEL: info\n  WORKERS: 4\n  DATABASE_URL: postgres://api:${DB_PASSWORD}@db/api\n')
    expect(plainOf(body, ['env.LOG_LEVEL', 'env.WORKERS', 'env.DATABASE_URL'])).toEqual({ plain: { LOG_LEVEL: 'info', WORKERS: '4' }, refused: '' })
    expect(plainOf(body, ['env.LOG_LEVEL', 'env.GONE']).refused).toBe('env.GONE')
    expect(plainOf(body, ['LOG_LEVEL']).refused).toBe('LOG_LEVEL')
    expect(plainOf({ name: 'x' }, ['env.LOG_LEVEL']).refused).toBe('env.LOG_LEVEL')
    expect(plainOf(body, [])).toEqual({ plain: {}, refused: '' })
  })
})
