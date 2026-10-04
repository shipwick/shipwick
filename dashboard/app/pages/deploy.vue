<script setup lang="ts">
import type { ApplicationConfig, Deployment, Validation } from '~/types/api'
import { AgentError, toAgentError } from '~/utils/agentError'
import { EXAMPLE_DOCUMENT, cliOnlyReason, describesStatic, inspectDocument, maskedLabel, maskedSecretName, maskedVariable, remainingMasks } from '~/utils/deployDocument'
import { pluralize } from '~/utils/format'
import { missingSecrets } from '~/utils/secrets'
import { wantedServer } from '~/utils/servers'

/**
 * A deploy.yaml edited and deployed: a new application, or the configuration
 * of one that runs, which the agent hands out to change. The agent is asked
 * to check the document first and nothing is deployed until it is in order.
 * The document goes to the agent as it is: the agent reads the application's
 * name out of it, and what it answers is what the page shows.
 */
const route = useRoute()
const agent = useAgent()
const access = useAccess()

/** `?application=<name>`: the editor was opened from that application, to change its configuration. */
const target = computed(() => wantedServer(route.query.application) ?? '')

useHead({ title: () => (target.value ? `Change the configuration of ${target.value}` : 'New application') })

const mayDeployAny = computed(() => access.can('deploy'))

const text = ref('')

// --- the configuration that runs --------------------------------------------------

/** What the agent handed out for the application the page was opened for. */
const config = shallowRef<ApplicationConfig | null>(null)
const configLoading = ref(false)
const configError = shallowRef<AgentError | null>(null)
/**
 * The agent is older than 0.7: it hands out no document, and takes one only
 * under its application's name. Found out by asking, and remembered.
 */
const legacy = ref(false)

async function loadConfig() {
  config.value = null
  configError.value = null
  if (target.value === '' || !mayDeployAny.value) return
  configLoading.value = true
  try {
    // Never with `escape`: that form is for a file the CLI reads.
    const answer = await agent.get<ApplicationConfig>(`/applications/${encodeURIComponent(target.value)}/config`)
    config.value = answer
    // What somebody typed before the answer arrived is theirs.
    if (text.value.trim() === '') text.value = answer.document
  }
  catch (cause) {
    const error = toAgentError(cause)
    if (error.code === 'ENDPOINT_NOT_FOUND') legacy.value = true
    else configError.value = error
  }
  finally {
    configLoading.value = false
  }
}
onMounted(loadConfig)
watch([target, mayDeployAny], loadConfig)

const edited = computed(() => config.value !== null && text.value !== config.value.document)

function startOver() {
  if (config.value) text.value = config.value.document
}

/** The values the agent did not hand out and the document still shows as the mask. */
const masks = computed(() => (config.value ? config.value.masked.map(field => ({ field, label: maskedLabel(field), variable: maskedVariable(field), secret: maskedSecretName(field) })) : []))
const stillMasked = computed(() => new Set(config.value ? remainingMasks(text.value, config.value.masked) : []))

// --- what an agent before 0.7 needs read out of the document -------------------------

const inspection = computed(() => inspectDocument(text.value))
const legacyProblem = computed(() => (legacy.value && text.value.trim() !== '' ? inspection.value.problem || cliOnlyReason(inspection.value.kind) : ''))

// --- check and deploy -------------------------------------------------------------

const ready = computed(() => text.value.trim() !== '' && mayDeployAny.value && legacyProblem.value === '')
const deployable = computed(() => ready.value && stillMasked.value.size === 0)

const working = ref<'check' | 'deploy' | null>(null)
const error = shallowRef<AgentError | null>(null)
/** The document the agent last found in order, to say so until it is edited. */
const checked = ref('')

watch(text, () => {
  error.value = null
})

const body = computed(() => ({ content: text.value, type: 'application/yaml' }))
/** A folder of files is deployed again by the digest of what was uploaded: the dashboard has no folder to upload. */
const staticDigest = computed(() => (config.value?.static_digest && describesStatic(text.value) ? config.value.static_digest : undefined))

