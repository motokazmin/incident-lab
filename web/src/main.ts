import "./style.css";
import * as api from "./api";
import { Chart, type Marker } from "./chart";
import { Debrief } from "./debrief";
import { h, inline, markdown } from "./dom";
import type { Editor } from "./editor";

const app = document.getElementById("app")!;
const labFromHash = () => new URLSearchParams(location.hash.slice(1)).get("lab");

const STATE: Record<api.LabView["state"], string> = {
  booting: "Запускается", healthy: "Всё спокойно", alert: "Идёт инцидент",
  verifying: "Проверка", resolved: "Инцидент закрыт", ended: "Стенд остановлен",
};

const retry = (v: api.LabView) => ({ title: "Проверка не пройдена", text: `Что именно не так, написано ниже. ${v.fixHow.join(" ")} Затем запусти проверку снова.` });

/** Подсказка «что делать сейчас». Единственное место, где описан ход задачи по состояниям. */
function nowHint(v: api.LabView): { title: string; text: string } {
  const failed = v.report && !v.report.passed;
  switch (v.state) {
    case "booting": return { title: "Запускаем стенд", text: "Поднимаем учебный сервер и сервис. Это несколько секунд." };
    case "verifying": return { title: "Идёт проверка", text: "Нагружаем сервис и сверяем данные. Это может занять пару минут." };
    case "resolved": return { title: "Инцидент закрыт", text: "Починка выдержала проверку. Разбор открыт справа, во вкладке «Разбор полётов» над редактором." };
    case "ended": return { title: "Стенд остановлен", text: "Он закрывается, если долго нет активности. Вернись к списку задач и начни заново." };
    case "alert": return failed
      ? retry(v)
      : { title: "Идёт инцидент", text: v.alertText };
    case "healthy": return failed
      ? retry(v)
      : { title: "Пока всё спокойно", text: "Клиенты получают ответы. Осмотрись: скоро начнётся инцидент, и графики покажут его первыми." };
  }
}

async function landing(error = "") {
  const list = await api.scenarios();
  // Пока стенд создаётся, кнопки закрыты: повторный клик поднял бы второй стенд.
  const start = async (id: string) => {
    document.querySelectorAll<HTMLButtonElement>(".card button").forEach((b) => { b.disabled = true; });
    try {
      const v = await api.startLab(id);
      location.hash = `lab=${v.id}`;
    } catch (e) {
      void landing((e as Error).message);
    }
  };
  app.replaceChildren(h("main", { class: "landing" },
    h("h1", {}, "Incident Lab"),
    h("p", { class: "lead" }, "Тренажёр для бэкенд-разработчиков. Ты становишься дежурным: настоящий сервис ломается на глазах, а тебе нужно найти причину и устранить её. Всё работает в браузере, ставить ничего не нужно."),
    h("ol", { class: "how" },
      h("li", {}, "Мы запускаем для тебя сервис и клиентов, которые шлют запросы."),
      h("li", {}, "Через пару минут начинается сбой, как на настоящем дежурстве."),
      h("li", {}, "В терминале и редакторе кода ты находишь причину и исправляешь её."),
      h("li", {}, "Проверка нагружает сервис: если починка настоящая, откроется разбор.")),
    error ? h("p", { class: "error", role: "alert" }, error) : null,
    h("h2", { class: "section" }, "Выбери задачу"),
    ...list.map((s) => h("article", { class: "card" },
      h("div", {}, h("h3", {}, s.title), h("p", {}, s.summary), h("p", { class: "muted" }, s.stack)),
      h("button", { class: "primary", click: () => void start(s.id) }, "Начать"))),
  ));
}

