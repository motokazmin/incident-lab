// Шаги ролика по сюжету pool-exhausted: блоки 3–9 сценария записи. Механика — в lib/.
// Каждый блок — отдельный файл ролика; ожидания режет skip(), неподвижный хвост добавляет record.mjs.

import { focusTerminal, openScenario, run, SIDE, verify } from "../lib/steps.mjs";

const PSQL = `psql -U postgres -d shop -c "select state, count(*) from pg_stat_activity where datname='shop' group by 1"`;
const FILE = "orders.go";
const FIX = "defer tx.Rollback()";

export const blocks = [
  {
    n: 3, name: "03-start", title: "Запуск",
    async run(ctx) {
      const { actor } = ctx;
      await openScenario(ctx, "pool-exhausted");
      await actor.hold(1.5);
      await actor.moveTo(".side h2.section", { text: "Задача", fx: 0.9, fy: 0 });
      await actor.hold(4);
    },
  },
  {
    n: 4, name: "04-alert", title: "Алерт",
    async run({ page, actor, skip }) {
      await actor.scrollTo(SIDE, ".side h2.section", { text: "Состояние сервиса", at: 0.02, ms: 1600 });
      await actor.moveTo(".side .stack > p", { fx: 1.02, fy: 0 });
      await actor.hold(3);
      // Алерт приходит через минуту-полторы: ожидание вырезаем, сам момент оставляем.
      await skip(() => page.waitForSelector('.badge[data-state="alert"]', { timeout: 300000 }),
        { show: 0, backfill: 2.5, after: 0.4 });
      await actor.moveTo(".top .badge", { fx: 1.6 });
      await actor.hold(5);
      await actor.scrollTo(SIDE, ".now", { at: 0.02, ms: 1400 });
      await actor.moveTo(".now", { fx: 0.97, fy: 1.15 });
      await actor.hold(3);
    },
  },
  {
    n: 5, name: "05-terminal", title: "Поиск в терминале",
    async run(ctx) {
      const { actor, lab } = ctx;
      await focusTerminal(ctx);
      await actor.type("svc logs");
      await actor.hold(0.4);
      await actor.press("Enter");
      await actor.hold(4.5);
      await actor.chord("Control", "c");
      await lab.prompt({ timeout: 10 });
      await actor.hold(3);
      await actor.chord("Control", "l");
      await actor.hold(0.6);
      await run(ctx, "curl localhost:8080/debug/dbstats", { pause: 3.5 });
      await actor.chord("Control", "l");
      await actor.hold(0.6);
      await run(ctx, PSQL, { pause: 4, timeout: 20 });
    },
  },
  {
    n: 6, name: "06-hints", title: "Подсказки",
    async run({ actor }) {
      await actor.scrollTo(SIDE, ".hints", { at: 0.02, ms: 1400 });
      await actor.hold(1);
      await actor.click(".hints > button");
      await actor.moveTo(".hint-list article", { fx: 1.02, fy: 0.1 });
      await actor.hold(5);
      await actor.click(".hints > button");
      await actor.scrollTo(SIDE, ".hint-list article:nth-child(2)", { at: 0.25, ms: 1000 });
      await actor.moveTo(".hint-list article:nth-child(2)", { fx: 1.02, fy: 0.1 });
      await actor.hold(6);
    },
  },
  {
    n: 7, name: "07-restart", title: "Ложная починка",
    async run(ctx) {
      const { actor } = ctx;
      await focusTerminal(ctx);
      await run(ctx, "svc restart", { pause: 1.5, timeout: 60 });
      await actor.scrollTo(SIDE, ".side h2.section", { text: "Состояние сервиса", at: 0.02, ms: 1400 });
      await actor.moveTo(".side .stack > p", { fx: 1.02, fy: 0 });
      await actor.hold(9); // ошибки уходят, время ответа падает
      await verify(ctx);
      await actor.hold(5);
    },
  },
  {
    n: 8, name: "08-fix", title: "Настоящая починка",
    async run(ctx) {
      const { page, actor, lab, skip } = ctx;
      const before = await lab.file(FILE);
      const lines = before.split("\n");
      const found = lines.findIndex((l) => l.includes("createOrder"));
      const begin = lines.findIndex((l) => l.includes("s.db.Begin()"));
      const close = lines.findIndex((l, i) => i > begin && l === "\t}");
      const expected = before.replace("\t\treturn\n\t}\n\n\tvar stock int", `\t\treturn\n\t}\n\t${FIX}\n\n\tvar stock int`);
      if (expected === before || found < 0 || close < 0) throw new Error(`${FILE} не такой, как ждёт сценарий: правку некуда вставить`);

      await actor.click(`.tab[data-path="${FILE}"]`);
      await actor.hold(1.5);
      await actor.click(".monaco", { fx: 0.6, fy: 0.3 });
      if (!(await page.evaluate(() => !!document.activeElement?.closest(".monaco-editor")))) throw new Error("фокус не в редакторе");
      await actor.chord("Control", "f");
      await actor.hold(0.4);
      await actor.type("createOrder");
      await actor.hold(1.5);
      await actor.press("Escape");
      await actor.moveTo(".monaco", { fx: 0.97, fy: 0.95 });
      await actor.hold(1);
      await actor.press("ArrowDown", close - found, 140);
      await actor.press("End");
      await actor.hold(0.8);
      await actor.press("Enter");
      await actor.type(FIX, { cps: 9 });
      await actor.press("Escape");
      await actor.hold(2.5);
      await actor.click(".pane-head button.ghost", { text: "Сохранить файл" });
      await actor.hold(2);
      const after = await lab.file(FILE);
      if (after !== expected) throw new Error(`после сохранения ${FILE} отличается от эталонной правки`);

      await focusTerminal(ctx);
      await actor.type("svc deploy");
      await actor.hold(0.4);
      await actor.press("Enter");
      await skip(() => lab.prompt({ timeout: 300 }), { show: 2.5, backfill: 1.2, after: 0.3 });
      await actor.hold(3);
      await verify(ctx, { backfill: 1.5 });
      await actor.hold(4);
    },
  },
  {
    n: 9, name: "09-debrief", title: "Разбор",
    async run({ page, actor }) {
      const VIEW = ".debrief-view";
      await page.waitForSelector(`${VIEW}:not([hidden])`, { timeout: 30000 });
      await actor.moveTo(VIEW, { fx: 0.98, fy: 0.5 });
      await actor.hold(3);
      for (const heading of ["Как это находится", "Как чинить", "Почему очевидные"]) {
        await actor.scrollTo(VIEW, `${VIEW} h2`, { text: heading, at: 0.03, ms: 1800 });
        await actor.hold(3);
      }
      const summary = await actor.byText(`${VIEW} summary`, "Все починки");
      await actor.scrollTo(VIEW, summary, { at: 0.1, ms: 2000 });
      await actor.hold(1);
      await actor.click(summary, { fx: 0.15 });
      await actor.hold(1);
      await actor.moveTo(VIEW, { fx: 0.985, fy: 0.5 });
      await actor.scrollTo(VIEW, `${VIEW} details[open] h3`, { text: "Верные починки", at: 0.02, ms: 1400 });
      await actor.hold(4);
      await actor.scrollTo(VIEW, `${VIEW} details[open] h3`, { text: "Ложные починки", at: 0.02, ms: 1600 });
      await actor.hold(6);
      await actor.scrollTo(SIDE, ".report", { at: 0.02, ms: 1000 });
      await actor.moveTo(".report", { fx: 0.97, fy: 0.2 });
      await actor.hold(4);
    },
  },
];
