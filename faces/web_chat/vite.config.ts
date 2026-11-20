import path from 'node:path'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// `npm run dev` puts the page in front of whatever answers its /api: the
// face's own showcase by default, a lab with API_URL.
const api = process.env.API_URL ?? 'http://localhost:8080'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { '@': path.resolve(import.meta.dirname, './src') } },
  server: {
    proxy: { '/api': { target: api, changeOrigin: true, secure: false } },
  },
})
