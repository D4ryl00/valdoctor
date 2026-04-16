import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig(({ mode }) => ({
  plugins: [react()],
  build: {
    outDir: mode === 'embedded' ? '../internal/api/ui/static' : 'dist',
    emptyOutDir: true,
  },
}))
