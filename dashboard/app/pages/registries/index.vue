<script setup lang="ts">
import type { Registry, SetRegistryRequest } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { toAgentError } from '~/utils/agentError'
import { formatAbsoluteUtc } from '~/utils/format'
import { normalizeRegistry, passwordProblem, registryProblem, registryRemovalConsequence, replacesRegistry, usernameProblem } from '~/utils/registries'
import { roleHint } from '~/utils/roles'

useHead({ title: 'Registries' })

const route = useRoute()
const agent = useAgent()
const access = useAccess()

/** Names and usernames only: the API never returns a password, and this page never holds one longer than its request. */
const registries = usePolling<Registry[]>(signal => agent.get<Registry[]>('/registries', { signal }), { interval: 30_000 })

const mayAdmin = computed(() => access.can('admin'))
/** An agent from before it kept registry credentials: it reads the server's Docker configuration file, and nothing else. */
const unsupported = computed(() => registries.error.value?.code === 'ENDPOINT_NOT_FOUND')

// --- log in ---------------------------------------------------------------------

// A deployment refused by a registry links here with the registry it names.
const registry = ref(typeof route.query.registry === 'string' ? route.query.registry : '')
const username = ref('')
const password = ref('')
const saving = ref(false)
const saveError = shallowRef<AgentError | null>(null)
/** The registry just logged in to, for the confirmation line; gone with the next edit. */
const stored = ref('')

const normalized = computed(() => normalizeRegistry(registry.value))
const registryIssue = computed(() => registryProblem(registry.value))
const usernameIssue = computed(() => usernameProblem(username.value))
const passwordIssue = computed(() => passwordProblem(password.value))
/** PUT creates and replaces alike: say which one the form is about to do. */
const replacing = computed(() => replacesRegistry(normalized.value, registries.data.value ?? []))
const ready = computed(() => normalized.value !== null && username.value !== '' && password.value !== ''
  && usernameIssue.value === '' && passwordIssue.value === '')

watch([registry, username, password], () => {
  stored.value = ''
})

async function logIn() {
  const target = normalized.value
  if (saving.value || !ready.value || target === null) return
  saving.value = true
  saveError.value = null
  const body: SetRegistryRequest = { username: username.value, password: password.value }
  // Whatever the agent answers, the password has left this page.
  password.value = ''
  try {
    await agent.put(`/registries/${encodeURIComponent(target)}`, { body })
    registry.value = ''
    username.value = ''
    stored.value = target
    void registries.refresh()
  }
  catch (cause) {
    saveError.value = toAgentError(cause)
  }
  finally {
    saving.value = false
  }
}

// --- log out --------------------------------------------------------------------

const removing = ref<Registry | null>(null)
const removePending = ref(false)
const removeError = shallowRef<AgentError | null>(null)

