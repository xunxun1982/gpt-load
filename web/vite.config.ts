import vue from "@vitejs/plugin-vue";
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
  const isProd = mode === "production";

  return {
    plugins: [vue()],
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
      // Target modern browsers for smaller bundle size
      target: "esnext",
      outDir: "dist",
      assetsDir: "assets",
      // Use Vite 8's default Oxc minifier.
      minify: "oxc",
      // Enable CSS minification and code splitting
      cssMinify: true,
      cssCodeSplit: true,
      // Disable sourcemap in production for smaller output
      sourcemap: false,
      // Report compressed size for better size visibility (set to false for faster builds)
      reportCompressedSize: true,
      // Emit dependency license metadata without generating Markdown artifacts.
      license: {
        fileName: ".vite/license.json",
      },
      rolldownOptions: {
        output: {
          /**
           * Manual chunk configuration - Optimize caching and loading performance.
           * - vue-vendor: Vue core libraries, stable and suitable for long-term caching
           * - naive-ui: Large UI library, updated independently
           * - vendor: Utility dependencies, shared functionality
           */
          codeSplitting: {
            groups: [
              {
                name: "vue-vendor",
                test: /node_modules[\\/](vue|vue-router|vue-i18n)[\\/]/,
                priority: 3,
              },
              {
                name: "naive-ui",
                test: /node_modules[\\/]naive-ui[\\/]/,
                priority: 2,
              },
              {
                name: "vendor",
                test: /node_modules[\\/](@vicons[\\/]ionicons5|axios)[\\/]/,
                priority: 1,
              },
            ],
          },
          minify: isProd
            ? {
                compress: {
                  dropConsole: true,
                  dropDebugger: true,
                },
              }
            : undefined,
          comments: {
            legal: true,
            annotation: false,
            jsdoc: false,
          },
          // Optimize chunk file names for better caching
          chunkFileNames: "assets/[name]-[hash].js",
          entryFileNames: "assets/[name]-[hash].js",
          assetFileNames: "assets/[name]-[hash][extname]",
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
