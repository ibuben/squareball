import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// Сборка кладётся в internal/web/dist и встраивается в Go-бинарь через go:embed.
// base './': адреса ассетов относительные — сервер ставит <base href> под префикс прокси.
export default defineConfig({
  base: './',
  plugins: [vue()],
  build: { outDir: '../internal/web/dist', emptyOutDir: true, assetsDir: 'assets' },
  server: { proxy: { '/api': 'http://127.0.0.1:8080' } },
})