/** Sends the document as it is; to an agent that has no such endpoint, under the name read out of it. */
async function send<T>(nameless: string, named: string, query?: Record<string, string | undefined>): Promise<T> {
  if (!legacy.value) {
    try {
      return await agent.post<T>(nameless, { text: body.value, query })
    }
    catch (cause) {
      if (!(cause instanceof AgentError) || cause.code !== 'ENDPOINT_NOT_FOUND') throw cause
      legacy.value = true
    }
  }
  if (legacyProblem.value) throw new AgentError(400, 'INVALID_REQUEST', legacyProblem.value)
  return await agent.post<T>(`/applications/${encodeURIComponent(inspection.value.name)}/${named}`, { text: body.value, query })
}

async function validate() {
  await send<Validation>('/validate', 'validate')
  checked.value = text.value
}

async function check() {
  if (working.value || !ready.value) return
  working.value = 'check'
  error.value = null
  try {
    await validate()
  }
  catch (cause) {
    error.value = toAgentError(cause)
  }
  finally {
    working.value = null
  }
}

async function deploy() {
  if (working.value || !deployable.value) return
  working.value = 'deploy'
  error.value = null
  try {
    // Asked first, so that every problem is listed at once and nothing is recorded for a document that would be refused.
    await validate()
    const deployment = await send<Deployment>('/applications', 'deploy', { static: staticDigest.value })
    // The application's page picks the deployment up and narrates it.
    await navigateTo(`/applications/${encodeURIComponent(deployment.application)}`)
  }
  catch (cause) {
    error.value = toAgentError(cause)
  }
  finally {
    working.value = null
  }
}

function useExample() {
  text.value = EXAMPLE_DOCUMENT
}

const fields = computed(() => error.value?.fields ?? [])
const secrets = computed(() => (error.value?.code === 'INVALID_CONFIG' ? missingSecrets(fields.value) : []))

const crumbs = computed(() => (target.value
  ? [{ label: 'Applications', to: '/applications' }, { label: target.value, to: `/applications/${target.value}`, mono: true }, { label: 'Change the configuration' }]
  : [{ label: 'Applications', to: '/applications' }, { label: 'New application' }]))
</script>

