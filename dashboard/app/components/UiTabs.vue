<script setup lang="ts">
import type { Tab } from '~/utils/tabs'
import { activeTab } from '~/utils/tabs'

/**
 * The sections of a page as links: each tab is an address, so the browser's
 * back button, a bookmark and "open in new tab" all work. On a narrow screen
 * the row scrolls sideways and the current tab is brought into view.
 */
const props = defineProps<{ tabs: Tab[], label: string }>()

const route = useRoute()
const current = computed(() => activeTab(props.tabs, route.path)?.key ?? null)

const list = ref<HTMLElement | null>(null)

function reveal() {
  list.value?.querySelector<HTMLElement>('[aria-current="page"]')?.scrollIntoView({ block: 'nearest', inline: 'nearest' })
}

watch(current, () => nextTick(reveal))
onMounted(reveal)

const BADGE = { warn: 'border-warn-line bg-warn-bg text-warn', danger: 'border-danger-line bg-danger-bg text-danger', muted: 'border-line bg-subtle text-fg-muted' } as const
</script>

<template>
  <nav :aria-label="props.label" class="tabs border-b border-line">
    <ul ref="list" class="-mb-px flex gap-1 overflow-x-auto">
      <li v-for="tab in props.tabs" :key="tab.key" class="shrink-0">
        <NuxtLink
          :to="tab.to"
          class="flex h-9 items-center gap-1.5 whitespace-nowrap border-b-2 px-2.5 text-sm transition-colors duration-100"
          :class="current === tab.key ? 'border-fg font-medium text-fg' : 'border-transparent text-fg-muted hover:border-line-strong hover:text-fg'"
          :aria-current="current === tab.key ? 'page' : undefined"
        >
          {{ tab.label }}
          <span
            v-if="tab.badge !== undefined && tab.badge !== null && tab.badge !== ''"
            class="mono inline-flex h-4 min-w-4 items-center justify-center rounded-full border px-1 text-2xs font-medium"
            :class="BADGE[tab.badgeTone ?? 'muted']"
          >{{ tab.badge }}</span>
        </NuxtLink>
      </li>
    </ul>
  </nav>
</template>
