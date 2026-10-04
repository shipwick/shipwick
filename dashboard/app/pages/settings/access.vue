<script setup lang="ts">
import { accessTabs, tabTitle } from '~/utils/tabs'

/**
 * Who may do what on this server: the frame of the Access pages. API tokens,
 * the rules for people who sign in through the provider, and the trail of
 * what was done are a tab each, with an address of its own. All three are
 * the admin's: the agent refuses them to every other role.
 */
const route = useRoute()
const access = useAccess()
const tabs = accessTabs()

useHead({ title: () => tabTitle(tabs, route.path, 'Access') })

/** Known not to be an admin: the agent would refuse every list on these pages. */
const refused = computed(() => access.role.value !== null && access.role.value !== 'admin')
</script>

<template>
  <div>
    <PageHeader :crumbs="[{ label: 'Access' }]" />
    <PageBody>
      <div v-if="refused" class="rounded-sm border border-line">
        <EmptyState title="Access is managed by admins">
          You are signed in as <span class="mono text-fg">{{ access.token.value?.name }}</span> with the {{ access.role.value }} role; this needs admin.
          Sign in with an admin token to create or revoke tokens, to say who may sign in, and to read the audit trail.
        </EmptyState>
      </div>

      <div v-else class="space-y-6">
        <p class="max-w-prose text-fg-muted">
          A token is how a CI job, the CLI or a teammate signs in to this server; people can also sign in with their account at the company's provider. Give each the least role that does the job.
          <template v-if="access.token.value">
            You are signed in as <span class="mono text-fg">{{ access.token.value.name }}</span>.
          </template>
        </p>

        <UiTabs :tabs="tabs" label="Sections of Access" />

        <NuxtPage />
      </div>
    </PageBody>
  </div>
</template>
