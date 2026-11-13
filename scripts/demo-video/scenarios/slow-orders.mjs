// Шаги ролика по сюжету slow-orders: короткий кусок после pool-exhausted, блоки 10–13.
// Показывает инцидент без кода: всё в терминале и базе, редактор не трогаем.
// Подсказки, ложную починку и разбор не показываем: их уже показал первый сюжет.
import { focusTerminal, openScenario, run, SIDE, verify } from "../lib/steps.mjs";

// Кто занят в базе: одна строка на запрос вместо строки на сессию, чтобы вывод уместился в экран.
const ACTIVE = `psql -U postgres -d shop -c "select count(*), left(query, 80) from pg_stat_activity where state='active' group by 2"`;
// План как в подсказке «Почему запрос дорогой», но без оценок стоимости: строки уже, пейджер выключен.
const EXPLAIN = `psql -U postgres -d shop -P pager=off -c "explain (analyze, costs off) select * from orders where customer_id = 5 order by created_at desc limit 20"`;
// Эталонная правка: repairs.go «составной индекс», подсказка «Готовая правка».
const INDEX = `psql -U postgres -d shop -c "create index concurrently orders_customer_created on orders (customer_id, created_at desc)"`;
const CPS = 26; // длинные команды набираем быстро: кусок ролика короткий, 60–75 с

const LATENCY = ".stack figure.chart:nth-of-type(2)";

export const blocks = [
  {
    n: 10, name: "10-start-alert", title: "Запуск и алерт", tail: 2,
    async run(ctx) {
      const { page, actor, lab, skip } = ctx;
      await openScenario(ctx, "slow-orders");
      await actor.scrollTo(SIDE, ".side h2.section", { text: "Состояние сервиса", at: 0.02, ms: 1400 });
      await actor.moveTo(LATENCY, { fx: 0.72, fy: 0.08 });
      await actor.hold(1.5);
      // Тихая минута и рост трафика вырезаются; рост времени ответа и смена бейджа остаются.
      await skip(() => page.waitForSelector('.badge[data-state="alert"]', { timeout: 400000 }),
        { show: 0, backfill: 3, after: 0.4 });
      await actor.hold(3.5);
      console.log(`алерт: ${(await lab.view()).alertText}`);
    },
  },
  {
    n: 11, name: "11-search", title: "Поиск", tail: 2,
    async run(ctx) {
      const { actor } = ctx;
      await focusTerminal(ctx);
      await run(ctx, ACTIVE, { pause: 2.5, timeout: 30, cps: CPS });
      await actor.chord("Control", "l");
      await actor.hold(0.5);
      await run(ctx, EXPLAIN, { pause: 4, timeout: 60, cps: CPS });
    },
  },
  {
    n: 12, name: "12-index", title: "Починка", tail: 2,
    async run(ctx) {
      const { actor, lab, skip } = ctx;
      await actor.type(INDEX, { cps: CPS });
      await actor.hold(0.4);
      await actor.press("Enter");
      // Сборка индекса под нагрузкой — десятки секунд: в кадре начало и ответ CREATE INDEX.
      await skip(() => lab.prompt({ timeout: 600 }), { show: 2, backfill: 1.2, after: 0.3 });
      if (!/CREATE INDEX/.test(await lab.termText())) throw new Error("индекс не создан: нет ответа CREATE INDEX");
      await actor.hold(3);
    },
  },
  {
    n: 13, name: "13-verify", title: "Проверка", tail: 2,
    async run(ctx) {
      const { actor, lab } = ctx;
      await verify(ctx, { backfill: 1.5 });
      await actor.hold(4.5);
      const { report } = await lab.view();
      console.log(`проверка: ${report.passed ? "пройдена" : "не пройдена"}`);
      for (const i of report.items) console.log(`  ${i.pass ? "✓" : "✕"} ${i.name} — ${i.detail}${i.hint ? ` (${i.hint})` : ""}`);
    },
  },
];
