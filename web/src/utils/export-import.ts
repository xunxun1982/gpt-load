import { NButton, type DialogApi } from "naive-ui";
import { h } from "vue";

export type ExportMode = "plain" | "encrypted";
export type ImportMode = "plain" | "encrypted" | "auto";

type ButtonType = "default" | "primary" | "info" | "success" | "warning" | "error";

function askMode<T extends string>(
  dialog: DialogApi,
  t: (key: string) => string,
  opts: {
    titleKey: string;
    contentKey: string;
    actions: Array<{ label: string; value: T; type?: ButtonType }>;
  }
): Promise<T> {
  return new Promise(resolve => {
    const d = dialog.create({
      title: t(opts.titleKey),
      content: t(opts.contentKey),
      showIcon: true,
      closable: false,
      maskClosable: false,
      action: () =>
        h(
          "div",
          { style: "display:flex; gap:8px; justify-content:flex-end; width:100%" },
          opts.actions.map(action =>
            h(
              NButton,
              {
                type: action.type,
                onClick: () => {
                  d.destroy();
                  resolve(action.value);
                },
              },
              { default: () => action.label }
            )
          )
        ),
    });
  });
}

export function askExportMode(dialog: DialogApi, t: (key: string) => string): Promise<ExportMode> {
  return askMode<ExportMode>(dialog, t, {
    titleKey: "export.modeTitle",
    contentKey: "export.modeDesc",
    actions: [
      { label: t("export.encrypted"), value: "encrypted", type: "primary" },
      { label: t("export.plain"), value: "plain" },
    ],
  });
}

export function askImportMode(dialog: DialogApi, t: (key: string) => string): Promise<ImportMode> {
  return askMode<ImportMode>(dialog, t, {
    titleKey: "import.modeTitle",
    contentKey: "import.modeDesc",
    actions: [
      { label: t("import.encrypted"), value: "encrypted", type: "primary" },
      { label: t("import.plain"), value: "plain" },
      { label: t("import.auto"), value: "auto" },
    ],
  });
}
