import * as monaco from "monaco-editor";
import * as api from "./api";
import { h } from "./dom";

(self as unknown as { MonacoEnvironment: unknown }).MonacoEnvironment = {
  getWorker: () => new Worker(new URL("../node_modules/monaco-editor/esm/vs/editor/editor.worker.start.js", import.meta.url), { type: "module" }),
};

const language = (p: string) => (p.endsWith(".go") ? "go" : p.endsWith(".sql") ? "sql" : "plaintext");

const IDLE = "Правка заработает, когда ты сохранишь файл и выполнишь svc deploy в терминале.";

/** Редактор файлов учебного сервиса. */
export class Editor {
  readonly el: HTMLElement;
  private readonly tabs = h("div", { class: "tabs", role: "tablist" });
  private readonly status = h("p", { class: "pane-note muted", role: "status" });
  private readonly host = h("div", { class: "monaco" });
  private readonly save = h("button", {
    class: "ghost", disabled: true, title: "Записывает файл на учебный сервер (Ctrl+S)", click: () => void this.persist(),
  }, "Сохранить файл");
  private editor?: monaco.editor.IStandaloneCodeEditor;
  private path = "";
  private saved = "";

  constructor(private readonly lab: string) {
    this.el = h("section", { class: "pane editor" },
      h("header", { class: "pane-head" }, h("h2", { class: "pane-title" }, "Код сервиса"), this.tabs, this.save),
      this.status, this.host);
  }

  async mount() {
    this.editor = monaco.editor.create(this.host, {
      value: "", theme: "vs-dark", automaticLayout: true, minimap: { enabled: false },
      fontSize: 13, scrollBeyondLastLine: false, tabSize: 4,
    });
    this.editor.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS, () => void this.persist());
    this.editor.onDidChangeModelContent(() => this.mark());
    const list = (await api.files(this.lab)).filter((f) => !/\.(sum|mod)$/.test(f)).sort();
    this.tabs.replaceChildren(...list.map((f) =>
      h("button", { class: "tab", role: "tab", "data-path": f, click: () => void this.open(f) }, f)));
    const first = list.find((f) => f === "main.go") ?? list[0];
    if (first) await this.open(first);
  }

  private async open(path: string) {
    const text = await api.readFile(this.lab, path);
    this.path = path;
    this.saved = text;
    const model = monaco.editor.createModel(text, language(path));
    this.editor?.getModel()?.dispose();
    this.editor?.setModel(model);
    this.tabs.querySelectorAll<HTMLElement>(".tab").forEach((t) => {
      t.setAttribute("aria-selected", String(t.dataset.path === path));
    });
    this.mark();
  }

  private mark() {
    const dirty = (this.editor?.getValue() ?? "") !== this.saved;
    this.save.disabled = !dirty;
    this.status.textContent = dirty ? "Есть несохранённые правки" : IDLE;
  }

  private async persist() {
    const text = this.editor?.getValue() ?? "";
    try {
      await api.writeFile(this.lab, this.path, text);
      this.saved = text;
      this.mark();
      this.status.textContent = "Файл сохранён. Сервис ещё работает на старом коде: выполни svc deploy в терминале.";
    } catch (e) {
      this.status.textContent = (e as Error).message;
    }
  }
}
