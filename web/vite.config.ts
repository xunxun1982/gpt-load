import vue from "@vitejs/plugin-vue";
import path from "path";
import { defineConfig, loadEnv } from "vite";

/**
 * Vite 配置文件
 * 配置开发服务器、构建优化和代码分割策略
 * @see https://vite.dev/config/
 */
export default defineConfig(({ mode }) => {
  // 加载环境变量
  const env = loadEnv(mode, path.resolve(__dirname, "../"), "");

  return {
    plugins: [vue()],
    // 解析配置
    resolve: {
      // 配置路径别名
      alias: {
        "@": path.resolve(__dirname, "./src"),
      },
    },
    // 开发服务器配置
    server: {
      // 代理配置示例
      proxy: {
        "/api": {
          target: env.VITE_API_BASE_URL || "http://127.0.0.1:3001",
          changeOrigin: true,
        },
      },
    },
    // 构建配置
    build: {
      outDir: "dist",
      assetsDir: "assets",
      rollupOptions: {
        output: {
          /**
           * 手动分块配置 - 优化缓存和加载性能
           * - vue-vendor: Vue 核心库，稳定且适合长期缓存
           * - naive-ui: 大型 UI 库，独立更新
           * - vendor: 工具类依赖，共享功能
           * 使用对象形式避免循环依赖问题
           */
          manualChunks: {
            "vue-vendor": ["vue", "vue-router", "vue-i18n"],
            "naive-ui": ["naive-ui"],
            vendor: ["axios", "@vueuse/core", "@vicons/ionicons5"],
          },
        },
      },
      /**
       * Chunk 大小警告限制
       * 设置为 1400 KB 以适应 Naive UI 库（1332 KB 未压缩，354 KB gzip 后）
       */
      chunkSizeWarningLimit: 1400,
    },
  };
});
