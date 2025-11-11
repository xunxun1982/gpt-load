import { h } from "vue";
import type { DialogApi } from "naive-ui";
import { NButton } from "naive-ui";

export type ExportMode = "plain" | "encrypted";
export type ImportMode = "plain" | "encrypted" | "auto";

export function askExportMode(dialog: DialogApi, t: (key: string) => string): Promise<ExportMode> {
  return new Promise(resolve => {
    const d = dialog.create({
      title: t("export.modeTitle"),
      content: t("export.modeDesc"),
      showIcon: true,
      closable: true,
      maskClosable: true,
      action: () =>
        h(
          "div",
          { style: "display:flex; gap:8px; justify-content:flex-end; width:100%" },
          [
            h(
              NButton,
              {
                onClick: () => {
                  d.destroy();
                  resolve("encrypted");
                },
                type: "primary",
              },
              { default: () => t("export.encrypted") }
            ),
            h(
              NButton,
              {
                onClick: () => {
                  d.destroy();
                  resolve("plain");
                },
              },
              { default: () => t("export.plain") }
            ),
          ]
        ),
    });
  });
}

export function askImportMode(dialog: DialogApi, t: (key: string) => string): Promise<ImportMode> {
  return new Promise(resolve => {
    const d = dialog.create({
      title: t("import.modeTitle"),
      content: t("import.modeDesc"),
      showIcon: true,
      closable: true,
      maskClosable: true,
      action: () =>
        h(
          "div",
          { style: "display:flex; gap:8px; justify-content:flex-end; width:100%" },
          [
            h(
              NButton,
              {
                onClick: () => {
                  d.destroy();
                  resolve("encrypted");
                },
                type: "primary",
              },
              { default: () => t("import.encrypted") }
            ),
            h(
              NButton,
              {
                onClick: () => {
                  d.destroy();
                  resolve("plain");
                },
              },
              { default: () => t("import.plain") }
            ),
            h(
              NButton,
              {
                onClick: () => {
                  d.destroy();
                  resolve("auto");
                },
              },
              { default: () => t("import.auto") }
            ),
          ]
        ),
    });
  });
}