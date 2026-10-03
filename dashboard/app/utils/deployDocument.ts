/**
 * What the dashboard needs to know of a pasted deploy.yaml before it sends
 * it: the application's name, which is part of the address it is sent to, and
 * whether it describes something only the CLI can deploy. Pure, so it is
 * unit-tested. This is not a YAML parser and judges nothing else: the agent
 * validates the document, and its answer is what the page shows.
 */

export interface DocumentInspection {
  /** The top-level `name`; "" when the document has none that can be read. */
  name: string
  /**
   * image: runs an image from a registry, which the agent pulls.
   * build: built from the project where `shipwick deploy` runs.
   * static: a folder of files, uploaded by `shipwick deploy`.
   */
  kind: 'image' | 'build' | 'static'
  /** Why the document cannot be sent as it is; "" when it can. */
  problem: string
}

const APPLICATION_NAME = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/

/** The agent refuses a larger document. */
export const MAX_DOCUMENT_BYTES = 64 * 1024

export function inspectDocument(text: string): DocumentInspection {
  const keys = text.trimStart().startsWith('{') ? jsonKeys(text) : yamlKeys(text)
  const name = keys.get('name') ?? ''
  const kind = keys.has('static') ? 'static' : keys.has('build') ? 'build' : 'image'

  let problem = ''
  if (text.trim() === '') problem = ''
  else if (new TextEncoder().encode(text).length > MAX_DOCUMENT_BYTES) problem = 'The document is larger than 64 KB, which is more than the agent reads.'
  else if (name === '') problem = 'The document has no name. Its first line is usually name: my-api.'
  else if (!APPLICATION_NAME.test(name)) problem = `"${name}" cannot be an application's name: lowercase letters, digits and dashes, e.g. my-api.`
  return { name, kind, problem }
}

/** The top-level keys of a YAML mapping written in block style, with the value of those that are plain scalars. */
function yamlKeys(text: string): Map<string, string> {
  const keys = new Map<string, string>()
  for (const line of text.split(/\r?\n/)) {
    // Top level only: a key at the start of a line. Indented lines belong to a block above.
    const match = /^(?:"([^"]+)"|'([^']+)'|([A-Za-z_][\w-]*))\s*:(?:\s+(.*))?$/.exec(line)
    if (!match) continue
    const key = match[1] ?? match[2] ?? match[3]!
    if (!keys.has(key)) keys.set(key, scalar(match[4] ?? ''))
  }
  return keys
}

/** A scalar as written after a key: quotes and a trailing comment removed. */
function scalar(raw: string): string {
  const value = raw.trim()
  const quoted = /^"((?:[^"\\]|\\.)*)"|^'((?:[^']|'')*)'/.exec(value)
  if (quoted) return quoted[1] ?? quoted[2] ?? ''
  return value.replace(/\s+#.*$/, '').trim()
}

function jsonKeys(text: string): Map<string, string> {
  const keys = new Map<string, string>()
  try {
    const value = JSON.parse(text) as unknown
    if (typeof value !== 'object' || value === null || Array.isArray(value)) return keys
    for (const [key, v] of Object.entries(value)) {
      if (v === null || v === undefined || v === '') continue
      keys.set(key, typeof v === 'string' ? v : '')
    }
  }
  catch {
    // Not JSON after all: the agent says what is wrong with it.
  }
  return keys
}

/** A document to start from: an image from a registry behind a domain, which is all this page deploys. */
export const EXAMPLE_DOCUMENT = `name: my-api
image: ghcr.io/company/my-api:1.0.0
port: 8080
domain: api.example.com
replicas: 2
env:
  LOG_LEVEL: info
  DATABASE_URL: postgres://app:\${DATABASE_PASSWORD}@postgres:5432/app
health:
  path: /health
resources:
  cpu: 1
  memory: 512mb
`

/** Why the CLI is needed, and the command, for a document this page cannot deploy; "" for one it can. */
export function cliOnlyReason(kind: DocumentInspection['kind']): string {
  if (kind === 'build') return 'This application is built from its project: the image is built where shipwick deploy runs and sent to the server. The dashboard has no project to build from.'
  if (kind === 'static') return 'This application is a folder of files, which shipwick deploy uploads from the project. The dashboard has no folder to upload.'
  return ''
}
