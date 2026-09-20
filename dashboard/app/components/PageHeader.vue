<script setup lang="ts">
export interface Crumb {
  label: string
  to?: string
  /** Identifiers (application names, deployment numbers) are set in monospace. */
  mono?: boolean
}

/** Sticky page header: breadcrumb on the left (its last item is the page title), actions on the right. */
const props = defineProps<{ crumbs: Crumb[] }>()
</script>

<template>
  <header class="sticky top-0 z-20 border-b border-line bg-bg">
    <div class="mx-auto flex min-h-12 max-w-[1240px] flex-wrap items-center justify-between gap-x-4 gap-y-2 px-4 py-2 sm:px-6">
      <nav aria-label="Breadcrumb" class="min-w-0">
        <ol class="flex min-w-0 items-center gap-1.5 text-base">
          <li v-for="(crumb, index) in props.crumbs" :key="index" class="flex min-w-0 items-center gap-1.5">
            <UiIcon v-if="index > 0" name="chevron-right" :size="12" class="text-fg-faint" />
            <NuxtLink
              v-if="crumb.to && index < props.crumbs.length - 1"
              :to="crumb.to"
              class="truncate text-fg-muted hover:text-fg"
              :class="crumb.mono ? 'mono' : ''"
            >
              {{ crumb.label }}
            </NuxtLink>
            <h1 v-else class="truncate font-semibold" :class="crumb.mono ? 'mono' : ''" aria-current="page">
              {{ crumb.label }}
            </h1>
          </li>
        </ol>
      </nav>
      <div v-if="$slots.actions" class="flex flex-wrap items-center gap-2">
        <slot name="actions" />
      </div>
    </div>
  </header>
</template>
