import { defineNuxtConfig } from 'nuxt/config'
import process from 'node:process'
import type { IncomingMessage, Server } from 'node:http'
import type { Duplex } from 'node:stream'
import type { Buffer } from 'node:buffer'
import { apiProxyTarget } from './api-proxy'

const apiUrl = process.env.NUXT_API_URL
let devServer: Server | undefined

export default defineNuxtConfig({
  ssr: false,
  sourcemap: false,
  devtools: { enabled: false },
  devServer: { port: 8082, host: '0.0.0.0' },
  colorMode: { preference: 'dark', fallback: 'dark', classSuffix: '' },
  icon: {
    provider: 'none',
    fallbackToApi: false,
    clientBundle: {
      scan: { globInclude: ['app/**/*.{vue,ts}', 'node_modules/@nuxt/ui/dist/shared/ui*.mjs'] },
    },
  },
  runtimeConfig: { public: { domain: '', version: '1.0.0' } },
  app: {
    buildAssetsDir: 'assets',
    head: {
      meta: [
        { charset: 'utf-8' },
        { name: 'viewport', content: 'width=device-width, initial-scale=1.0, viewport-fit=cover' },
        { name: 'theme-color', content: '#000000' },
      ],
      link: [{ rel: 'icon', type: 'image/png', href: '/favicon.png' }],
    },
    pageTransition: { name: 'page', mode: 'out-in' },
  },
  css: ['~/assets/css/tailwind.css'],
  modules: ['@nuxt/ui', '@pinia/nuxt', '@nuxt/eslint'],
  hooks: {
    listen(server) {
      devServer = server
    },
    'vite:extendConfig'(config, { isClient }) {
      if (apiUrl && isClient && config.server && devServer) {
        config.server.middlewareMode = { server: devServer }
      }
    },
    ready(nuxt) {
      const server = nuxt.server
      if (apiUrl && nuxt.options.dev && server && 'upgrade' in server) {
        const upgrade = server.upgrade.bind(server)
        // Vite owns API upgrades; Nitro's devProxy only forwards HTTP requests.
        server.upgrade = (request: IncomingMessage, socket: Duplex, head: Buffer) => {
          if (!request.url?.startsWith('/api/')) {
            return upgrade(request, socket, head)
          }
        }
      }
    },
  },
  nitro: {
    output: { publicDir: new URL('./exported', import.meta.url).pathname },
  },
  vite: {
    server: {
      allowedHosts: true,
      ...(apiUrl
        ? { proxy: { '/api/': { target: apiProxyTarget(apiUrl), changeOrigin: false, ws: true } } }
        : {}),
    },
  },
  telemetry: false,
  compatibilityDate: '2025-08-04',
  experimental: {
    checkOutdatedBuildInterval: 1000 * 60 * 60,
    defaults: { nuxtLink: { prefetchOn: { interaction: true, visibility: false } } },
  },
  typescript: { typeCheck: true },
})