async function remove() {
  const target = removing.value
  if (!target || removePending.value) return
  removePending.value = true
  removeError.value = null
  try {
    await agent.del(`/registries/${encodeURIComponent(target.registry)}`)
    removing.value = null
    void registries.refresh()
  }
  catch (cause) {
    removeError.value = toAgentError(cause)
    // Already gone: the list on screen was stale.
    if (removeError.value.notFound) void registries.refresh()
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
    <PageHeader :crumbs="[{ label: 'Registries' }]" />
    <PageBody>
      <StaleNotice :error="registries.data.value ? registries.error.value : null" :updated-at="registries.updatedAt.value" />

      <div v-if="unsupported" class="rounded-sm border border-line">
        <EmptyState title="This agent keeps no registry credentials">
          Storing them needs a newer agent. This one pulls private images with the Docker configuration file on the server: run <span class="mono text-fg">docker login</span> there, or upgrade the agent.
        </EmptyState>
      </div>

      <div v-else class="space-y-8">
        <UiPanel title="Registries" :meta="registries.data.value?.length ?? null">
          <TableSkeleton v-if="registries.loading.value" :rows="2" :columns="4" />
          <ErrorState
            v-else-if="registries.error.value && !registries.data.value"
            :error="registries.error.value"
            subject="registries"
            :retrying="registries.refreshing.value"
            @retry="registries.refresh()"
          />
          <EmptyState v-else-if="(registries.data.value?.length ?? 0) === 0" title="No registry credentials stored">
            Public images need none. For a private image, log the server in to its registry below<template v-if="!mayAdmin"> with an admin token</template>, or run <span class="mono text-fg">shipwick registry login ghcr.io --username NAME</span>.
          </EmptyState>
          <div v-else class="overflow-x-auto">
            <table class="data-table stack">
              <thead>
                <tr>
                  <th>Registry</th>
                  <th>Username</th>
                  <th>Logged in</th>
                  <th>Updated</th>
                  <th v-if="mayAdmin" class="right">
                    <span class="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="r in registries.data.value" :key="r.registry">
                  <td data-primary class="mono font-medium">
                    {{ r.registry }}
                  </td>
                  <td data-label="Username" class="mono">
                    {{ r.username }}
                  </td>
                  <td data-label="Logged in" class="text-fg-muted">
                    <TimeAgo :time="r.created_at" /><span class="mono ml-2 text-xs text-fg-subtle max-lg:hidden">{{ formatAbsoluteUtc(r.created_at) }}</span>
                  </td>
                  <td data-label="Updated" class="text-fg-muted">
                    <TimeAgo v-if="r.updated_at !== r.created_at" :time="r.updated_at" />
                    <span v-else title="Never replaced since it was stored">never</span>
                  </td>
                  <td v-if="mayAdmin" class="right">
                    <UiButton variant="danger" size="sm" @click="removing = r">
                      Log out
                    </UiButton>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <p class="border-t border-line px-4 py-2 text-xs text-fg-subtle">
            The agent sends a registry's credential with every pull of an image from it: a deployment, a rollback, a job, a replica whose image is gone. Passwords are written encrypted and never returned: not here, not by the API, not in an error. Without a stored credential the Docker configuration file on the server is consulted, as before.
          </p>
        </UiPanel>

        <UiPanel v-if="mayAdmin" :title="replacing ? 'Replace a credential' : 'Log in to a registry'">
          <form class="grid gap-4 px-4 py-4 sm:grid-cols-2 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_minmax(0,1fr)_auto] lg:items-end" @submit.prevent="logIn">
            <div>
              <label for="registry-name" class="label block">Registry</label>
              <input
                id="registry-name"
                v-model="registry"
                type="text"
                class="input mono mt-1.5"
                placeholder="ghcr.io"
                autocomplete="off"
                autocapitalize="off"
                spellcheck="false"
                required
                :aria-invalid="registryIssue !== '' || undefined"
                aria-describedby="registry-name-help"
              >
              <p id="registry-name-help" class="mt-1.5 text-xs lg:min-h-[2.25rem]" :class="registryIssue ? 'text-danger' : replacing ? 'text-warn' : 'text-fg-muted'">
                <template v-if="registryIssue">
                  {{ registryIssue }}
                </template>
                <template v-else-if="replacing">
                  Already stored: the credential is replaced.
                </template>
                <template v-else-if="normalized && normalized !== registry.trim()">
                  Stored as <span class="mono">{{ normalized }}</span>.
                </template>
                <template v-else>
                  As image references name it, with its port if it has one.
                </template>
              </p>
            </div>
            <div>
              <label for="registry-username" class="label block">Username</label>
              <input
                id="registry-username"
                v-model="username"
                type="text"
                class="input mono mt-1.5"
                autocomplete="off"
                autocapitalize="off"
                spellcheck="false"
                required
                :aria-invalid="usernameIssue !== '' || undefined"
                aria-describedby="registry-username-help"
              >
              <p id="registry-username-help" class="mt-1.5 text-xs lg:min-h-[2.25rem]" :class="usernameIssue ? 'text-danger' : 'text-fg-muted'">
                {{ usernameIssue || 'The account, or what the registry asks for with a token.' }}
              </p>
            </div>
            <div>
              <label for="registry-password" class="label block">Password or token</label>
              <input
                id="registry-password"
                v-model="password"
                type="password"
                class="input mono mt-1.5"
                autocomplete="new-password"
                autocapitalize="off"
                spellcheck="false"
                required
                :aria-invalid="passwordIssue !== '' || undefined"
                aria-describedby="registry-password-help"
              >
              <p id="registry-password-help" class="mt-1.5 text-xs lg:min-h-[2.25rem]" :class="passwordIssue ? 'text-danger' : 'text-fg-muted'">
                {{ passwordIssue || 'Sent once and cleared from the page as soon as it is sent. A token that may read images is enough.' }}
              </p>
            </div>
            <UiButton type="submit" variant="primary" class="justify-self-start lg:mb-[2.625rem]" :pending="saving" :disabled="!ready">
              {{ replacing ? 'Replace' : 'Log in' }}
            </UiButton>
            <p class="text-xs text-fg-muted sm:col-span-2 lg:col-span-4">
              The agent checks the credential against the registry before it stores anything, which can take a moment.
            </p>
            <p v-if="stored" class="flex items-center gap-2 font-medium text-ok sm:col-span-2 lg:col-span-4" role="status">
              <UiIcon name="check" :size="14" />
              <span><span class="mono">{{ stored }}</span> accepted the credential and it is stored. It is used from the next pull on.</span>
            </p>
            <InlineError :error="saveError" class="sm:col-span-2 lg:col-span-4" />
          </form>
        </UiPanel>
        <p v-else class="text-xs text-fg-subtle">
          {{ roleHint('admin') }} to log in to a registry or out of one; this token can only list them.
        </p>
      </div>
    </PageBody>

    <ConfirmDialog
      :open="removing !== null"
      :title="`Log out of ${removing?.registry ?? ''}`"
      confirm-label="Log out"
      danger
      :pending="removePending"
      :error="removeError"
      @close="closeRemove"
      @confirm="remove"
    >
      {{ registryRemovalConsequence(removing?.registry ?? '') }}
    </ConfirmDialog>
  </div>
</template>
