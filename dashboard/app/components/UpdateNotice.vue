<script setup lang="ts">
import type { Server } from '~/types/api'
import { UPGRADE_COMMAND, UPGRADE_GUIDE_URL, updateNotice } from '~/utils/updates'

/**
 * A newer release of Shipwick, as this server's agent heard of it: what it
 * is, and what to do on the server. A notice on the page, never a dialog; and
 * nothing at all while there is none. The dashboard itself asks nobody.
 */
const props = defineProps<{ server: Server }>()

const notice = computed(() => updateNotice(props.server))
</script>

<template>
  <section v-if="notice" id="update" aria-label="A newer release" class="rounded-sm border border-line-strong bg-subtle px-4 py-3">
    <p class="flex flex-wrap items-center gap-x-2 gap-y-1">
      <UiIcon name="download" :size="14" class="text-fg-muted" />
      <span class="font-medium">Shipwick <span class="mono">{{ notice.latest }}</span> is available.</span>
      <span class="text-fg-muted">This server runs <span class="mono text-fg">{{ notice.running }}</span>.</span>
    </p>
    <p class="mt-2 text-fg-muted">
      On the server, run the installer again. Applications keep running while the agent restarts:
    </p>
    <code class="command mt-1.5">{{ UPGRADE_COMMAND }}</code>
    <p class="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-fg-muted">
      <span>A server installed from a package is upgraded by installing the newer package.</span>
      <a :href="UPGRADE_GUIDE_URL" target="_blank" rel="noopener noreferrer" class="link inline-flex items-center gap-1">How to upgrade<UiIcon name="external" :size="12" /><span class="sr-only">(opens in a new tab)</span></a>
      <a :href="notice.url" target="_blank" rel="noopener noreferrer" class="link inline-flex items-center gap-1">What is in {{ notice.latest }}<UiIcon name="external" :size="12" /><span class="sr-only">(opens in a new tab)</span></a>
    </p>
  </section>
</template>
