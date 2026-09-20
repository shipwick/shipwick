/**
 * One shared clock for every relative timestamp on screen ("2m ago"), so they
 * all tick together and a page full of them costs a single timer.
 */
const now = ref(Date.now())
let started = false

function start() {
  if (started || !import.meta.client) return
  started = true
  setInterval(() => {
    if (!document.hidden) now.value = Date.now()
  }, 1000)
  document.addEventListener('visibilitychange', () => {
    if (!document.hidden) now.value = Date.now()
  })
}

export function useNow(): Readonly<Ref<number>> {
  start()
  return now
}
