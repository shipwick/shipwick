import tailwindcss from '@tailwindcss/vite'

// Applied before first paint so a stored theme never flashes the wrong colors.
// Without a stored choice the CSS follows prefers-color-scheme on its own.
const themeBootScript = `(function(){try{var t=localStorage.getItem('shipwick-theme');if(t==='light'||t==='dark')document.documentElement.setAttribute('data-theme',t)}catch(e){}})()`

const securityHeaders = {
  'x-content-type-options': 'nosniff',
  'x-frame-options': 'DENY',
  'referrer-policy': 'no-referrer',
  'cross-origin-opener-policy': 'same-origin',
  'permissions-policy': 'camera=(), microphone=(), geolocation=(), interest-cohort=()',
  // Everything is self-hosted: fonts, scripts, styles. Nothing may be loaded
  // from or sent to another origin. Inline script/style is needed by Nuxt's
  // payload and the theme boot script above.
  'content-security-policy': [
    'default-src \'self\'',
    'script-src \'self\' \'unsafe-inline\'',
    'style-src \'self\' \'unsafe-inline\'',
    'img-src \'self\' data:',
    'font-src \'self\'',
    'connect-src \'self\'',
    'frame-ancestors \'none\'',
    'base-uri \'self\'',
    'form-action \'self\'',
  ].join('; '),
}

export default defineNuxtConfig({
  compatibilityDate: '2026-01-01',

  // A client-rendered app behind a login: there is nothing to index or to
  // render on the server, and every view is live data polled from the agent.
  // Nitro still serves the app, the session routes and the agent proxy.
  ssr: false,

  devtools: { enabled: false },
  telemetry: false,

  css: ['~/assets/css/main.css'],

  vite: {
    plugins: [tailwindcss()],
  },

  runtimeConfig: {
    // Prefer SHIPWICK_AGENT_URL, which is read at runtime by server/utils/agent.ts.
    // NUXT_AGENT_URL also works, through Nuxt's own runtime config mechanism.
    agentUrl: '',
  },

  app: {
    head: {
      htmlAttrs: { lang: 'en' },
      title: 'Shipwick',
      titleTemplate: '%s · Shipwick',
      meta: [
        { name: 'viewport', content: 'width=device-width, initial-scale=1' },
        { name: 'color-scheme', content: 'light dark' },
        { name: 'robots', content: 'noindex, nofollow' },
        { name: 'theme-color', content: '#ffffff', media: '(prefers-color-scheme: light)' },
        { name: 'theme-color', content: '#121212', media: '(prefers-color-scheme: dark)' },
      ],
      link: [{ rel: 'icon', type: 'image/svg+xml', href: '/favicon.svg' }],
      script: [{ innerHTML: themeBootScript, tagPosition: 'head' }],
    },
  },

  typescript: {
    strict: true,
    tsConfig: {
      compilerOptions: {
        noUncheckedIndexedAccess: true,
      },
    },
  },

  devServer: {
    port: 3000,
  },

  $production: {
    routeRules: {
      '/**': { headers: securityHeaders },
    },
  },
})
