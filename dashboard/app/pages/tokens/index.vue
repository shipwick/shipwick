<script setup lang="ts">
import type { CreatedToken, CreateTokenRequest, Role, Token } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { toAgentError } from '~/utils/agentError'
import { formatAbsoluteUtc } from '~/utils/format'
import { ROLE_DESCRIPTIONS, ROLE_ORDER, forbiddenExplanation } from '~/utils/roles'

useHead({ title: 'Tokens' })

const agent = useAgent()
const access = useAccess()

const tokens = usePolling<Token[]>(signal => agent.get<Token[]>('/tokens', { signal }), { interval: 30_000 })

/** The page was opened by a token below admin: the list is refused, and the agent says with which role. */
const forbidden = computed(() => (tokens.error.value?.code === 'FORBIDDEN' ? tokens.error.value : null))

// --- create ---------------------------------------------------------------------

const NAME_PATTERN = /^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$/

const name = ref('')
const role = ref<Role>('deploy')
const creating = ref(false)
const createError = shallowRef<AgentError | null>(null)
/** Shown once, right after creation; gone with the next navigation or "Done". */
const created = shallowRef<CreatedToken | null>(null)
const copied = ref(false)

const trimmedName = computed(() => name.value.trim().toLowerCase())
const nameProblem = computed(() => {
  const value = trimmedName.value
  if (value === '') return ''
  if (value === 'root') return 'root is the token the agent is configured with; choose another name.'
  if (!NAME_PATTERN.test(value)) return 'Lowercase letters, digits and dashes, up to 40 characters, e.g. ci.'
  return ''
})

async function create() {
  if (creating.value || trimmedName.value === '' || nameProblem.value) return
  creating.value = true
  createError.value = null
  try {
    const body: CreateTokenRequest = { name: trimmedName.value, role: role.value }
    created.value = await agent.post<CreatedToken>('/tokens', { body })
    copied.value = false
    name.value = ''
    void tokens.refresh()
  }
  catch (cause) {
    createError.value = toAgentError(cause)
  }
  finally {
    creating.value = false
  }
}

async function copy() {
  if (!created.value) return
  try {
    await navigator.clipboard.writeText(created.value.token)
    copied.value = true
  }
  catch {
    // No clipboard access (http, or denied): the value is selectable right there.
  }
}

// --- revoke ---------------------------------------------------------------------

const revoking = ref<Token | null>(null)
const revokePending = ref(false)
const revokeError = shallowRef<AgentError | null>(null)

async function revoke() {
  const target = revoking.value
  if (!target || revokePending.value) return
  revokePending.value = true
  revokeError.value = null
  try {
    await agent.del(`/tokens/${encodeURIComponent(target.name)}`)
    revoking.value = null
    void tokens.refresh()
  }
  catch (cause) {
    revokeError.value = toAgentError(cause)
    // Already gone: the list on screen was stale.
    if (revokeError.value.notFound) void tokens.refresh()
  }
  finally {
    revokePending.value = false
  }
}

function closeRevoke() {
  revoking.value = null
  revokeError.value = null
}

const own = computed(() => access.token.value?.name ?? '')
</script>

