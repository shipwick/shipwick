<script setup lang="ts">
import type { Application, Secret, SetSecretRequest } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { AgentError as Refusal, toAgentError } from '~/utils/agentError'
import { formatAbsoluteUtc } from '~/utils/format'
import { roleHint } from '~/utils/roles'
import { applicationsWithSecrets, replaces, scopeLabel, scopesKnown, secretNameProblem, secretRemovalConsequence, secretScopeProblem, secretValueProblem, sortSecrets, urlCharacterHint } from '~/utils/secrets'
import { wantedServer } from '~/utils/servers'

useHead({ title: 'Secrets' })

const agent = useAgent()
const access = useAccess()

/** Names, scopes and dates only: the API never returns a value, and this page never holds one longer than its request. */
const secrets = usePolling<Secret[]>(signal => agent.get<Secret[]>('/secrets', { signal }), { interval: 30_000 })
/** The applications of the server, for the names the scope choice offers; an application that does not exist yet is typed. */
const applications = usePolling<Application[]>(signal => agent.get<Application[]>('/applications', { signal }), { interval: 60_000 })

const mayAdmin = computed(() => access.can('admin'))
/** By name, the server's before the applications' of the same name. */
const sorted = computed(() => sortSecrets(secrets.data.value ?? []))

/**
 * An agent before 0.9 keeps one list of secrets for the whole server: its
 * rows carry no scope, and it refuses a body that names one. Found out from
 * the rows, or from the refusal, and remembered; the page then offers no scope
 * and shows none.
 */
const legacy = ref(false)
watch(() => secrets.data.value, (rows) => {
  if (rows && scopesKnown(rows) === false) legacy.value = true
})
const scoped = computed(() => !legacy.value)

// --- set ------------------------------------------------------------------------

// A deployment refused for a secret that is not stored links here with its
// name, and with the application it is for.
const route = useRoute()
const name = ref(typeof route.query.name === 'string' ? route.query.name : '')
const value = ref('')
const setting = ref(false)
const setError = shallowRef<AgentError | null>(null)
/** What was just stored, for the confirmation line; gone with the next edit. */
const stored = ref<{ name: string, application: string } | null>(null)

/** The choice "another application", whose name is typed: no application is called this. */
const OTHER = '+'
const scopeChoice = ref(wantedScope(wantedServer(route.query.application)))
const otherApplication = ref(scopeChoice.value === OTHER ? wantedServer(route.query.application) ?? '' : '')

/** The scope the address asked for: the server's, a name to offer, or a name to type when it is not one. */
function wantedScope(asked: string | null): string {
  if (!asked) return ''
  return secretScopeProblem(asked) === '' ? asked : OTHER
}

/** The names offered: the server's applications, those with secrets of their own, and the one the address asked for. */
const knownApplications = computed(() => {
  const names = new Set<string>((applications.data.value ?? []).map(a => a.name))
  for (const a of applicationsWithSecrets(secrets.data.value ?? [])) names.add(a)
  if (scopeChoice.value !== '' && scopeChoice.value !== OTHER) names.add(scopeChoice.value)
  return [...names].sort()
})

const trimmedName = computed(() => name.value.trim())
const nameProblem = computed(() => secretNameProblem(trimmedName.value))
/** The application the secret is for; "" for the server's. */
const application = computed(() => (!scoped.value ? '' : scopeChoice.value === OTHER ? otherApplication.value.trim() : scopeChoice.value))
const scopeProblem = computed(() => secretScopeProblem(application.value))
const scopeMissing = computed(() => scoped.value && scopeChoice.value === OTHER && otherApplication.value.trim() === '')
/** How deploy.yaml refers to it: "${DATABASE_PASSWORD}". */
const reference = computed(() => `\${${trimmedName.value || 'NAME'}}`)
const valueProblem = computed(() => secretValueProblem(value.value))
/** Characters a URL cannot carry: said, never the value. */
const valueHint = computed(() => urlCharacterHint(value.value))
/** PUT creates and replaces alike: say which one the form is about to do, for this scope and name. */
const replacing = computed(() => replaces(trimmedName.value, secrets.data.value ?? [], application.value))
const ready = computed(() => trimmedName.value !== '' && value.value !== '' && nameProblem.value === '' && valueProblem.value === '' && scopeProblem.value === '' && !scopeMissing.value)

watch([name, value, scopeChoice, otherApplication], () => {
  stored.value = null
})

async function set() {
  if (setting.value || !ready.value) return
  setting.value = true
  setError.value = null
  const target = { name: trimmedName.value, application: application.value }
  const body: SetSecretRequest = target.application ? { value: value.value, application: target.application } : { value: value.value }
  // Whatever the agent answers, the value has left this page.
  value.value = ''
  try {
    await agent.put(`/secrets/${encodeURIComponent(target.name)}`, { body })
    name.value = ''
    stored.value = target
    void secrets.refresh()
  }
  catch (cause) {
    const error = toAgentError(cause)
    // An agent before 0.9 refuses the scope as an unknown field of the body: nothing was stored.
    if (target.application && error.code === 'INVALID_REQUEST' && error.message.startsWith('invalid JSON body')) {
      legacy.value = true
      scopeChoice.value = ''
      setError.value = new Refusal(error.status, error.code, 'This server\'s agent is older than 0.9 and keeps one list of secrets for the whole server; nothing was stored. Store it for the server, or upgrade the agent first.')
    }
    else {
      setError.value = error
    }
  }
  finally {
    setting.value = false
  }
}

