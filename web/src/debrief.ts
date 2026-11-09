import type { LabView } from "./api";
import { h, markdown } from "./dom";

const LOCKED = "Откроется после успешной проверки или когда ты откроешь все подсказки";

/** Вкладка «Разбор полётов»: кнопка над редактором и сам текст. Открывает её сервер (`debrief` в снимке). */
export class Debrief {
  readonly tab = h("button", { class: "view", role: "tab", "aria-selected": "false", disabled: true, title: LOCKED },
    "Разбор полётов", h("span", { class: "lock" }, "после проверки"));
  readonly el = h("div", { class: "debrief-view", hidden: true });
  private readonly banner = h("p", { class: "debrief-banner" });
  private bannerKey = "";
  private solved = false;

  /** Обновляет вкладку по снимку. Возвращает true, когда проверка впервые прошла: пора показать разбор. */
  update(v: LabView): boolean {
    if (!v.debrief) return false;
    if (this.tab.disabled) {
      this.tab.disabled = false;
      this.tab.title = "Что сломалось, как это находить и почему очевидные починки не работают";
      this.tab.querySelector(".lock")?.replaceWith(h("span", { class: "dot-new", title: "Новое" }));
      const body = markdown(v.debrief);
      body.classList.add("long");
      this.el.replaceChildren(this.banner, body);
    }
    this.renderBanner(v);
    const first = !!v.solved && !this.solved;
    this.solved = !!v.solved;
    return first;
  }

  /** Дежурный открыл вкладку: отметка «новое» больше не нужна. */
  seen() { this.tab.querySelector(".dot-new")?.remove(); }

  private renderBanner(v: LabView) {
    const key = `${v.debriefEarly}|${v.solved ?? ""}|${v.hints.length}`;
    if (key === this.bannerKey) return;
    this.bannerKey = key;
    this.banner.dataset.kind = v.debriefEarly ? "early" : "solved";
    if (v.debriefEarly) {
      this.banner.replaceChildren(h("strong", {}, "Разбор открыт до успешной проверки: "), v.solved
        ? "в отчёте проверки это отмечено."
        : "ты открыл все подсказки. Починку всё равно можно проверить, в отчёте будет отметка.");
    } else if (v.solved) {
      const alert = v.events.find((e) => e.kind === "alert")?.at ?? v.created;
      const min = Math.max(1, Math.round((Date.parse(v.solved) - Date.parse(alert)) / 60000));
      this.banner.replaceChildren(h("strong", {}, `Инцидент закрыт за ${min} мин. `),
        v.hintsTotal ? `Подсказок использовано: ${v.hints.length} из ${v.hintsTotal}.` : "");
    }
  }
}
