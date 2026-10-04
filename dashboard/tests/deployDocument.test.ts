import { describe, expect, it } from 'vitest'
import { cliOnlyReason, describesStatic, inspectDocument, maskedLabel, maskedSecretName, maskedVariable, remainingMasks, stillMasked } from '../app/utils/deployDocument'

// What GET /applications/:name/config hands out for an application with one reference and three literals.
const DOCUMENT = `# A value shown as "********" was given when the application was deployed and
# is not handed out.

name: my-api

image: ghcr.io/acme/my-api:1.4.2

env:
  DATABASE_URL: postgres://api:\${POSTGRES_PASSWORD}@postgres:5432/api
  LOG_LEVEL: "********" # not handed out: write the value again, or refer to a secret as \${NAME}
  REDIS_URL: "********" # not handed out: write the value again, or refer to a secret as \${NAME}

proxy:
  basic_auth:
    - path: /admin
      username: ops
      password: "********" # not handed out: write the value again, or refer to a secret as \${NAME}
    - username: audit
      password: \${AUDIT_PASSWORD}
`

const MASKED = ['env.LOG_LEVEL', 'env.REDIS_URL', 'proxy.basic_auth[0].password']

describe('the values an agent did not hand out', () => {
  it('names a variable by its name and a password by where it is', () => {
    expect(maskedVariable('env.LOG_LEVEL')).toBe('LOG_LEVEL')
    expect(maskedVariable('proxy.basic_auth[0].password')).toBe('')
    expect(maskedLabel('env.LOG_LEVEL')).toBe('LOG_LEVEL')
    expect(maskedLabel('proxy.basic_auth[1].password')).toBe('the password of account 2 under proxy.basic_auth')
    expect(maskedLabel('something.else')).toBe('something.else')
  })

  it('proposes the name to store each under as a secret', () => {
    expect(maskedSecretName('env.LOG_LEVEL')).toBe('LOG_LEVEL')
    expect(maskedSecretName('proxy.basic_auth[0].password')).toBe('PROXY_PASSWORD')
    expect(maskedSecretName('proxy.basic_auth[2].password')).toBe('PROXY_PASSWORD_3')
    expect(maskedSecretName('something.else')).toBe('')
  })

  it('finds every mask in the document as it was handed out', () => {
    expect(remainingMasks(DOCUMENT, MASKED)).toEqual(MASKED)
  })

  it('sees a variable that was written again, or turned into a reference', () => {
    const edited = DOCUMENT
      .replace('LOG_LEVEL: "********" # not handed out: write the value again, or refer to a secret as ${NAME}', 'LOG_LEVEL: info')
      .replace('REDIS_URL: "********"', 'REDIS_URL: ${REDIS_URL}')
    expect(stillMasked(edited, 'env.LOG_LEVEL')).toBe(false)
    expect(stillMasked(edited, 'env.REDIS_URL')).toBe(false)
    expect(remainingMasks(edited, MASKED)).toEqual(['proxy.basic_auth[0].password'])
  })

  it('tells the accounts apart by their position', () => {
    expect(stillMasked(DOCUMENT, 'proxy.basic_auth[0].password')).toBe(true)
    expect(stillMasked(DOCUMENT, 'proxy.basic_auth[1].password')).toBe(false)
    expect(stillMasked(DOCUMENT.replace('password: "********"', 'password: ${PROXY_PASSWORD}'), 'proxy.basic_auth[0].password')).toBe(false)
  })

  it('is not fooled by a variable whose name ends in another one', () => {
    const text = 'env:\n  APP_LOG_LEVEL: "********"\n  LOG_LEVEL: info\n'
    expect(stillMasked(text, 'env.LOG_LEVEL')).toBe(false)
    expect(stillMasked(text, 'env.APP_LOG_LEVEL')).toBe(true)
  })

  it('reads a document written as JSON as well', () => {
    expect(stillMasked('{"name": "x", "env": {"LOG_LEVEL": "********", "PORT": "80"}}', 'env.LOG_LEVEL')).toBe(true)
    expect(stillMasked('{"name": "x", "env": {"LOG_LEVEL": "info"}}', 'env.LOG_LEVEL')).toBe(false)
  })

  it('says nothing is masked once the variable is gone from the document', () => {
    expect(remainingMasks('name: my-api\nimage: nginx\n', MASKED)).toEqual([])
  })
})

describe('describesStatic', () => {
  it('is true for a document whose top level names a folder', () => {
    expect(describesStatic('name: landing\n\nstatic:\n  dir: dist\n  fallback: index.html\n')).toBe(true)
    expect(describesStatic('name: landing\nstatic: dist\n')).toBe(true)
    expect(describesStatic('{"name": "landing", "static": "dist"}')).toBe(true)
  })

  it('is false for a container, whatever its values say', () => {
    expect(describesStatic('name: web\nimage: nginx\nenv:\n  static: yes\n')).toBe(false)
    expect(describesStatic('name: web\nimage: ghcr.io/acme/static:1\n')).toBe(false)
  })
})

describe('inspectDocument, for an agent before 0.7', () => {
  it('reads the name the address needs, quoted or not, with a comment after it', () => {
    expect(inspectDocument('name: my-api\nimage: nginx\n').name).toBe('my-api')
    expect(inspectDocument('name: "my-api" # the API\nimage: nginx\n').name).toBe('my-api')
    expect(inspectDocument('{"name": "my-api", "image": "nginx"}').name).toBe('my-api')
  })

  it('reads it below the comment the agent opens a handed-out document with', () => {
    expect(inspectDocument(DOCUMENT)).toEqual({ name: 'my-api', kind: 'image', problem: '' })
  })

  it('says why a document cannot be sent under a name', () => {
    expect(inspectDocument('image: nginx\n').problem).toContain('has no name')
    expect(inspectDocument('name: My_API\nimage: nginx\n').problem).toContain('cannot be an application\'s name')
    expect(inspectDocument('').problem).toBe('')
  })

  it('tells what only the CLI can deploy there', () => {
    expect(inspectDocument('name: shop\nbuild: .\n').kind).toBe('build')
    expect(inspectDocument('name: landing\nstatic: dist\n').kind).toBe('static')
    expect(cliOnlyReason('build')).toContain('built from its project')
    expect(cliOnlyReason('static')).toContain('folder of files')
    expect(cliOnlyReason('image')).toBe('')
  })
})
