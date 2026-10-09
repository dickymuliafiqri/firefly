import { defineConfig } from "vitest/config";

export default defineConfig({
  resolve: {
    alias: {
      "@": "/src",
    },
  },
  test: {
    environment: "jsdom",
    globals: false,
    include: ["src/**/*.test.ts", "src/**/*.test.tsx"],
    css: true,
  },
});
