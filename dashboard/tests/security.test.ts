import { describe, expect, it } from 'vitest'
import type { SpecSecurity } from '../app/types/api'
import { describeSecurity } from '../app/utils/spec'

interface Problem { field: string, message: string, expected?: string }
const { parseSecurity, rootRefusal, rootUser, securityLines, DEFAULT_CAPABILITIES } = await import('../mock/security.mjs' as string) as {
  parseSecurity: (block: unknown, context?: { volumes?: { name: string, path: string }[], user?: string, isStatic?: boolean }) => { security: SpecSecurity | undefined, problems: Problem[] }
  rootRefusal: (image: string, user: string, imageUser: string) => string
  rootUser: (user: string) => string
  securityLines: (security: SpecSecurity | undefined, str: (value: string) => string) => string[]
  DEFAULT_CAPABILITIES: string[]
}
const { documentOf } = await import('../mock/document.mjs' as string) as { documentOf: (spec: Record<string, unknown>) => { document: string } }
const { parseYaml } = await import('../mock/yaml.mjs' as string) as { parseYaml: (text: string) => Record<string, unknown> }

const MB = 1024 ** 2

describe('describeSecurity', () => {
  it('says each thing the containers go without, in the order of deploy.yaml', () => {
    expect(describeSecurity({
      read_only: true,
      tmpfs: [{ path: '/tmp', size_bytes: 64 * MB }, { path: '/var/cache/api', size_bytes: 200 * MB }],
      capabilities: [],
      non_root: true,
    })).toEqual([
      'Read-only root filesystem',
      'Scratch space: /tmp (64 MB), /var/cache/api (200 MB)',
      'Capabilities: none',
      'Refuses to run as root',
    ])
  })

  it('names the capabilities that are kept', () => {
    expect(describeSecurity({ capabilities: ['CHOWN', 'SETGID', 'SETUID'] })).toEqual(['Capabilities kept: CHOWN, SETGID, SETUID'])
  })

  it('says nothing about capabilities when the key is absent: Docker\'s default set is kept, which is not "none"', () => {
    const lines = describeSecurity({ read_only: true })
    expect(lines).toEqual(['Read-only root filesystem'])
    expect(lines.join(' ')).not.toMatch(/capabilit/i)
  })

  it('is empty without the block, as an agent before 0.8 answers', () => {
    expect(describeSecurity(undefined)).toEqual([])
    expect(describeSecurity(null)).toEqual([])
    expect(describeSecurity({})).toEqual([])
  })
})

describe('the mock agent reads the security block as the agent does', () => {
  it('normalises it: the default size, names in upper case without CAP_ and sorted, none as an empty list', () => {
    expect(parseSecurity({ read_only: true, tmpfs: ['/tmp', { path: '/var/cache/api', size: '200mb' }], capabilities: 'none', non_root: true }, { user: '1000:1000' })).toEqual({
      security: { read_only: true, tmpfs: [{ path: '/tmp', size_bytes: 67108864 }, { path: '/var/cache/api', size_bytes: 209715200 }], capabilities: [], non_root: true },
      problems: [],
    })
    expect(parseSecurity({ capabilities: ['setuid', 'CAP_CHOWN', ' SetGid '] }).security).toEqual({ capabilities: ['CHOWN', 'SETGID', 'SETUID'] })
  })

  it('keeps the three states of capabilities apart', () => {
    expect(parseSecurity({ read_only: true }).security).toEqual({ read_only: true })
    expect(parseSecurity({ read_only: true }).security).not.toHaveProperty('capabilities')
    expect(parseSecurity({ capabilities: 'none' }).security).toEqual({ capabilities: [] })
    expect(parseSecurity({ capabilities: ['KILL'] }).security).toEqual({ capabilities: ['KILL'] })
  })

  it('answers no block for one that asks for nothing, and for none', () => {
    expect(parseSecurity({ read_only: false, non_root: false, tmpfs: [] })).toEqual({ security: undefined, problems: [] })
    expect(parseSecurity(undefined)).toEqual({ security: undefined, problems: [] })
  })

  const refusal = (block: unknown, context = {}) => parseSecurity(block, context).problems.map(p => `${p.field}: ${p.message}`)

  it('refuses a block that is not a mapping, a key it does not know, and the block next to static', () => {
    expect(refusal(true)).toEqual(['security: must be a block'])
    expect(refusal({ readonly: true })).toEqual(['security: unknown field "readonly"'])
    expect(refusal({ read_only: true }, { isStatic: true })).toEqual(['security: does not apply to a static application: the proxy serves the files, there is no container'])
  })

  it('refuses a word other than none, an empty list, a capability outside Docker\'s default set and one listed twice', () => {
    expect(refusal({ capabilities: 'ALL' })).toEqual(['security.capabilities: invalid value "ALL"'])
    expect(refusal({ capabilities: [] })).toEqual(['security.capabilities: must not be empty; write none to keep no capability, or omit it to keep Docker\'s default set'])
    expect(refusal({ capabilities: ['CHOWN', 'SYS_ADMIN', 'chown'] })).toEqual([
      'security.capabilities[1]: invalid value "SYS_ADMIN": not in Docker\'s default set, and nothing is added to it',
      'security.capabilities[2]: "CHOWN" is listed twice',
    ])
    expect(parseSecurity({ capabilities: 'ALL' }).problems[0]!.expected).toBe(`none, or a list of the ones to keep out of ${DEFAULT_CAPABILITIES.join(', ')}`)
  })

  it('refuses a tmpfs path that is missing, relative, not clean, the root or mounted already, and a size it cannot read or that is out of range', () => {
    expect(refusal({ tmpfs: [''] })).toEqual(['security.tmpfs[0].path: is required'])
    expect(refusal({ tmpfs: ['tmp', '/', '/a/../b', '/tmp/'] })).toEqual([
      'security.tmpfs[0].path: invalid value "tmp"',
      'security.tmpfs[1].path: invalid value "/"',
      'security.tmpfs[2].path: invalid value "/a/../b"',
      'security.tmpfs[3].path: invalid value "/tmp/"',
    ])
    expect(refusal({ tmpfs: ['/data', '/tmp', '/tmp'] }, { volumes: [{ name: 'data', path: '/data' }] })).toEqual([
      'security.tmpfs[0].path: "/data" is already mounted: volume data',
      'security.tmpfs[2].path: "/tmp" is already mounted: security.tmpfs',
    ])
    expect(refusal({ tmpfs: [{ path: '/tmp', size: 'plenty' }, { path: '/var/tmp', size: '2gb' }, { path: '/run', size: '512kb' }] })).toEqual([
      'security.tmpfs[0].size: invalid value "plenty"',
      'security.tmpfs[1].size: invalid value "2gb": out of range',
      'security.tmpfs[2].size: invalid value "512kb": out of range',
    ])
    expect(refusal({ tmpfs: Array.from({ length: 11 }, (_, i) => `/t${i}`) })).toEqual(['security.tmpfs: too many (11)'])
  })

  it('refuses, under non_root, a user of the document that is root, the id 0 or a name; and leaves a document without a user to the image', () => {
    expect(refusal({ non_root: true }, { user: 'root' })).toEqual(['user: invalid value "root" next to security.non_root: it is root'])
    expect(refusal({ non_root: true }, { user: '0:0' })).toEqual(['user: invalid value "0:0" next to security.non_root: id 0 is root'])
    expect(refusal({ non_root: true }, { user: 'node' })).toEqual(['user: invalid value "node" next to security.non_root: it is a name, and which id a name stands for is in the image\'s /etc/passwd, which is not read'])
    expect(refusal({ non_root: true }, { user: '1000' })).toEqual([])
    expect(refusal({ non_root: true }, { user: '' })).toEqual([])
    expect(refusal({ read_only: true }, { user: 'root' })).toEqual([])
  })
})