<template>
  <div>
    <PageHeader :crumbs="crumbs" />
    <PageBody>
      <div v-if="!mayDeployAny" class="rounded-sm border border-line">
        <EmptyState title="Deploying needs the deploy or admin role">
          You are signed in with a read token: you can see everything and change nothing. Ask an admin for a token with the deploy role.
          <template v-if="target">
            What {{ target }} runs with is on its <NuxtLink :to="`/applications/${target}/configuration`" class="link">Configuration</NuxtLink> tab.
          </template>
        </EmptyState>
      </div>

      <div v-else class="grid gap-8 lg:grid-cols-[minmax(0,1fr)_19rem]">
        <form class="min-w-0 space-y-4" @submit.prevent="deploy">
          <div>
            <h2 class="text-lg font-semibold">
              {{ target ? `Change the configuration of ${target}` : 'Deploy an application from its deploy.yaml' }}
            </h2>
            <p class="mt-1 max-w-prose text-fg-muted">
              <template v-if="target && config">
                This is the deploy.yaml <span class="mono text-fg">{{ target }}</span> runs with (deployment #{{ config.sequence }}, <span class="mono text-fg">{{ config.version }}</span>). Change what you want and deploy it: the running version keeps serving until the new one is healthy.
              </template>
              <template v-else-if="target && legacy">
                Paste the deploy.yaml of <span class="mono text-fg">{{ target }}</span> from its project, with your change. This server's agent is older than 0.7 and cannot hand out the configuration it runs with.
              </template>
              <template v-else-if="target">
                The deploy.yaml of <span class="mono text-fg">{{ target }}</span>, to change and deploy again.
              </template>
              <template v-else>
                Paste the application's deploy.yaml. The agent checks it first, and nothing is deployed until it is in order. The application is the one the document names: a name that exists gets this configuration, a new name is created.
              </template>
            </p>
          </div>

          <InlineError v-if="configError" :error="configError" />
          <p v-if="configError" class="text-xs text-fg-muted">
            The configuration could not be fetched. A deploy.yaml pasted below is deployed all the same.
          </p>

          <!-- What the agent did not hand out: each value has to be written again, or become a secret. -->
          <div v-if="masks.length > 0" class="overflow-hidden rounded-sm border border-warn-line">
            <div class="bg-warn-bg px-3 py-2 text-xs text-warn">
              <p class="font-medium">
                {{ masks.length === 1 ? 'One value is' : `${masks.length} values are` }} shown as <span class="mono">"********"</span> and must be replaced before this can be deployed
              </p>
              <p class="mt-0.5">
                These values were set when the application was deployed and are not handed out. Write each again in the document, or store it as a secret and write <span class="mono">${NAME}</span> in its place.
              </p>
            </div>
            <ul class="divide-y divide-line" aria-label="Values to replace">
              <li v-for="mask in masks" :key="mask.field" class="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2">
                <span class="min-w-0 flex-1 basis-48 break-words" :class="mask.variable ? 'mono font-medium' : ''">{{ mask.label }}</span>
                <span v-if="stillMasked.has(mask.field)" class="text-xs text-warn">still "********"</span>
                <span v-else class="inline-flex items-center gap-1 text-xs text-ok"><UiIcon name="check" :size="12" />replaced</span>
                <NuxtLink
                  :to="{ path: '/settings/secrets', query: { name: mask.secret } }"
                  target="_blank"
                  class="link text-xs text-fg-muted"
                  :aria-label="`Store as a secret: ${mask.label} (opens in a new tab)`"
                >Store as a secret</NuxtLink>
                <span class="mono text-xs text-fg-subtle" :title="`What to write in the document once the secret ${mask.secret} is stored`">{{ '${' + mask.secret + '}' }}</span>
              </li>
            </ul>
          </div>

          <div>
            <div class="flex items-end justify-between gap-3">
              <label for="deploy-document" class="label">deploy.yaml</label>
              <span class="flex flex-wrap items-center justify-end gap-x-3 gap-y-1 text-xs">
                <button v-if="edited" type="button" class="link text-fg-muted" @click="startOver">
                  Start over from what runs
                </button>
                <button v-if="!target && text.trim() === ''" type="button" class="link text-fg-muted" @click="useExample">
                  Start from an example
                </button>
                <a href="https://shipwick.com/docs/reference/deploy-yaml" target="_blank" rel="noopener noreferrer" class="link inline-flex items-center gap-1 text-fg-muted">Every key<UiIcon name="external" :size="12" /><span class="sr-only">(opens in a new tab)</span></a>
              </span>
            </div>
            <div v-if="configLoading" class="mt-1.5 space-y-2 rounded-sm border border-line px-3 py-3" role="progressbar" aria-busy="true" aria-label="Loading the configuration">
              <span class="skeleton w-40" /><span class="skeleton w-64" /><span class="skeleton w-52" />
            </div>
            <textarea
              v-else
              id="deploy-document"
              v-model="text"
              class="input mono mt-1.5 !h-auto min-h-[22rem] w-full resize-y whitespace-pre !py-2.5 leading-5"
              rows="20"
              spellcheck="false"
              autocomplete="off"
              autocapitalize="off"
              autocorrect="off"
              wrap="off"
              :placeholder="'name: my-api\nimage: ghcr.io/company/my-api:1.0.0\nport: 8080\ndomain: api.example.com'"
              :aria-invalid="fields.length > 0 || legacyProblem !== '' || undefined"
              aria-describedby="deploy-document-status"
            />
          </div>

          <!-- What keeps the document from being sent, said before the agent is asked. -->
          <div id="deploy-document-status" class="space-y-2 text-xs">
            <p v-if="legacyProblem" class="flex items-start gap-2 text-danger">
              <UiIcon name="alert" :size="14" class="mt-px" />
              <span class="min-w-0">{{ legacyProblem }}<template v-if="!inspection.problem"> Run <span class="mono">shipwick deploy</span> from the project.</template></span>
            </p>
            <p v-else-if="legacy && !configLoading" class="text-fg-muted">
              This server's agent is older than 0.7: the document is sent under the name on its <span class="mono text-fg">name:</span> line<template v-if="inspection.name">, <span class="mono text-fg">{{ inspection.name }}</span></template>.
            </p>
          </div>

          <!-- The agent's verdict: every field it refused, each with what it expects. -->
          <div v-if="error && fields.length > 0" class="overflow-hidden rounded-sm border border-danger-line" role="alert">
            <p class="bg-danger-bg px-3 py-2 text-xs font-medium text-danger">
              The agent refused the document: {{ fields.length === 1 ? 'one thing is' : `${fields.length} things are` }} not in order. Nothing was deployed.
            </p>
            <ul class="divide-y divide-line">
              <li v-for="(field, index) in fields" :key="index" class="grid gap-x-4 gap-y-0.5 px-3 py-2 sm:grid-cols-[minmax(0,12rem)_minmax(0,1fr)]">
                <span class="mono break-all font-medium">{{ field.field }}</span>
                <span class="min-w-0 break-words">
                  {{ field.message }}
                  <span v-if="field.expected" class="block text-xs text-fg-muted">Expected: <span class="mono">{{ field.expected }}</span></span>
                </span>
              </li>
            </ul>
            <p v-if="secrets.length > 0" class="border-t border-line px-3 py-2 text-xs text-fg-muted">
              Store
              <template v-for="(name, index) in secrets" :key="name">
                <template v-if="index > 0">
                  {{ index === secrets.length - 1 ? ' and ' : ', ' }}
                </template>
                <NuxtLink :to="{ path: '/settings/secrets', query: { name } }" target="_blank" class="mono link font-medium">{{ name }}</NuxtLink>
              </template>
              on the Secrets page (it opens in a new tab, so this document stays), then deploy again. An admin token can add {{ secrets.length === 1 ? 'it' : 'them' }}.
            </p>
          </div>
          <InlineError v-else-if="error" :error="error" />
          <p v-else-if="checked !== '' && checked === text" class="flex items-center gap-2 text-xs font-medium text-ok" role="status">
            <UiIcon name="check" :size="14" />
            The agent finds the document in order. Nothing has been deployed yet.
          </p>

          <div class="flex flex-wrap items-center gap-2">
            <UiButton
              type="submit"
              variant="primary"
              :pending="working === 'deploy'"
              :disabled="!deployable || working !== null"
              :title="ready && stillMasked.size > 0 ? `Replace the ${pluralize(stillMasked.size, 'value')} shown as ******** first` : undefined"
            >
              Deploy
            </UiButton>
            <UiButton :pending="working === 'check'" :disabled="!ready || working !== null" @click="check">
              Check only
            </UiButton>
            <UiButton variant="ghost" :to="target ? `/applications/${target}/configuration` : '/applications'">
              Cancel
            </UiButton>
          </div>
        </form>

        <aside aria-label="About deploying a document" class="min-w-0 space-y-4 text-xs text-fg-muted lg:pt-1">
          <div>
            <p class="label mb-1">
              What happens
            </p>
            <ol class="list-decimal space-y-1 pl-4">
              <li>The agent checks the document: every key, the hostnames and ports others hold, the secrets it refers to.</li>
              <li>It pulls the image, starts the replicas and waits for them to be healthy.</li>
              <li>You are taken to the application's page, which follows the deployment to its end.</li>
            </ol>
          </div>
          <div>
            <p class="label mb-1">
              Values that are secret
            </p>
            <p>
              Write <span class="mono text-fg">${NAME}</span> in an env value and store the value under <NuxtLink to="/settings/secrets" class="link">Secrets</NuxtLink>: the document then says the reference again whenever it is opened here. A value written into the document itself is stored too, encrypted, and never shown again.
            </p>
          </div>
          <div>
            <p class="label mb-1">
              From a project
            </p>
            <p>
              An application that is built from source (<span class="mono text-fg">build:</span>) or is a folder of files (<span class="mono text-fg">static:</span>) is first deployed with <span class="mono text-fg">shipwick deploy</span> in the project's directory: the CLI builds or uploads. Its configuration can be changed here afterwards; another image or other files come from the project again.
            </p>
          </div>
        </aside>
      </div>
    </PageBody>
  </div>
</template>
