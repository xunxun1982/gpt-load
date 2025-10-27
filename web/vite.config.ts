import vue from "@vitejs/plugin-vue";
import path from "path";
import { defineConfig, loadEnv } from "vite";

// https://vite.dev/config/
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
          manualChunks: {
            // 使用对象形式避免循环依赖问题
            "vue-vendor": ["vue", "vue-router", "vue-i18n"],
            "naive-ui": ["naive-ui"],
            vendor: ["axios", "@vueuse/core", "@vicons/ionicons5"],
          },
        },
      },
      // 提高 chunk 大小警告限制，因为 Naive UI 本身就很大
      chunkSizeWarningLimit: 1400,
    },
  };
});
