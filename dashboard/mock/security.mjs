// The `security` block of a deploy.yaml, read and refused as the agent does
// (pkg/spec/security.go), and what `security.non_root` says about an image
// (agent/internal/docker/runtime_security.go). Apart from the mock agent so
// that the rules are unit-tested.
//
//   parseSecurity({ read_only: true, tmpfs: ['/tmp'], capabilities: 'none' }, {})
//     → { security: { read_only: true, tmpfs: [{ path: '/tmp', size_bytes: 67108864 }], capabilities: [] }, problems: [] }
//
// `capabilities` has three states and they are kept apart: the key left out
// keeps Docker's default set (no `capabilities` in the answer), `none` keeps
// nothing (`[]`), a list keeps those.

/** What Docker gives a container that asks for nothing, and the only names the block accepts. */
export const DEFAULT_CAPABILITIES = [
  'AUDIT_WRITE', 'CHOWN', 'DAC_OVERRIDE', 'FOWNER', 'FSETID', 'KILL', 'MKNOD',
  'NET_BIND_SERVICE', 'NET_RAW', 'SETFCAP', 'SETGID', 'SETPCAP', 'SETUID', 'SYS_CHROOT',
]

export const DEFAULT_TMPFS_BYTES = 64 * 1024 ** 2
const MIN_TMPFS_BYTES = 1024 ** 2
const MAX_TMPFS_BYTES = 1024 ** 3
const MAX_TMPFS = 10

const KEYS = ['read_only', 'tmpfs', 'capabilities', 'non_root']
const KEYS_EXPECTED = KEYS.join(', ')
const SIZE_EXPECTED = '16mb, 64mb, 256mb, ... (1mb to 1gb)'
const STATIC_EXCLUSIVE = 'does not apply to a static application: the proxy serves the files, there is no container'

/** "64mb", "1gb" as bytes; null for anything else. */
function sizeOf(text) {
  const m = /^(\d+(?:\.\d+)?)\s*(b|kb|mb|gb)$/i.exec(String(text).trim())
  return m ? Math.round(Number(m[1]) * { b: 1, kb: 1024, mb: 1024 ** 2, gb: 1024 ** 3 }[m[2].toLowerCase()]) : null
}

/** Whether something can be mounted at a path inside a container: absolute, written the one way there is, and not the root. */
function validMountPath(path) {
  if (!path.startsWith('/') || path === '/' || /[\0\r\n]/.test(path)) return false
  return !path.endsWith('/') && !path.includes('//') && !path.split('/').some(part => part === '.' || part === '..')
}

/**
 * Why a container started as `user` cannot be held to run as someone other
 * than root; "" when it can, which takes a numeric id other than 0. A name is
 * not enough: which id it stands for is in the image's /etc/passwd.
 */
export function rootUser(user) {
  const name = String(user ?? '').trim().split(':')[0]
  if (name === '') return 'no user is named, and a container without one runs as root'
  if (!/^\d+$/.test(name)) return name === 'root' ? 'it is root' : 'it is a name, and which id a name stands for is in the image\'s /etc/passwd, which is not read'
  return Number(name) === 0 ? 'id 0 is root' : ''
}

/**
 * The block of a document as a stored spec's `security`, and what is wrong
 * with it as INVALID_CONFIG fields. `security` is undefined for no block and
 * for one that asks for nothing.
 *
 * context: `volumes` of the document, its `user`, and whether it is static.
 */
