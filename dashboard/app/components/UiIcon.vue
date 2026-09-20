<script setup lang="ts">
/**
 * The handful of icons the dashboard needs, drawn on a 16px grid with a 1.5px
 * stroke. Inline SVG: no icon font, no sprite request, colored by currentColor.
 */
const PATHS = {
  'overview': 'M2.5 2.5h4.5V7H2.5zM9 2.5h4.5V7H9zM2.5 9h4.5v4.5H2.5zM9 9h4.5v4.5H9z',
  'applications': 'M8 1.75 13.5 4.75v6.5L8 14.25 2.5 11.25v-6.5zM2.5 4.75 8 7.75l5.5-3M8 7.75v6.5',
  'deployments': 'M8 1.75 14 5 8 8.25 2 5zM2 8l6 3.25L14 8M2 11l6 3.25L14 11',
  'servers': 'M2.5 2.75h11V7h-11zM2.5 9h11v4.25h-11zM5 4.9h.01M5 11.1h.01',
  'logs': 'M2.5 4h11M2.5 8h11M2.5 12h7',
  'sun': 'M8 5.25a2.75 2.75 0 1 0 0 5.5 2.75 2.75 0 0 0 0-5.5zM8 1.5v1.25M8 13.25v1.25M1.5 8h1.25M13.25 8h1.25M3.4 3.4l.9.9M11.7 11.7l.9.9M3.4 12.6l.9-.9M11.7 4.3l.9-.9',
  'moon': 'M13.25 9.6A5.75 5.75 0 0 1 6.4 2.75a5.75 5.75 0 1 0 6.85 6.85z',
  'monitor': 'M2 3h12v8H2zM5.5 13.5h5M8 11v2.5',
  'menu': 'M2.5 4.5h11M2.5 8h11M2.5 11.5h11',
  'close': 'M4 4l8 8M12 4l-8 8',
  'external': 'M6.5 3.5h-3v9h9v-3M9.5 2.5h4v4M13.5 2.5l-6 6',
  'check': 'M3.5 8.5l3 3 6-7',
  'alert': 'M8 2.25 14.25 13H1.75zM8 6.5v3M8 11.4h.01',
  'x-circle': 'M8 2.25a5.75 5.75 0 1 0 0 11.5 5.75 5.75 0 0 0 0-11.5zM6 6l4 4M10 6l-4 4',
  'chevron-right': 'M6 3.5 10.5 8 6 12.5',
  'refresh': 'M13.25 8a5.25 5.25 0 1 1-1.54-3.71M13.25 2.5v2.75H10.5',
  'pause': 'M5.5 3.5v9M10.5 3.5v9',
  'play': 'M4.5 3l8 5-8 5z',
  'trash': 'M3 4.5h10M6.5 4.5v-2h3v2M4.5 4.5l.5 9h6l.5-9',
  'logout': 'M6.5 2.5h-3v11h3M10 5l3 3-3 3M13 8H6.5',
  'search': 'M7 2.5a4.5 4.5 0 1 0 0 9 4.5 4.5 0 0 0 0-9zM10.25 10.25l3.25 3.25',
  'arrow-down': 'M8 3v10M4 9l4 4 4-4',
} as const

export type IconName = keyof typeof PATHS

const props = withDefaults(defineProps<{ name: IconName, size?: number }>(), { size: 16 })
</script>

<template>
  <svg
    :width="props.size"
    :height="props.size"
    viewBox="0 0 16 16"
    fill="none"
    stroke="currentColor"
    stroke-width="1.5"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
    focusable="false"
    class="shrink-0"
  >
    <path :d="PATHS[props.name]" />
  </svg>
</template>