class LabScreen {
  private readonly badge = h("span", { class: "badge" });
  private readonly verifyBtn = h("button", {
    class: "primary", title: "Нагружает сервис и сверяет данные. Если починка настоящая, инцидент закроется.",
    click: () => void this.verify(),
  }, "Проверить починку");
  private readonly now = h("section", { class: "now", "aria-live": "polite" });
  private nowKey = "";
  private readonly notice = h("p", { class: "error", role: "alert", hidden: true });
  private readonly side = h("div", { class: "side-body" });
  private readonly report = h("div", {});
  private readonly events = h("ol", { class: "events" });
  private readonly debrief = new Debrief();
  private editor?: Editor;
  private showDebrief = false; // проверка прошла до того, как открылся редактор
  private readonly hintList = h("div", { class: "hint-list" });
  private readonly hintBtn = h("button", { class: "ghost", click: () => void this.openHint() });
  private readonly hintsDone = h("div", { class: "debrief-link", hidden: true },
    h("p", { class: "muted" }, "Подсказки кончились, открыт разбор полётов. Почини по нему и проверь: так путь запомнится."),
    h("button", { class: "ghost", click: () => this.editor?.show(true) }, "Открыть разбор полётов"));
  private readonly hintBox = h("section", { class: "hints", hidden: true },
    h("h2", { class: "section" }, "Подсказки"),
    h("p", { class: "muted" }, "Застрял? Открывай по одной: от того, куда смотреть, до готовой правки."),
    this.hintList, this.hintBtn, this.hintsDone);
  private readonly errors = new Chart("Ошибки", "Доля запросов клиентов, закончившихся ошибкой", (s) => (s.sent ? (s.failed / s.sent) * 100 : undefined), (v) => `${v.toFixed(0)}%`, 100);
  private readonly latency = new Chart("Время ответа", "95% запросов укладываются в это время (p95)", (s) => (s.sent ? s.p95ms : undefined), (v) => `${Math.round(v)} мс`);
  private lag?: Chart;
  private rows: api.Sample[] = [];
  private last = 0;
  private timer = 0;
  private workspace = false;
  private hintsOpen = 0;
  private markers: Marker[] = [];

  constructor(private readonly id: string) {}

  async run() {
    const v = await api.getLab(this.id);
    if (v.lagTitle) {
      this.lag = new Chart(v.lagTitle, "Сколько ждёт самая старая задача фоновой работы", (s) => (s.lagms === undefined ? undefined : s.lagms / 1000), (x) => `${Math.round(x)} с`);
    }
    this.side.append(
      this.now, this.report, this.hintBox,
      h("section", {}, h("h2", { class: "section" }, "Задача"), markdown(v.briefing)),
      h("section", {}, h("h2", { class: "section" }, "Как пройти"), h("ol", { class: "how" },
        h("li", {}, "Найди причину: смотри графики и логи."),
        ...v.fixHow.map((step) => h("li", {}, ...inline(step))),
        h("li", {}, "Нажми «Проверить починку» вверху справа."))),
      h("section", { class: "stack" }, h("h2", { class: "section" }, "Состояние сервиса"),
        h("p", { class: "muted" }, "Клиентов имитируем мы: они шлют запросы, пока работает стенд. Графики обновляются каждую секунду."),
        this.errors.el, this.latency.el, ...(this.lag ? [this.lag.el] : [])),
      h("section", {}, h("h2", { class: "section" }, "Журнал"), this.events),
    );
    app.replaceChildren(
      h("header", { class: "top" },
        h("strong", {}, v.title), this.badge,
        h("span", { class: "spacer" }), this.notice, this.verifyBtn,
        h("button", { class: "ghost", title: "Останавливает стенд и возвращает к списку задач", click: () => void this.stop() }, "Завершить дежурство")),
      h("div", { class: "layout" },
        h("aside", { class: "side" }, this.side),
        h("div", { class: "work", id: "work" }, h("p", { class: "booting" }, "Поднимаем виртуалку и сервис…"))),
    );
    await this.tick();
    this.timer = window.setInterval(() => void this.tick(), 1000);
  }

  private async tick() {
    try {
      const v = await api.getLab(this.id);
      this.render(v);
      if (!this.workspace && v.state !== "booting" && v.state !== "ended") await this.openWorkspace();
      const fresh = await api.metrics(this.id, this.last);
      if (fresh.length) {
        this.rows = [...this.rows, ...fresh].slice(-400);
        this.last = fresh[fresh.length - 1].t;
      }
      const now = Math.floor(Date.now() / 1000) - 1;
      this.errors.draw(this.rows, now, this.markers);
      this.latency.draw(this.rows, now, this.markers);
      this.lag?.draw(this.rows, now, this.markers);
    } catch (e) {
      clearInterval(this.timer);
      this.show((e as Error).message);
    }
  }