// --- remove ---------------------------------------------------------------------

const removing = ref<Secret | null>(null)
const removePending = ref(false)
const removeError = shallowRef<AgentError | null>(null)

const removalConsequence = computed(() => (removing.value ? secretRemovalConsequence(removing.value.name, removing.value.application ?? '', secrets.data.value ?? []) : ''))

async function remove() {
  const target = removing.value
  if (!target || removePending.value) return
  removePending.value = true
  removeError.value = null
  try {
    await agent.del(`/secrets/${encodeURIComponent(target.name)}`, { query: { application: target.application || undefined } })
    removing.value = null
    void secrets.refresh()
  }
  catch (cause) {
    removeError.value = toAgentError(cause)
    // Already gone: the list on screen was stale.
    if (removeError.value.notFound) void secrets.refresh()
  }
  finally {
    removePending.value = false
  }
}

function closeRemove() {
  removing.value = null
  removeError.value = null
}
</script>

<template>
  <div>
    <PageHeader :crumbs="[{ label: 'Secrets' }]" />
    <PageBody>
      <StaleNotice :error="secrets.data.value ? secrets.error.value : null" :updated-at="secrets.updatedAt.value" />

      <div class="space-y-8">
        <UiPanel title="Secrets" :meta="secrets.data.value?.length ?? null">
          <TableSkeleton v-if="secrets.loading.value" :rows="3" :columns="4" />
          <ErrorState
            v-else-if="secrets.error.value && !secrets.data.value"
            :error="secrets.error.value"
            subject="secrets"
            :retrying="secrets.refreshing.value"
            @retry="secrets.refresh()"
          />
          <EmptyState v-else-if="(secrets.data.value?.length ?? 0) === 0" title="No secrets stored">
            A secret is a value for <span class="mono text-fg">${NAME}</span> in an <span class="mono text-fg">env</span> value of deploy.yaml, kept on the server so that no CI job or laptop has to hold it.
            Add one below<template v-if="!mayAdmin"> with an admin token</template>, or run <span class="mono text-fg">shipwick secret set NAME</span>.
          </EmptyState>
          <div v-else class="overflow-x-auto">
            <table class="data-table stack" aria-label="Secrets">
              <thead>
                <tr>
                  <th>Name</th>
                  <!-- An agent before 0.9 keeps one list for the whole server and says no scope: no column. -->
                  <th v-if="scoped">
                    Scope
                  </th>
                  <th>Created</th>
                  <th>Updated</th>
                  <th v-if="mayAdmin" class="right">
                    <span class="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="s in sorted" :key="`${s.application ?? ''}/${s.name}`">
                  <td data-primary class="mono font-medium">
                    {{ s.name }}<span v-if="scoped && s.application" class="ml-2 font-sans font-normal text-fg-muted rows:hidden">{{ s.application }}</span>
                  </td>
                  <td v-if="scoped" data-label="Scope" class="cards:!hidden">
                    <UiTooltip
                      :class="s.application ? 'mono' : 'text-fg-muted'"
                      :text="s.application ? `Only ${s.application}'s \${${s.name}} resolves to it, before a server-wide ${s.name}` : 'Every application without a secret of its own by this name falls back to it'"
                    >{{ scopeLabel(s.application) }}</UiTooltip>
                  </td>
                  <td data-label="Created" class="text-fg-muted">
                    <TimeAgo :time="s.created_at" /><span class="mono ml-2 text-xs text-fg-subtle cards:hidden">{{ formatAbsoluteUtc(s.created_at) }}</span>
                  </td>
                  <td data-label="Updated" class="text-fg-muted">
                    <template v-if="s.updated_at !== s.created_at">
                      <TimeAgo :time="s.updated_at" /><span class="mono ml-2 text-xs text-fg-subtle cards:hidden">{{ formatAbsoluteUtc(s.updated_at) }}</span>
                    </template>
                    <span v-else>never replaced</span>
                  </td>
                  <td v-if="mayAdmin" class="right">
                    <UiButton variant="danger" size="sm" :aria-label="`Remove ${s.name}${s.application ? ` of ${s.application}` : ''}`" @click="removing = s">
                      Remove
                    </UiButton>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <p class="border-t border-line px-4 py-2 text-xs text-fg-subtle">
            Values are written encrypted and never returned: not here, not by the API, not in an error. The agent fills them in when a deploy.yaml is deployed; a deployment keeps the values it was started with.
            <template v-if="scoped">
              A secret of one application answers that application's <span class="mono">${NAME}</span> before a server-wide one; the server's is what every other application gets.
            </template>
          </p>
        </UiPanel>

        <UiPanel v-if="mayAdmin" :title="replacing ? 'Replace a secret' : 'Add a secret'">
          <form class="grid gap-4 px-4 py-4 sm:grid-cols-2 lg:grid-cols-[minmax(0,13rem)_minmax(0,13rem)_minmax(0,1fr)_auto] lg:items-start" @submit.prevent="set">
            <div>
              <label for="secret-name" class="label block">Name</label>
              <input
                id="secret-name"
                v-model="name"
                type="text"
                class="input mono mt-1.5"
                placeholder="DATABASE_PASSWORD"
                autocomplete="off"
                autocapitalize="off"
                spellcheck="false"
                required
                :aria-invalid="nameProblem !== '' || undefined"
                aria-describedby="secret-name-help"
              >
              <p id="secret-name-help" class="mt-1.5 text-xs" :class="nameProblem ? 'text-danger' : replacing ? 'text-warn' : 'text-fg-muted'">
                <template v-if="nameProblem">
                  {{ nameProblem }}
                </template>
                <template v-else-if="replacing">
                  Already stored<template v-if="application"> for {{ application }}</template><template v-else-if="scoped"> for the server</template>: the value is replaced. Deployments already made keep the old one; the next deploy gets the new one.
                </template>
                <template v-else>
                  Referenced as <span class="mono">{{ reference }}</span> in an env value.
                </template>
              </p>
            </div>
            <!-- The scope: the server's, which every application falls back to, or one application's own. Hidden for an agent before 0.9, which has only the server's. -->
            <div v-if="scoped">
              <label for="secret-scope" class="label block">For</label>
              <select id="secret-scope" v-model="scopeChoice" class="input mt-1.5" :class="scopeChoice !== '' && scopeChoice !== OTHER ? 'mono' : ''" aria-describedby="secret-scope-help">
                <option value="">This server: every application</option>
                <option v-for="a in knownApplications" :key="a" :value="a" class="mono">{{ a }}</option>
                <option :value="OTHER">Another application…</option>
              </select>
              <input
                v-if="scopeChoice === OTHER"
                v-model="otherApplication"
                type="text"
                class="input mono mt-1.5"
                placeholder="my-api"
                autocomplete="off"
                autocapitalize="off"
                spellcheck="false"
                required
                aria-label="The application's name"
                :aria-invalid="scopeProblem !== '' || undefined"
                aria-describedby="secret-scope-help"
              >
              <p id="secret-scope-help" class="mt-1.5 text-xs" :class="scopeProblem ? 'text-danger' : 'text-fg-muted'">
                <template v-if="scopeProblem">
                  {{ scopeProblem }}
                </template>
                <template v-else-if="scopeChoice === OTHER">
                  The application need not exist yet: its first deployment finds the secret.
                </template>
                <template v-else-if="application">
                  Only <span class="mono">{{ application }}</span> sees it, before a server-wide one by the same name. For what an application owns: its database password, its session key.
                </template>
                <template v-else>
                  For what applications share: a registry login, the key of a mail provider. An application's own secret by the same name comes first.
                </template>
              </p>
            </div>
            <div>
              <label for="secret-value" class="label block">Value</label>
              <input
                id="secret-value"
                v-model="value"
                type="password"
                class="input mono mt-1.5"
                autocomplete="off"
                autocapitalize="off"
                spellcheck="false"
                required
                :aria-invalid="valueProblem !== '' || undefined"
                aria-describedby="secret-value-help"
              >
              <p id="secret-value-help" class="mt-1.5 text-xs" :class="valueProblem ? 'text-danger' : valueHint ? 'text-warn' : 'text-fg-muted'">
                {{ valueProblem || valueHint || 'Sent once, over this session, and cleared from the page as soon as it is sent. Up to 64 KB.' }}
              </p>
            </div>
            <UiButton type="submit" variant="primary" class="lg:mt-[1.375rem]" :pending="setting" :disabled="!ready">
              {{ replacing ? 'Replace secret' : 'Add secret' }}
            </UiButton>
            <p v-if="stored" class="flex items-center gap-2 font-medium text-ok sm:col-span-2 lg:col-span-4" role="status">
              <UiIcon name="check" :size="14" />
              <span><span class="mono">{{ stored.name }}</span> is stored<template v-if="stored.application"> for <span class="mono">{{ stored.application }}</span></template>. It applies to the next deploy that refers to it.</span>
            </p>
            <InlineError :error="setError" class="sm:col-span-2 lg:col-span-4" />
          </form>
        </UiPanel>
        <p v-else class="text-xs text-fg-subtle">
          {{ roleHint('admin') }} to add or remove a secret; this token can only list them.
        </p>
      </div>
    </PageBody>

    <ConfirmDialog
      :open="removing !== null"
      :title="`Remove ${removing?.name ?? ''}${removing?.application ? ` of ${removing.application}` : ''}`"
      confirm-label="Remove secret"
      danger
      :pending="removePending"
      :error="removeError"
      @close="closeRemove"
      @confirm="remove"
    >
      {{ removalConsequence }}
    </ConfirmDialog>
  </div>
</template>
