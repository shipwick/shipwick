<script setup lang="ts">
import type { Certificate, SetCertificateRequest } from '~/types/api'
import type { AgentError } from '~/utils/agentError'
import { toAgentError } from '~/utils/agentError'
import { certificateHostnameProblem, certificateRemovalConsequence, chainProblem, expiryDisplay, formatDate, keyProblem, replacesCertificate, sortByExpiry } from '~/utils/certificates'
import { roleHint } from '~/utils/roles'

useHead({ title: 'Certificates' })

const agent = useAgent()
const access = useAccess()
const server = useServerInfo()
const now = useNow()

/** What each certificate says about itself: never the key, never the PEM. */
const certificates = usePolling<Certificate[]>(signal => agent.get<Certificate[]>('/certificates', { signal }), { interval: 60_000 })

const mayAdmin = computed(() => access.can('admin'))
const unsupported = computed(() => certificates.error.value?.code === 'ENDPOINT_NOT_FOUND')
const dnsChallenge = computed(() => server.data.value?.proxy.dns_challenge === true)
const rows = computed(() => sortByExpiry(certificates.data.value ?? []))

// --- add ------------------------------------------------------------------------

const hostname = ref('')
const chain = ref('')
const key = ref('')
const saving = ref(false)
const saveError = shallowRef<AgentError | null>(null)
/** What the agent stored, for the confirmation; gone with the next edit. */
const stored = shallowRef<Certificate | null>(null)

const target = computed(() => hostname.value.trim().toLowerCase())
const hostnameIssue = computed(() => certificateHostnameProblem(target.value))
const chainIssue = computed(() => chainProblem(chain.value))
const keyIssue = computed(() => keyProblem(key.value))
const replacing = computed(() => replacesCertificate(target.value, certificates.data.value ?? []))
const ready = computed(() => target.value !== '' && chain.value.trim() !== '' && key.value.trim() !== ''
  && hostnameIssue.value === '' && chainIssue.value === '' && keyIssue.value === '')

watch([hostname, chain, key], () => {
  stored.value = null
})

async function save() {
  if (saving.value || !ready.value) return
  saving.value = true
  saveError.value = null
  const name = target.value
  const body: SetCertificateRequest = { certificate: chain.value, key: key.value }
  // Whatever the agent answers, the key has left this page.
  key.value = ''
  try {
    const answer = await agent.put<Certificate>(`/certificates/${encodeURIComponent(name)}`, { body })
    hostname.value = ''
    chain.value = ''
    stored.value = answer
    void certificates.refresh()
  }
  catch (cause) {
    saveError.value = toAgentError(cause)
  }
  finally {
    saving.value = false
  }
}

// --- remove ---------------------------------------------------------------------

const removing = ref<Certificate | null>(null)
const removePending = ref(false)
const removeError = shallowRef<AgentError | null>(null)