  private async openWorkspace() {
    this.workspace = true;
    const [{ Editor }, { Shell }] = await Promise.all([import("./editor"), import("./terminal")]);
    const editor = new Editor(this.id, this.debrief);
    const shell = new Shell(this.id);
    document.getElementById("work")!.replaceChildren(editor.el, shell.el);
    await editor.mount();
    shell.mount();
    this.editor = editor;
    if (this.showDebrief) editor.show(true);
  }

  private render(v: api.LabView) {
    this.badge.textContent = STATE[v.state];
    this.badge.dataset.state = v.state;
    this.verifyBtn.disabled = v.state === "booting" || v.state === "verifying" || v.state === "ended";
    this.verifyBtn.textContent = v.state === "verifying" ? "Проверяем…" : v.state === "resolved" ? "Проверить ещё раз" : "Проверить починку";

    const hint = nowHint(v);
    if (hint.title + hint.text !== this.nowKey) {
      this.nowKey = hint.title + hint.text;
      this.now.dataset.state = v.state;
      this.now.replaceChildren(h("h2", {}, "Сейчас: " + hint.title.toLowerCase()), markdown(hint.text));
    }

    this.markers = v.events.filter((e) => e.kind === "alert")
      .map((e) => ({ t: Math.floor(new Date(e.at).getTime() / 1000), label: "алерт" }));

    this.events.replaceChildren(...v.events.slice().reverse().map((e) => h("li", {},
      h("time", {}, new Date(e.at).toLocaleTimeString("ru-RU")), e.text)));

    if (v.report) {
      this.report.replaceChildren(h("section", { class: "report" },
        h("h3", {}, v.report.passed ? "Проверка пройдена" : "Проверка не пройдена"),
        v.report.passed && v.debriefEarly ? h("p", { class: "early" }, "Разбор был открыт до успешной проверки") : null,
        h("ul", {}, ...v.report.items.map((i) => h("li", {},
          h("span", { class: i.pass ? "ok" : "bad" }, i.pass ? "✓ " : "✕ "),
          h("span", {}, i.name), h("small", { class: "muted" }, i.detail),
          i.hint ? h("p", { class: "check-hint" }, ...inline(i.hint)) : null))),
        v.report.passed ? h("button", { class: "ghost", click: () => this.editor?.show(true) }, "Открыть разбор полётов") : null));
    }
    if (this.debrief.update(v)) {
      this.showDebrief = true;
      this.editor?.show(true);
    }
    this.renderHints(v);
    if (v.state === "ended") {
      clearInterval(this.timer);
      const fail = v.events.find((e) => e.kind === "error");
      this.show(fail ? "Стенд не запустился, попробуй ещё раз" : "Стенд остановлен");
    }
  }

  private renderHints(v: api.LabView) {
    this.hintBox.hidden = v.hintsTotal === 0;
    for (const hint of v.hints.slice(this.hintList.childElementCount)) {
      const n = this.hintList.childElementCount + 1;
      this.hintList.append(h("article", {}, h("h3", {}, `${n}. ${hint.title}`), markdown(hint.text)));
    }
    this.hintsOpen = v.hints.length;
    this.hintsDone.hidden = !v.debriefEarly || v.state === "resolved";
    const locked = v.state === "booting" || v.state === "resolved" || v.state === "ended";
    this.hintBtn.hidden = locked || v.hints.length >= v.hintsTotal;
    const waiting = v.state !== "alert" && !v.report;
    this.hintBtn.disabled = waiting;
    this.hintBtn.textContent = waiting
      ? "Откроются, когда начнётся сбой"
      : `Показать подсказку ${v.hints.length + 1} из ${v.hintsTotal}`;
  }

  private async openHint() {
    try {
      this.renderHints(await api.openHint(this.id, this.hintsOpen + 1));
    } catch (e) {
      this.show((e as Error).message);
    }
  }

  private show(text: string) {
    this.notice.textContent = text;
    this.notice.hidden = false;
  }

  private async verify() {
    try {
      await api.verify(this.id);
    } catch (e) {
      this.show((e as Error).message);
    }
  }

  private async stop() {
    clearInterval(this.timer);
    await api.stopLab(this.id).catch(() => undefined);
    location.hash = "";
  }
}

function route() {
  const id = labFromHash();
  if (id) void new LabScreen(id).run().catch(() => { location.hash = ""; });
  else void landing();
}

window.addEventListener("hashchange", () => location.reload());
route();
