import App from "@/App.vue";
import "@/assets/style.css";
import router from "@/router";
import i18n from "@/locales";
import { createApp } from "vue";

createApp(App).use(router).use(i18n).mount("#app");
