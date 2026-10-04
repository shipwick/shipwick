<script setup lang="ts">
import { focusWasLost } from '~/utils/focus'

/**
 * Modal dialog on the native <dialog> element. showModal() gives us: focus
 * moved inside, everything else inert, Esc to close, top-layer stacking. Mark
 * the field that should receive focus with `autofocus`; a dialog without one
 * starts at its title. Two things the element leaves open are closed here:
 * Tab wraps from the last control to the first instead of leaving for the
 * browser's own, and when the control that opened the dialog is gone by the
 * time it closes (a row that was removed), the page's content takes the focus
 * instead of nothing.
 */
const props = withDefaults(defineProps<{
  open: boolean
  title: string
  /** While true the dialog cannot be dismissed (a request is in flight). */
  busy?: boolean
  size?: 'sm' | 'md'
}>(), { busy: false, size: 'sm' })

const emit = defineEmits<{ close: [] }>()

const dialog = ref<HTMLDialogElement | null>(null)
const title = ref<HTMLElement | null>(null)
const titleId = useId()
const trap = useFocusTrap(dialog)
let opener: Element | null = null

function sync(open: boolean) {
  const el = dialog.value
  if (!el) return
  if (open && !el.open) {
    opener = document.activeElement
    el.showModal()
    // Left to itself the browser picks the first control, the close button:
    // reading would start there, and Enter would close what was just opened.
    if (!el.querySelector('[autofocus]')) title.value?.focus()
  }
  else if (!open && el.open) {
    el.close()
    // After the parent's own update: what the dialog was about may be removed by it.
    void nextTick(() => restoreFocus(opener))
  }
}

watch(() => props.open, open => sync(open), { flush: 'post' })
onMounted(() => sync(props.open))

// A dialog that changes inside — a second step, a row that is removed — can
// take away the control that had the focus. It is then given to the new
// step's `autofocus` field, or to the title.
onUpdated(() => {
  const el = dialog.value
  if (!el?.open || !focusWasLost(document.activeElement)) return
  const field = el.querySelector<HTMLElement>('[autofocus]')
  if (field) field.focus()
  else title.value?.focus()
})

function requestClose() {
  if (!props.busy) emit('close')
}

// Esc: the browser would close the dialog by itself; route it through the parent's state instead.
// An Esc that has just closed a tooltip inside was the tooltip's, and the dialog stays.
function onCancel(event: Event) {
  event.preventDefault()
  if (!tooltipTookEscape()) requestClose()
}

// The dialog element itself is only reachable by pointer where the backdrop is.
function onPointerDown(event: MouseEvent) {
  if (event.target === dialog.value) requestClose()
}
</script>

<template>
  <dialog
    ref="dialog"
    :aria-labelledby="titleId"
    class="m-auto w-[calc(100vw-2rem)] rounded-sm border border-line-strong bg-bg p-0 text-fg shadow-dialog backdrop:bg-overlay"
    :class="props.size === 'md' ? 'max-w-[36rem]' : 'max-w-[27rem]'"
    @cancel="onCancel"
    @mousedown="onPointerDown"
    @keydown="trap.onKeydown"
  >
    <div v-if="props.open" class="flex max-h-[calc(100dvh-4rem)] flex-col">
      <header class="flex items-start justify-between gap-4 border-b border-line px-5 py-3.5">
        <h2 :id="titleId" ref="title" tabindex="-1" class="text-base font-semibold outline-none">
          {{ props.title }}
        </h2>
        <button
          type="button"
          class="target -mr-1.5 -mt-0.5 flex items-center justify-center rounded-sm p-1.5 text-fg-subtle hover:bg-hover hover:text-fg"
          aria-label="Close"
          :disabled="props.busy"
          @click="requestClose"
        >
          <UiIcon name="close" :size="14" />
        </button>
      </header>
      <div class="overflow-y-auto px-5 py-4">
        <slot />
      </div>
      <footer v-if="$slots.footer" class="flex flex-wrap items-center justify-end gap-2 border-t border-line bg-subtle px-5 py-3">
        <slot name="footer" />
      </footer>
    </div>
  </dialog>
</template>
