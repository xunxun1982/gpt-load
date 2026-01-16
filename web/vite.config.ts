import vue from "@vitejs/plugin-vue";
import path from "path";
import { defineConfig, loadEnv } from "vite";
import { compression } from "vite-plugin-compression2";

/**
 * Vite Configuration
 * Configures dev server, build optimizations, and code splitting strategies
 * @see https://vite.dev/config/
 */
export default defineConfig(({ mode }) => {
  // Load environment variables
  const env = loadEnv(mode, path.resolve(__dirname, "../"), "");
  const isProd = mode === "production";

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
    // Production build: remove console and debugger statements
    esbuild: isProd
      ? {
          drop: ["console", "debugger"],
          legalComments: "none",
        }
      : undefined,
    // Build configuration
    build: {
      // Target modern browsers for smaller bundle size
      target: "esnext",
      outDir: "dist",
      assetsDir: "assets",
      // Use esbuild for faster minification (default in Vite)
      minify: "esbuild",
      // Enable CSS minification and code splitting
      cssMinify: true,
      cssCodeSplit: true,
      // Disable sourcemap in production for smaller output
      sourcemap: false,
      // Skip compressed size reporting for faster builds
      reportCompressedSize: false,
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
          // Optimize chunk file names for better caching
          chunkFileNames: "assets/[name]-[hash].js",
          entryFileNames: "assets/[name]-[hash].js",
          assetFileNames: "assets/[name]-[hash].[ext]",
        },
        // Tree-shaking optimization
        treeshake: {
          moduleSideEffects: false,
          propertyReadSideEffects: false,
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
