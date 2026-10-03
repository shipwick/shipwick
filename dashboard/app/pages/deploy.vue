<script setup lang="ts">
import type { Application, Deployment, Validation } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { toAgentError } from '~/utils/agentError'
import { deployHint } from '~/utils/access'
import { EXAMPLE_DOCUMENT, cliOnlyReason, inspectDocument } from '~/utils/deployDocument'
import { missingSecrets } from '~/utils/secrets'
import { wantedServer } from '~/utils/servers'

/**
 * A deploy.yaml pasted and deployed: a new application, or a changed
 * configuration for one that exists. The agent is asked to check the document
 * first and nothing is deployed until it is in order. Only for what the agent
 * can fetch by itself — an image in a registry; a build or a folder of files
 * comes from the project, with the CLI.
 */
const route = useRoute()
const agent = useAgent()
const access = useAccess()

/** `?application=<name>`: the editor was opened from that application, to change its configuration. */
const target = computed(() => wantedServer(route.query.application) ?? '')

useHead({ title: () => (target.value ? `Deploy ${target.value}` : 'New application') })

const apps = usePolling<Application[]>(signal => agent.get<Application[]>('/applications', { signal }), { interval: 30_000 })

const text = ref('')
const inspection = computed(() => inspectDocument(text.value))
const existing = computed(() => (apps.data.value ?? []).find(a => a.name === inspection.value.name) ?? null)
const cliOnly = computed(() => cliOnlyReason(inspection.value.kind))

/** The document names another application than the page was opened for: said, not refused. */
const otherName = computed(() => target.value !== '' && inspection.value.name !== '' && inspection.value.name !== target.value)

// A token limited to some applications deploys only those, also by a name that does not exist yet.
const mayDeployAny = computed(() => access.can('deploy'))
const refusal = computed(() => (inspection.value.name && !inspection.value.problem ? deployHint(access.token.value, inspection.value.name) ?? '' : ''))

const ready = computed(() => text.value.trim() !== '' && inspection.value.problem === '' && cliOnly.value === '' && refusal.value === '' && mayDeployAny.value)

const working = ref<'check' | 'deploy' | null>(null)
const error = shallowRef<AgentError | null>(null)
/** The document the agent last found in order, to say so until it is edited. */
const checked = ref('')

watch(text, () => {
  error.value = null
})

const path = computed(() => `/applications/${encodeURIComponent(inspection.value.name)}`)
const body = computed(() => ({ content: text.value, type: 'application/yaml' }))

