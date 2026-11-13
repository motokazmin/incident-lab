// Стенд глазами записи: состояние через API сервера, терминал и редактор через DOM страницы.
import { sleep } from "./actor.mjs";

export class Lab {
  constructor(base, page) {
    this.base = base;
    this.page = page;
    this.id = "";
  }

  async api(path, init) {
    const r = await fetch(this.base + path, init);
    if (!r.ok) throw new Error(`${path}: ${r.status} ${(await r.text()).trim()}`);
    return r;
  }

  async scenarios() { return (await this.api("/api/scenarios")).json(); }
  async view() { return (await this.api(`/api/labs/${this.id}`)).json(); }
  async file(path) { return (await this.api(`/api/labs/${this.id}/file?path=${encodeURIComponent(path)}`)).text(); }
  async stop() { if (this.id) await this.api(`/api/labs/${this.id}`, { method: "DELETE" }).catch(() => {}); }

  /** Ждёт, пока снимок стенда удовлетворит условию. */
  async until(cond, { timeout = 600, every = 250, what = "условие" } = {}) {
    const end = Date.now() + timeout * 1000;
    for (;;) {
      const v = await this.view();
      if (cond(v)) return v;
      if (v.state === "ended") throw new Error(`стенд остановлен, пока ждали: ${what}`);
      if (Date.now() > end) throw new Error(`не дождались за ${timeout} с: ${what}`);
      await sleep(every);
    }
  }

  // --- терминал ---

  /** Видимые строки терминала и строка с курсором. */
  async screen() {
    return this.page.evaluate(() => {
      const rows = [...document.querySelectorAll(".xterm-rows > div")].map((d) => d.textContent.replace(/ /g, " ").trimEnd());
      const cursorRow = document.querySelector(".xterm-rows .xterm-cursor")?.closest(".xterm-rows > div");
      const at = cursorRow ? [...cursorRow.parentElement.children].indexOf(cursorRow) : -1;
      return { rows, cursor: at >= 0 ? rows[at] : "" };
    });
  }

  /** Ждёт приглашения оболочки в строке с курсором: команда завершилась. */
  async prompt({ timeout = 120, re = /[#$]$/ } = {}) {
    const end = Date.now() + timeout * 1000;
    for (;;) {
      const s = await this.screen();
      if (re.test(s.cursor) && !/\S\s*[#$]\s+\S/.test(s.cursor)) return s;
      if (Date.now() > end) throw new Error(`терминал не вернул приглашение за ${timeout} с: «${s.cursor}»`);
      await sleep(150);
    }
  }

  async termText() { return (await this.screen()).rows.join("\n"); }
}
