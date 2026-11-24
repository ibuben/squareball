import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// Сборка кладётся в internal/web/dist и встраивается в Go-бинарь через go:embed.
export default defineConfig({
  plugins: [vue()],
  build: { outDir: '../internal/web/dist', emptyOutDir: true, assetsDir: 'assets' },
  server: { proxy: { '/api': 'http://127.0.0.1:8080' } },
})
