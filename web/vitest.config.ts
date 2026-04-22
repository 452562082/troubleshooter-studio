/// <reference types="vitest" />
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// vitest 复用 vite 的 resolver / plugin,这里单独一份 config 避免污染 vite.config.ts。
// happy-dom 替代 jsdom(轻得多,Vue 组件测试完全够用)。
export default defineConfig({
  plugins: [vue()],
  test: {
    environment: 'happy-dom',
    globals: false,
    include: ['src/**/*.{test,spec}.ts'],
  },
})
