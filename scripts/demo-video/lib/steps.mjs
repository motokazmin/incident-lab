// Общие шаги сюжетов: открыть стенд с главной, команда в терминале, «Проверить починку».
// Что и в каком порядке показывать, решает файл сюжета в scenarios/.

export const SIDE = ".side";
export const TERM = ".term-host";

/** Главная → карточка сюжета → «Начать» → стенд поднят. Подъём стенда вырезается. */
export async function openScenario({ page, actor, lab, skip, base }, id) {
  const { title } = (await lab.scenarios()).find((s) => s.id === id);
  await page.goto(base + "/", { waitUntil: "networkidle0" });
  await actor.hold(2);
  const card = await actor.byText(".card", title);
  await actor.scrollTo("html", card, { at: 0.3, ms: 1600 });
  await actor.hold(1.5);
  await actor.click(await card.$("button"));
  await page.waitForSelector(".top .badge", { timeout: 60000 });
  lab.id = new URLSearchParams((await page.evaluate(() => location.hash)).slice(1)).get("lab");
  console.log(`стенд ${lab.id}`);
  await skip(async () => {
    await lab.until((v) => v.state !== "booting", { timeout: 120, what: "подъём стенда" });
    await page.waitForSelector(".xterm-rows .xterm-cursor", { timeout: 60000 });
    await page.waitForSelector(".monaco .view-lines", { timeout: 60000 });
    await lab.prompt({ timeout: 30 });
  }, { show: 1.5, backfill: 0.8, after: 0.5 });
}

/** Команда в терминале: набрать, Enter, дождаться приглашения, дать диктору договорить. */
export async function run({ actor, lab }, cmd, { pause = 3, timeout = 120, cps } = {}) {
  await actor.type(cmd, cps ? { cps } : undefined);
  await actor.hold(0.4);
  await actor.press("Enter");
  await lab.prompt({ timeout });
  await actor.hold(pause);
}

/** Фокус в терминал кликом, как человек. Курсор уводим из текста вправо. */
export async function focusTerminal({ actor, page }) {
  await actor.click(TERM, { fx: 0.75, fy: 0.55 });
  const ok = await page.evaluate(() => document.activeElement?.classList.contains("xterm-helper-textarea"));
  if (!ok) throw new Error("фокус не в терминале");
  await actor.moveTo(TERM, { fx: 0.93, fy: 0.85, ms: 500 });
}

/** «Проверить починку»: клик, ожидание нового отчёта вырезается, появление результата остаётся в кадре. */
export async function verify({ page, actor, lab, skip }, { backfill = 1.2 } = {}) {
  const prev = (await lab.view()).report?.finished ?? "";
  await actor.click(".top button.primary");
  await actor.scrollTo(SIDE, ".now", { at: 0.02, ms: 1000 });
  await skip(async () => {
    await lab.until((v) => v.state !== "verifying" && v.report && v.report.finished !== prev, { timeout: 600, what: "результат проверки" });
    await page.waitForFunction(() => document.querySelector(".top .badge")?.dataset.state !== "verifying", { timeout: 10000 });
  }, { show: 2, backfill, after: 0.6 });
  await actor.moveTo(".report", { fx: 0.97, fy: 0.2 });
}
