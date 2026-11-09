import * as monaco from "monaco-editor";
import * as api from "./api";
import type { Debrief } from "./debrief";
import { h } from "./dom";

(self as unknown as { MonacoEnvironment: unknown }).MonacoEnvironment = {
  getWorker: () => new Worker(new URL("../node_modules/monaco-editor/esm/vs/editor/editor.worker.start.js", import.meta.url), { type: "module" }),
};

const language = (p: string) => (p.endsWith(".go") ? "go" : p.endsWith(".sql") ? "sql" : "plaintext");

const IDLE = "Правки в редакторе пока только у тебя в браузере. «Сохранить файл» (Ctrl+S) запишет их на учебный сервер, а svc deploy в терминале соберёт код и перезапустит сервис.";

/** Редактор файлов учебного сервиса и вкладка разбора над ним. */
export class Editor {
  readonly el: HTMLElement;
  private readonly tabs = h("div", { class: "tabs", role: "tablist" });
  private readonly status = h("p", { class: "pane-note muted", role: "status" });
  private readonly host = h("div", { class: "monaco" });
  private readonly save = h("button", {
    class: "ghost", disabled: true, title: "Записывает файл на учебный сервер (Ctrl+S)", click: () => void this.persist(),
  }, "Сохранить файл");
  private readonly codeTab = h("button", {
    class: "view", role: "tab", "aria-selected": "true", click: () => this.show(false),
  }, "Код сервиса");
  private editor?: monaco.editor.IStandaloneCodeEditor;
  private path = "";
  private saved = "";

  constructor(private readonly lab: string, private readonly debrief: Debrief) {
    debrief.tab.addEventListener("click", () => this.show(true));
    this.el = h("section", { class: "pane editor" },
      h("header", { class: "pane-head" }, h("div", { class: "views", role: "tablist" }, this.codeTab, debrief.tab),
        h("span", { class: "sep" }), this.tabs, this.save),
      this.status, this.host, debrief.el);
  }

  /** Переключает панель между кодом и разбором. */
  show(onDebrief: boolean) {
    if (onDebrief && this.debrief.tab.disabled) return;
    this.codeTab.setAttribute("aria-selected", String(!onDebrief));
    this.debrief.tab.setAttribute("aria-selected", String(onDebrief));
    this.debrief.el.hidden = !onDebrief;
    for (const el of [this.tabs, this.save, this.status, this.host]) el.hidden = onDebrief;
    if (onDebrief) this.debrief.seen();
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