async function remove() {
  const victim = removing.value
  if (!victim || removePending.value) return
  removePending.value = true
  removeError.value = null
  try {
    await agent.del(`/certificates/${encodeURIComponent(victim.hostname)}`)
    removing.value = null
    void certificates.refresh()
  }
  catch (cause) {
    removeError.value = toAgentError(cause)
    if (removeError.value.notFound) void certificates.refresh()
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
    <PageHeader :crumbs="[{ label: 'Certificates' }]" />
    <PageBody>
      <StaleNotice :error="certificates.data.value ? certificates.error.value : null" :updated-at="certificates.updatedAt.value" />

      <div v-if="unsupported" class="rounded-sm border border-line">
        <EmptyState title="This agent takes no certificates">
          Supplying a certificate of your own needs a newer agent. With this one, every certificate is obtained by the proxy.
        </EmptyState>
      </div>

      <div v-else class="space-y-8">
        <UiPanel title="Supplied certificates" :meta="certificates.data.value?.length ?? null">
          <TableSkeleton v-if="certificates.loading.value" :rows="2" :columns="4" />
          <ErrorState
            v-else-if="certificates.error.value && !certificates.data.value"
            :error="certificates.error.value"
            subject="certificates"
            :retrying="certificates.refreshing.value"
            @retry="certificates.refresh()"
          />
          <EmptyState v-else-if="rows.length === 0" title="No certificates supplied">
            None is needed: the proxy obtains a certificate for every hostname by itself and renews it. Supply one for a hostname whose certificate must come from elsewhere — a company authority, or a wildcard without a DNS challenge — below<template v-if="!mayAdmin"> with an admin token</template>, or with <span class="mono text-fg">shipwick cert set</span>.
          </EmptyState>
          <div v-else class="overflow-x-auto">
            <table class="data-table stack" aria-label="Supplied certificates">
              <thead>
                <tr>
                  <th>Stored under</th>
                  <th>Covers</th>
                  <th>Issuer</th>
                  <th>Expires</th>
                  <th v-if="mayAdmin" class="right">
                    <span class="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="c in rows" :key="c.hostname">
                  <td data-primary class="mono font-medium">
                    {{ c.hostname }}
                  </td>
                  <td data-label="Covers" class="mono text-fg-muted">
                    <span class="block">
                      <span v-for="subject in c.subjects" :key="subject" class="block break-words">{{ subject }}</span>
                    </span>
                  </td>
                  <td data-label="Issuer">
                    {{ c.issuer || '—' }}
                  </td>
                  <td data-label="Expires" :title="`Valid from ${formatDate(c.not_before)} until ${formatDate(c.not_after)}`">
                    <span class="mono">{{ formatDate(c.not_after) }}</span>
                    <StatusBadge
                      v-if="expiryDisplay(c.not_after, now).tone !== 'muted'"
                      v-bind="expiryDisplay(c.not_after, now)"
                      class="ml-2"
                    />
                  </td>
                  <td v-if="mayAdmin" class="right">
                    <UiButton variant="danger" size="sm" :aria-label="`Remove the certificate of ${c.hostname}`" @click="removing = c">
                      Remove
                    </UiButton>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <p class="border-t border-line px-4 py-2 text-xs text-fg-subtle">
            A supplied certificate belongs to the server: every hostname it covers, of any application, is served with it, is not asked of any authority and does not wait for DNS. Nothing renews it: it is marked in its last 30 days, and stays listed and served after it expired until it is replaced or removed.
            <template v-if="dnsChallenge">
              Every other certificate is obtained through Cloudflare DNS.
            </template>
          </p>
        </UiPanel>

        <UiPanel v-if="mayAdmin" :title="replacing ? 'Replace a certificate' : 'Add a certificate'">
          <form class="space-y-4 px-4 py-4" @submit.prevent="save">
            <div class="max-w-md">
              <label for="cert-hostname" class="label block">Hostname</label>
              <input
                id="cert-hostname"
                v-model="hostname"
                type="text"
                class="input mono mt-1.5"
                placeholder="example.com"
                autocomplete="off"
                autocapitalize="off"
                spellcheck="false"
                required
                :aria-invalid="hostnameIssue !== '' || undefined"
                aria-describedby="cert-hostname-help"
              >
              <p id="cert-hostname-help" class="mt-1.5 text-xs" :class="hostnameIssue ? 'text-danger' : replacing ? 'text-warn' : 'text-fg-muted'">
                <template v-if="hostnameIssue">
                  {{ hostnameIssue }}
                </template>
                <template v-else-if="replacing">
                  A certificate is stored under this name: it is replaced.
                </template>
                <template v-else>
                  The name to store it under, which the certificate must cover. A wildcard certificate goes under the wildcard, <span class="mono">*.example.com</span>.
                </template>
              </p>
            </div>
            <div class="grid gap-4 lg:grid-cols-2">
              <div>
                <label for="cert-chain" class="label block">Certificate chain <span class="normal-case tracking-normal text-fg-subtle">PEM, fullchain.pem</span></label>
                <textarea
                  id="cert-chain"
                  v-model="chain"
                  class="input mono mt-1.5 !h-40 resize-y !py-2 !text-xs leading-[1.125rem]"
                  placeholder="-----BEGIN CERTIFICATE-----"
                  autocomplete="off"
                  autocapitalize="off"
                  spellcheck="false"
                  required
                  :aria-invalid="chainIssue !== '' || undefined"
                  aria-describedby="cert-chain-help"
                />
                <p id="cert-chain-help" class="mt-1.5 text-xs" :class="chainIssue ? 'text-danger' : 'text-fg-muted'">
                  {{ chainIssue || 'The hostname\'s own certificate first, the intermediates after it. Up to 64 KB.' }}
                </p>
              </div>
              <div>
                <label for="cert-key" class="label block">Private key <span class="normal-case tracking-normal text-fg-subtle">PEM, privkey.pem</span></label>
                <textarea
                  id="cert-key"
                  v-model="key"
                  class="input mono mt-1.5 !h-40 resize-y !py-2 !text-xs leading-[1.125rem]"
                  placeholder="-----BEGIN PRIVATE KEY-----"
                  autocomplete="off"
                  autocapitalize="off"
                  spellcheck="false"
                  required
                  :aria-invalid="keyIssue !== '' || undefined"
                  aria-describedby="cert-key-help"
                />
                <p id="cert-key-help" class="mt-1.5 text-xs" :class="keyIssue ? 'text-danger' : 'text-fg-muted'">
                  {{ keyIssue || 'Without a passphrase. Sent once, cleared from the page as soon as it is sent, stored encrypted and never shown again.' }}
                </p>
              </div>
            </div>
            <UiButton type="submit" variant="primary" :pending="saving" :disabled="!ready">
              {{ replacing ? 'Replace certificate' : 'Add certificate' }}
            </UiButton>
            <p v-if="stored" class="flex items-start gap-2 font-medium text-ok" role="status">
              <UiIcon name="check" :size="14" class="mt-[3px]" />
              <span>
                Stored under <span class="mono">{{ stored.hostname }}</span>: issued by {{ stored.issuer || 'an unnamed authority' }}, valid until <span class="mono">{{ formatDate(stored.not_after) }}</span>, for <span class="mono break-all">{{ stored.subjects.join(', ') }}</span>. The proxy serves it already.
              </span>
            </p>
            <InlineError :error="saveError" />
          </form>
        </UiPanel>
        <p v-else class="text-xs text-fg-subtle">
          {{ roleHint('admin') }} to add or remove a certificate; this token can only list them.
        </p>
      </div>
    </PageBody>

    <ConfirmDialog
      :open="removing !== null"
      :title="`Remove the certificate for ${removing?.hostname ?? ''}`"
      confirm-label="Remove certificate"
      danger
      :pending="removePending"
      :error="removeError"
      @close="closeRemove"
      @confirm="remove"
    >
      {{ removing ? certificateRemovalConsequence(removing, dnsChallenge) : '' }}
    </ConfirmDialog>
  </div>
</template>
