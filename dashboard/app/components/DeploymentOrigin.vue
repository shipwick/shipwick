<script setup lang="ts">
import type { Deployment } from '~/types/api'
import { deploymentOrigin } from '~/utils/deployments'

/**
 * "rollback to #3 1.4.0" / "redeploy of #6": where a deployment's
 * configuration came from, linked to that deployment. Renders nothing for a
 * plain deploy.
 */
const props = defineProps<{
  deployment: Pick<Deployment, 'kind' | 'source_deployment_id'>
  /** Deployments already at hand, to resolve the source's number and version without a request. */
  known: readonly Pick<Deployment, 'id' | 'sequence' | 'version'>[]
}>()

const origin = computed(() => deploymentOrigin(props.deployment, props.known))
</script>

<template>
  <span v-if="origin" class="whitespace-nowrap text-fg-muted">
    <template v-if="origin.source">
      {{ origin.phrase }}
      <NuxtLink :to="`/deployments/${origin.source.id}`" class="mono link">#{{ origin.source.sequence }}</NuxtLink>
      <span class="mono ml-1.5 text-fg-subtle">{{ origin.source.version }}</span>
    </template>
    <template v-else>
      {{ origin.kind }}
      <NuxtLink v-if="origin.sourceId !== null" :to="`/deployments/${origin.sourceId}`" class="link ml-1 text-xs">
        source
      </NuxtLink>
    </template>
  </span>
</template>