describe('what non_root says about an image', () => {
  it('is the agent\'s sentence for an image that names no user, with the remedy in its second half', () => {
    expect(rootRefusal('nginx:alpine', '', '')).toBe('security.non_root refuses image nginx:alpine: it names no user, and a container without one runs as root; set user in deploy.yaml to a numeric id the image can run as, e.g. user: "1000:1000", or build the image with a USER instruction')
  })

  it('names the image\'s user when that is root or a name', () => {
    expect(rootRefusal('node:22', '', 'node')).toBe('security.non_root refuses image node:22, which runs as user "node": it is a name, and which id a name stands for is in the image\'s /etc/passwd, which is not read; set user in deploy.yaml to a numeric id the image can run as, e.g. user: "1000:1000"')
    expect(rootRefusal('app:1', '', '0:0')).toContain('which runs as user "0:0": id 0 is root;')
  })

  it('passes a numeric id other than 0, the document\'s before the image\'s', () => {
    expect(rootRefusal('nginx:alpine', '101:101', '')).toBe('')
    expect(rootRefusal('app:nonroot', '', '65532:65532')).toBe('')
    expect(rootRefusal('app:1', '1000', 'root')).toBe('')
    expect(rootUser('1000:0')).toBe('')
  })
})

describe('the document of what runs carries the block', () => {
  const security: SpecSecurity = { read_only: true, tmpfs: [{ path: '/tmp', size_bytes: 64 * MB }, { path: '/var/cache/api', size_bytes: 200 * MB }], capabilities: ['CHOWN', 'SETUID'], non_root: true }
  const spec = { name: 'my-api', image: 'nginx:1', replicas: 1, user: '1000:1000', resources: {}, restart: { policy: 'always' }, deploy: { strategy: 'rolling' } }

  it('as the agent writes it, and reads back as the same block', () => {
    const { document } = documentOf({ ...spec, security })
    expect(document).toContain('security:\n  read_only: true\n  tmpfs:\n    - /tmp\n    - path: /var/cache/api\n      size: 200mb\n  capabilities: [CHOWN, SETUID]\n  non_root: true\n')
    expect(parseSecurity(parseYaml(document).security, { user: '1000:1000' })).toEqual({ security, problems: [] })
  })

  it('writes none for no capabilities and no line for the default set', () => {
    expect(securityLines({ capabilities: [] }, String)).toEqual(['security:', '  capabilities: none'])
    expect(securityLines({ read_only: true }, String)).toEqual(['security:', '  read_only: true'])
    expect(documentOf(spec).document).not.toContain('security')
  })
})
