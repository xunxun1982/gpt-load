import vue from "@vitejs/plugin-vue";
import { compression } from "vite-plugin-compression2";
import path from "path";
import { defineConfig, loadEnv } from "vite";

/**
 * Vite Configuration
 * Configures dev server, build optimizations, and code splitting strategies
 * @see https://vite.dev/config/
 */
export default defineConfig(({ mode }) => {
  // Load environment variables
  const env = loadEnv(mode, path.resolve(__dirname, "../"), "");

  return {
    plugins: [
      vue(),
      // Enable gzip compression for production builds
      compression({
        include: /\.(js|css|html|svg)$/,
        threshold: 10240, // Compress files larger than 10KB
        algorithms: ["gzip"],
      }),
    ],
    // Resolve configuration
    resolve: {
      // Configure path aliases
      alias: {
        "@": path.resolve(__dirname, "./src"),
      },
    },
    // Dev server configuration
    server: {
      // Proxy configuration example
      proxy: {
        "/api": {
          target: env.VITE_API_BASE_URL || "http://127.0.0.1:3001",
          changeOrigin: true,
        },
      },
    },
    // Build configuration
    build: {
      outDir: "dist",
      assetsDir: "assets",
      rollupOptions: {
        output: {
          /**
           * Manual chunk configuration - Optimize caching and loading performance
           * - vue-vendor: Vue core libraries, stable and suitable for long-term caching
           * - naive-ui: Large UI library, updated independently
           * - vendor: Utility dependencies, shared functionality
           * Use object format to avoid circular dependency issues
           */
          manualChunks: {
            "vue-vendor": ["vue", "vue-router", "vue-i18n"],
            "naive-ui": ["naive-ui"],
            vendor: ["axios", "@vueuse/core", "@vicons/ionicons5"],
          },
        },
      },
      /**
       * Chunk size warning limit
       * Set to 1400 KB to accommodate Naive UI library (1332 KB uncompressed, 354 KB gzipped)
       */
      chunkSizeWarningLimit: 1400,
    },
  };
});