async function validate(): Promise<boolean> {
  await agent.post<Validation>(`${path.value}/validate`, { text: body.value })
  checked.value = text.value
  return true
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
  if (working.value || !ready.value) return
  working.value = 'deploy'
  error.value = null
  try {
    // Asked first, so that every problem is listed at once and nothing is recorded for a document that would be refused.
    await validate()
    await agent.post<Deployment>(`${path.value}/deploy`, { text: body.value })
    // The application's page picks the deployment up and narrates it.
    await navigateTo(path.value)
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
/** An agent that cannot validate without deploying: it is older than this page needs. */
const tooOld = computed(() => error.value?.code === 'ENDPOINT_NOT_FOUND')

const crumbs = computed(() => (target.value
  ? [{ label: 'Applications', to: '/applications' }, { label: target.value, to: `/applications/${target.value}`, mono: true }, { label: 'Deploy a changed configuration' }]
  : [{ label: 'Applications', to: '/applications' }, { label: 'New application' }]))
</script>

<template>
  <div>
    <PageHeader :crumbs="crumbs" />
    <PageBody>
      <div v-if="!mayDeployAny" class="rounded-sm border border-line">
        <EmptyState title="Deploying needs the deploy or admin role">
          You are signed in with a read token: you can see everything and change nothing. Ask an admin for a token with the deploy role.
        </EmptyState>
      </div>

      <div v-else class="grid gap-8 lg:grid-cols-[minmax(0,1fr)_19rem]">
        <form class="min-w-0 space-y-4" @submit.prevent="deploy">
          <div>
            <h2 class="text-lg font-semibold">
              {{ target ? `Deploy a changed configuration of ${target}` : 'Deploy an application from its deploy.yaml' }}
            </h2>
            <p class="mt-1 max-w-prose text-fg-muted">
              <template v-if="target">
                Paste the deploy.yaml of <span class="mono text-fg">{{ target }}</span> from its project, with your change. The dashboard cannot fill it in for you: the agent never hands back the values of an application's environment, so what it shows under Configuration is not a document that could be deployed again.
              </template>
              <template v-else>
                Paste the application's deploy.yaml. The agent checks it first, and nothing is deployed until it is in order.
              </template>
            </p>
          </div>

          <div>
            <div class="flex items-end justify-between gap-3">
              <label for="deploy-document" class="label">deploy.yaml</label>
              <span class="flex items-center gap-3 text-xs">
                <button v-if="text.trim() === ''" type="button" class="link text-fg-muted" @click="useExample">Start from an example</button>
                <a href="https://shipwick.com/docs/reference/deploy-yaml" target="_blank" rel="noopener noreferrer" class="link inline-flex items-center gap-1 text-fg-muted">Every key<UiIcon name="external" :size="12" /></a>
              </span>
            </div>
            <textarea
              id="deploy-document"
              v-model="text"
              class="input mono mt-1.5 !h-auto min-h-[22rem] w-full resize-y whitespace-pre !py-2.5 leading-5"
              rows="18"
              spellcheck="false"
              autocomplete="off"
              autocapitalize="off"
              autocorrect="off"
              wrap="off"
              :placeholder="'name: my-api\nimage: ghcr.io/company/my-api:1.0.0\nport: 8080\ndomain: api.example.com'"
              :aria-invalid="fields.length > 0 || undefined"
              aria-describedby="deploy-document-status"
            />
          </div>

          <!-- What the page reads from the document before the agent is asked. -->
          <div id="deploy-document-status" class="space-y-2 text-xs" aria-live="polite">
            <p v-if="inspection.problem" class="flex items-start gap-2 text-danger">
              <UiIcon name="alert" :size="14" class="mt-px" />
              {{ inspection.problem }}
            </p>
            <template v-else-if="inspection.name">
              <p class="text-fg-muted">
                <template v-if="existing">
                  Replaces the configuration of <NuxtLink :to="`/applications/${existing.name}`" class="mono link">{{ existing.name }}</NuxtLink><template v-if="existing.version">, which runs <span class="mono text-fg">{{ existing.version }}</span></template>. The running version keeps serving until the new replicas are healthy.
                </template>
                <template v-else>
                  Creates a new application named <span class="mono text-fg">{{ inspection.name }}</span>.
                </template>
              </p>
              <p v-if="otherName" class="flex items-start gap-2 text-warn">
                <UiIcon name="alert" :size="14" class="mt-px" />
                This document is for {{ inspection.name }}, not for {{ target }}: deploying it leaves {{ target }} as it is.
              </p>
              <p v-if="refusal" class="flex items-start gap-2 text-danger">
                <UiIcon name="lock" :size="14" class="mt-px" />
                {{ refusal }}, so not an application named {{ inspection.name }}. An admin can widen that under Access.
              </p>
            </template>
          </div>

          <!-- A build or a folder of files comes from the project: said with the command, not attempted. -->
          <div v-if="cliOnly" class="rounded-sm border border-warn-line bg-warn-bg px-3 py-2.5 text-xs text-warn" role="status">
            <p class="font-medium">
              This one is deployed with the CLI, from the project's directory
            </p>
            <p class="mt-0.5">
              {{ cliOnly }}
            </p>
            <code class="command mt-2 inline-block">shipwick deploy</code>
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
          <template v-else-if="error">
            <InlineError :error="error" />
            <p v-if="tooOld" class="text-xs text-fg-muted">
              Deploying a pasted document needs an agent that can check one without deploying it (0.5 or newer). Use <span class="mono text-fg">shipwick deploy</span> from the project.
            </p>
          </template>
          <p v-else-if="checked !== '' && checked === text" class="flex items-center gap-2 text-xs font-medium text-ok" role="status">
            <UiIcon name="check" :size="14" />
            The agent finds the document in order. Nothing has been deployed yet.
          </p>

          <div class="flex flex-wrap items-center gap-2">
            <UiButton type="submit" variant="primary" :pending="working === 'deploy'" :disabled="!ready || working !== null">
              {{ existing ? 'Deploy' : 'Create and deploy' }}
            </UiButton>
            <UiButton :pending="working === 'check'" :disabled="!ready || working !== null" @click="check">
              Check only
            </UiButton>
            <UiButton variant="ghost" :to="target ? `/applications/${target}` : '/applications'">
              Cancel
            </UiButton>
          </div>
        </form>

        <aside class="min-w-0 space-y-4 text-xs text-fg-muted lg:pt-1">
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
              Write <span class="mono text-fg">${NAME}</span> in an env value and store the value under <NuxtLink to="/settings/secrets" class="link">Secrets</NuxtLink>. A value written into the document is stored too, encrypted, and never shown again.
            </p>
          </div>
          <div>
            <p class="label mb-1">
              From a project
            </p>
            <p>
              An application that is built from source (<span class="mono text-fg">build:</span>) or is a folder of files (<span class="mono text-fg">static:</span>) is deployed with <span class="mono text-fg">shipwick deploy</span> in the project's directory: the CLI builds or uploads, the dashboard cannot.
            </p>
          </div>
        </aside>
      </div>
    </PageBody>
  </div>
</template>