<template>
  <div>
    <PageHeader :crumbs="[{ label: 'Tokens' }]" />
    <PageBody>
      <StaleNotice :error="tokens.data.value ? tokens.error.value : null" :updated-at="tokens.updatedAt.value" />

      <div v-if="forbidden" class="rounded-sm border border-line">
        <EmptyState title="Tokens are managed by admins">
          {{ forbiddenExplanation(forbidden) }}. Sign in with an admin token to create or revoke tokens.
        </EmptyState>
      </div>

      <div v-else class="space-y-8">
        <UiPanel title="Tokens" :meta="tokens.data.value?.length ?? null">
          <TableSkeleton v-if="tokens.loading.value" :rows="3" :columns="4" />
          <ErrorState
            v-else-if="tokens.error.value && !tokens.data.value"
            :error="tokens.error.value"
            subject="tokens"
            :retrying="tokens.refreshing.value"
            @retry="tokens.refresh()"
          />
          <EmptyState v-else-if="(tokens.data.value?.length ?? 0) === 0" title="No tokens besides root">
            Create one below for CI, a teammate or a read-only dashboard, so the root token can stay on the server.
          </EmptyState>
          <div v-else class="overflow-x-auto">
            <table class="data-table stack">
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Role</th>
                  <th>Created</th>
                  <th>Last used</th>
                  <th class="right">
                    <span class="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="t in tokens.data.value" :key="t.id">
                  <td data-primary class="mono font-medium">
                    {{ t.name }}<span v-if="t.name === own" class="ml-2 font-sans text-xs font-normal text-fg-subtle">this session</span>
                  </td>
                  <td data-label="Role" class="mono" :title="ROLE_DESCRIPTIONS[t.role] ?? undefined">
                    {{ t.role }}
                  </td>
                  <td data-label="Created" class="text-fg-muted">
                    <TimeAgo :time="t.created_at" /><span class="mono ml-2 text-xs text-fg-subtle max-sm:hidden">{{ formatAbsoluteUtc(t.created_at) }}</span>
                  </td>
                  <td data-label="Last used" class="text-fg-muted" title="Kept to the minute">
                    <TimeAgo v-if="t.last_used_at" :time="t.last_used_at" />
                    <span v-else>never</span>
                  </td>
                  <td class="right">
                    <UiButton variant="danger" size="sm" @click="revoking = t">
                      Revoke
                    </UiButton>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <p class="border-t border-line px-4 py-2 text-xs text-fg-subtle">
            The root token is configured on the agent (<span class="mono">SHIPWICK_AGENT_TOKEN</span>, or generated on first start) and is not listed here: it has the admin role and cannot be revoked through the API.
          </p>
        </UiPanel>

        <UiPanel title="Create a token">
          <div v-if="created" class="space-y-3 px-4 py-4" role="status">
            <p class="flex items-center gap-2 font-medium text-ok">
              <UiIcon name="check" :size="14" />
              Token <span class="mono">{{ created.name }}</span> created with the {{ created.role }} role
            </p>
            <div class="flex flex-wrap items-center gap-2">
              <code class="mono select-all break-all rounded-sm border border-line bg-inset px-2.5 py-1.5 text-sm">{{ created.token }}</code>
              <UiButton size="sm" @click="copy">
                <UiIcon name="copy" :size="12" />
                {{ copied ? 'Copied' : 'Copy' }}
              </UiButton>
            </div>
            <p class="text-fg-muted">
              Copy it now: it will not be shown again. The agent keeps only a hash. Use it as <span class="mono text-fg">shipwick login --token</span> or as the token of a CI job.
            </p>
            <UiButton size="sm" variant="ghost" @click="created = null">
              Done
            </UiButton>
          </div>
          <form v-else class="grid gap-4 px-4 py-4 sm:grid-cols-[minmax(0,1fr)_12rem_auto] sm:items-end" @submit.prevent="create">
            <div>
              <label for="token-name" class="label block">Name</label>
              <input
                id="token-name"
                v-model="name"
                type="text"
                class="input mono mt-1.5"
                placeholder="ci"
                autocomplete="off"
                autocapitalize="off"
                spellcheck="false"
                required
                :aria-invalid="nameProblem !== '' || undefined"
                aria-describedby="token-name-help"
              >
              <p id="token-name-help" class="mt-1.5 text-xs" :class="nameProblem ? 'text-danger' : 'text-fg-muted'">
                {{ nameProblem || 'Names events and deployments made with it: "Application stopped by ci".' }}
              </p>
            </div>
            <div>
              <label for="token-role" class="label block">Role</label>
              <select id="token-role" v-model="role" class="input mono mt-1.5">
                <option v-for="r in ROLE_ORDER" :key="r" :value="r">{{ r }}</option>
              </select>
              <p class="mt-1.5 text-xs text-fg-muted">
                {{ ROLE_DESCRIPTIONS[role] }}
              </p>
            </div>
            <UiButton type="submit" variant="primary" class="sm:mb-[1.625rem]" :pending="creating" :disabled="trimmedName === '' || nameProblem !== ''">
              Create token
            </UiButton>
            <InlineError :error="createError" class="sm:col-span-3" />
          </form>
        </UiPanel>
      </div>
    </PageBody>

    <ConfirmDialog
      :open="revoking !== null"
      :title="`Revoke ${revoking?.name ?? ''}`"
      confirm-label="Revoke token"
      danger
      :pending="revokePending"
      :error="revokeError"
      @close="closeRevoke"
      @confirm="revoke"
    >
      Requests made with <span class="mono text-fg">{{ revoking?.name }}</span> are refused from now on; anything using it (a CI job, another dashboard) stops working until it gets a new token.
      <template v-if="revoking?.name === own">
        This is the token of your own session: you will be signed out.
      </template>
    </ConfirmDialog>
  </div>
</template>