export function parseSecurity(block, { volumes = [], user = '', isStatic = false } = {}) {
  const problems = []
  const problem = (field, message, expected) => problems.push({ field, message, ...(expected ? { expected } : {}) })
  if (block === undefined || block === null) return { security: undefined, problems }
  if (typeof block !== 'object' || Array.isArray(block)) {
    problem('security', 'must be a block', 'security:\n    read_only: true\n    tmpfs: [/tmp]')
    return { security: undefined, problems }
  }
  const unknown = Object.keys(block).filter(key => !KEYS.includes(key))
  for (const key of unknown) problem('security', `unknown field ${JSON.stringify(key)}`, KEYS_EXPECTED)
  for (const key of ['read_only', 'non_root']) {
    if (block[key] !== undefined && block[key] !== null && typeof block[key] !== 'boolean') problem('security', `${key} must be true or false`, KEYS_EXPECTED)
  }
  if (problems.length > 0) return { security: undefined, problems }
  if (isStatic) {
    problem('security', STATIC_EXCLUSIVE)
    return { security: undefined, problems }
  }

  const security = {}
  if (block.read_only === true) security.read_only = true

  const entries = block.tmpfs === undefined || block.tmpfs === null ? [] : [].concat(block.tmpfs)
  if (entries.length > MAX_TMPFS) problem('security.tmpfs', `too many (${entries.length})`, `at most ${MAX_TMPFS}`)
  else if (entries.length > 0) {
    const mounted = new Map(volumes.map(v => [v?.path, `volume ${v?.name}`]))
    const tmpfs = []
    for (const [i, entry] of entries.entries()) {
      const field = `security.tmpfs[${i}]`
      const form = typeof entry === 'object' && entry !== null ? entry : { path: entry }
      for (const key of Object.keys(form)) if (key !== 'path' && key !== 'size') problem('security', `unknown field ${JSON.stringify(key)}`, KEYS_EXPECTED)
      const written = String(form.path ?? '')
      const path = written.trim()
      if (path === '') problem(`${field}.path`, 'is required', '/tmp')
      else if (!validMountPath(path)) problem(`${field}.path`, `invalid value ${JSON.stringify(written)}`, 'an absolute path inside the container, e.g. /tmp')
      else if (mounted.has(path)) problem(`${field}.path`, `${JSON.stringify(path)} is already mounted: ${mounted.get(path)}`, 'a path nothing else is mounted at')
      mounted.set(path, 'security.tmpfs')

      let size = DEFAULT_TMPFS_BYTES
      if (form.size !== undefined && form.size !== null && form.size !== '') {
        size = sizeOf(form.size)
        if (size === null) problem(`${field}.size`, `invalid value ${JSON.stringify(String(form.size))}`, SIZE_EXPECTED)
        else if (size < MIN_TMPFS_BYTES || size > MAX_TMPFS_BYTES) problem(`${field}.size`, `invalid value ${JSON.stringify(String(form.size))}: out of range`, SIZE_EXPECTED)
      }
      tmpfs.push({ path, size_bytes: size })
    }
    security.tmpfs = tmpfs
  }

  // A key that was written, whatever it holds, is not the key left out.
  if (Object.hasOwn(block, 'capabilities')) {
    const written = block.capabilities
    const expected = `none, or a list of the ones to keep out of ${DEFAULT_CAPABILITIES.join(', ')}`
    const kept = []
    if (!Array.isArray(written)) {
      if (String(written ?? '').trim() !== 'none') problem('security.capabilities', `invalid value ${JSON.stringify(String(written ?? ''))}`, expected)
    }
    else if (written.length === 0) problem('security.capabilities', 'must not be empty; write none to keep no capability, or omit it to keep Docker\'s default set', expected)
    else {
      for (const [i, entry] of written.entries()) {
        const name = String(entry ?? '').trim().toUpperCase().replace(/^CAP_/, '')
        if (!DEFAULT_CAPABILITIES.includes(name)) problem(`security.capabilities[${i}]`, `invalid value ${JSON.stringify(String(entry ?? ''))}: not in Docker's default set, and nothing is added to it`, expected)
        else if (kept.includes(name)) problem(`security.capabilities[${i}]`, `${JSON.stringify(name)} is listed twice`, 'each capability once')
        else kept.push(name)
      }
    }
    security.capabilities = kept.sort()
  }

  if (block.non_root === true) {
    security.non_root = true
    // What the document itself says is known now; what the image says, once the image is on the server.
    const why = rootUser(user)
    if (String(user ?? '') !== '' && why !== '') problem('user', `invalid value ${JSON.stringify(String(user))} next to security.non_root: ${why}`, 'a numeric id other than 0, with or without a group: 1000, 1000:1000')
  }

  return { security: Object.keys(security).length > 0 ? security : undefined, problems }
}

/**
 * Why a deployment under `non_root` fails once its image is on the server:
 * the agent's sentence, or "" when the container runs as someone other than
 * root. `user` is the document's, `imageUser` the one the image names.
 */
export function rootRefusal(image, user, imageUser) {
  const remedy = 'set user in deploy.yaml to a numeric id the image can run as, e.g. user: "1000:1000"'
  const own = String(user ?? '')
  const effective = own !== '' ? own : String(imageUser ?? '')
  const why = rootUser(effective)
  if (why === '') return ''
  if (own !== '') return `security.non_root refuses user ${JSON.stringify(own)}: ${why}; ${remedy}`
  if (effective === '') return `security.non_root refuses image ${image}: it names no user, and a container without one runs as root; ${remedy}, or build the image with a USER instruction`
  return `security.non_root refuses image ${image}, which runs as user ${JSON.stringify(effective)}: ${why}; ${remedy}`
}

/** A size in the largest unit that says it exactly, as the agent writes one back. */
function sizeText(bytes) {
  for (const [name, size] of [['gb', 1024 ** 3], ['mb', 1024 ** 2], ['kb', 1024]]) {
    if (bytes % size === 0) return `${bytes / size}${name}`
  }
  return String(bytes)
}

/** The block as the agent writes it into the document of what runs; `str` quotes a string where YAML needs it. */
export function securityLines(security, str) {
  if (!security) return []
  const lines = ['security:']
  if (security.read_only) lines.push('  read_only: true')
  if (security.tmpfs?.length) {
    lines.push('  tmpfs:')
    for (const t of security.tmpfs) {
      if (t.size_bytes === DEFAULT_TMPFS_BYTES) lines.push(`    - ${str(t.path)}`)
      else lines.push(`    - path: ${str(t.path)}`, `      size: ${sizeText(t.size_bytes)}`)
    }
  }
  if (Array.isArray(security.capabilities)) lines.push(`  capabilities: ${security.capabilities.length === 0 ? 'none' : `[${security.capabilities.join(', ')}]`}`)
  if (security.non_root) lines.push('  non_root: true')
  return lines
}
