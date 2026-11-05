import "@xterm/xterm/css/xterm.css";
import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import { h } from "./dom";

/** Терминал внутри песочницы. Вывод — данные пользователя: xterm рисует его как текст. */
export class Shell {
  private readonly host = h("div", { class: "term-host" });
  readonly el = h("section", { class: "pane terminal" },
    h("header", { class: "pane-head" }, h("h2", { class: "pane-title" }, "Терминал"),
      h("span", { class: "muted" }, "Команды выполняются на учебном сервере")),
    this.host);
  private readonly term = new Terminal({
    fontSize: 13, cursorBlink: true, scrollback: 2000,
    theme: { background: "#111110", foreground: "#e8e7e3" },
  });
  private readonly fit = new FitAddon();
  private ws?: WebSocket;

  constructor(private readonly lab: string) {}

  mount() {
    this.term.loadAddon(this.fit);
    this.term.open(this.host);
    this.fit.fit();
    new ResizeObserver(() => { this.fit.fit(); this.sendSize(); }).observe(this.host);
    this.term.onData((d) => {
      if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(new TextEncoder().encode(d));
    });
    this.connect();
  }

  private sendSize() {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify({ cols: this.term.cols, rows: this.term.rows }));
    }
  }

  private connect() {
    const proto = location.protocol === "https:" ? "wss" : "ws";
    const ws = new WebSocket(`${proto}://${location.host}/api/labs/${this.lab}/term`);
    ws.binaryType = "arraybuffer";
    ws.onopen = () => { this.sendSize(); this.term.focus(); };
    ws.onmessage = (e) => this.term.write(new Uint8Array(e.data as ArrayBuffer));
    ws.onclose = () => this.term.write("\r\n\x1b[2m[соединение закрыто]\x1b[0m\r\n");
    this.ws = ws;
  }
}
