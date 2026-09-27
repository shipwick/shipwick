<script setup lang="ts">
import type { Secret, SetSecretRequest } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { toAgentError } from '~/utils/agentError'
import { formatAbsoluteUtc } from '~/utils/format'
import { roleHint } from '~/utils/roles'
import { replaces, secretNameProblem, secretRemovalConsequence, secretValueProblem } from '~/utils/secrets'

useHead({ title: 'Secrets' })

const agent = useAgent()
const access = useAccess()

/** Names and dates only: the API never returns a value, and this page never holds one longer than its request. */
const secrets = usePolling<Secret[]>(signal => agent.get<Secret[]>('/secrets', { signal }), { interval: 30_000 })

const mayAdmin = computed(() => access.can('admin'))

// --- set ------------------------------------------------------------------------

const name = ref('')
const value = ref('')
const setting = ref(false)
const setError = shallowRef<AgentError | null>(null)
/** The name just stored, for the confirmation line; gone with the next edit. */
const stored = ref('')

const trimmedName = computed(() => name.value.trim())
const nameProblem = computed(() => secretNameProblem(trimmedName.value))
/** How deploy.yaml refers to it: "${DATABASE_PASSWORD}". */
const reference = computed(() => `\${${trimmedName.value || 'NAME'}}`)
const valueProblem = computed(() => secretValueProblem(value.value))
/** PUT creates and replaces alike: say which one the form is about to do. */
const replacing = computed(() => replaces(trimmedName.value, secrets.data.value ?? []))
const ready = computed(() => trimmedName.value !== '' && value.value !== '' && nameProblem.value === '' && valueProblem.value === '')

watch([name, value], () => {
  stored.value = ''
})

async function set() {
  if (setting.value || !ready.value) return
  setting.value = true
  setError.value = null
  const target = trimmedName.value
  const body: SetSecretRequest = { value: value.value }
  // Whatever the agent answers, the value has left this page.
  value.value = ''
  try {
    await agent.put(`/secrets/${encodeURIComponent(target)}`, { body })
    name.value = ''
    stored.value = target
    void secrets.refresh()
  }
  catch (cause) {
    setError.value = toAgentError(cause)
  }
  finally {
    setting.value = false
  }
}

// --- remove ---------------------------------------------------------------------

const removing = ref<Secret | null>(null)
const removePending = ref(false)
const removeError = shallowRef<AgentError | null>(null)

async function remove() {
  const target = removing.value
  if (!target || removePending.value) return
  removePending.value = true
  removeError.value = null
  try {
    await agent.del(`/secrets/${encodeURIComponent(target.name)}`)
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
          <TableSkeleton v-if="secrets.loading.value" :rows="3" :columns="3" />
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
            <table class="data-table stack">
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Created</th>
                  <th>Updated</th>
                  <th v-if="mayAdmin" class="right">
                    <span class="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="s in secrets.data.value" :key="s.name">
                  <td data-primary class="mono font-medium">
                    {{ s.name }}
                  </td>
                  <td data-label="Created" class="text-fg-muted">
                    <TimeAgo :time="s.created_at" /><span class="mono ml-2 text-xs text-fg-subtle max-sm:hidden">{{ formatAbsoluteUtc(s.created_at) }}</span>
                  </td>
                  <td data-label="Updated" class="text-fg-muted">
                    <template v-if="s.updated_at !== s.created_at">
                      <TimeAgo :time="s.updated_at" /><span class="mono ml-2 text-xs text-fg-subtle max-sm:hidden">{{ formatAbsoluteUtc(s.updated_at) }}</span>
                    </template>
                    <span v-else title="Never replaced since it was created">never</span>
                  </td>
                  <td v-if="mayAdmin" class="right">
                    <UiButton variant="danger" size="sm" @click="removing = s">
                      Remove
                    </UiButton>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <p class="border-t border-line px-4 py-2 text-xs text-fg-subtle">
            Values are written encrypted and never returned: not here, not by the API, not in an error. The agent fills them in when a deploy.yaml is deployed; a deployment keeps the values it was started with.
          </p>
        </UiPanel>

        <UiPanel v-if="mayAdmin" :title="replacing ? 'Replace a secret' : 'Add a secret'">
          <form class="grid gap-4 px-4 py-4 sm:grid-cols-[minmax(0,14rem)_minmax(0,1fr)_auto] sm:items-end" @submit.prevent="set">
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
                  Already stored: the value is replaced. Deployments already made keep the old one; the next deploy gets the new one.
                </template>
                <template v-else>
                  Referenced as <span class="mono">{{ reference }}</span> in an env value.
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
              <p id="secret-value-help" class="mt-1.5 text-xs" :class="valueProblem ? 'text-danger' : 'text-fg-muted'">
                {{ valueProblem || 'Sent once, over this session, and cleared from the page as soon as it is sent. Up to 64 KB.' }}
              </p>
            </div>
            <UiButton type="submit" variant="primary" class="sm:mb-[1.625rem]" :pending="setting" :disabled="!ready">
              {{ replacing ? 'Replace secret' : 'Add secret' }}
            </UiButton>
            <p v-if="stored" class="flex items-center gap-2 font-medium text-ok sm:col-span-3" role="status">
              <UiIcon name="check" :size="14" />
              <span><span class="mono">{{ stored }}</span> is stored. It applies to the next deploy that refers to it.</span>
            </p>
            <InlineError :error="setError" class="sm:col-span-3" />
          </form>
        </UiPanel>
        <p v-else class="text-xs text-fg-subtle">
          {{ roleHint('admin') }} to add or remove a secret; this token can only list them.
        </p>
      </div>
    </PageBody>

    <ConfirmDialog
      :open="removing !== null"
      :title="`Remove ${removing?.name ?? ''}`"
      confirm-label="Remove secret"
      danger
      :pending="removePending"
      :error="removeError"
      @close="closeRemove"
      @confirm="remove"
    >
      {{ secretRemovalConsequence(removing?.name ?? '') }}
    </ConfirmDialog>
  </div>
</template>
