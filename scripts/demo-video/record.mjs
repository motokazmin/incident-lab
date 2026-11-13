// Немой черновик демонстрации стенда: браузер под скриптом проходит сюжет по блокам, каждый блок
// пишется в свой файл, долгие ожидания вырезаются при записи, в конце — склейка и кадры-превью.
//
//   node record.mjs [сюжет] [--blocks 3,4,5] [--lab <id>] [--keep] [--no-assemble] [--url http://127.0.0.1:8090]
//
// Шаги сюжета — scenarios/<сюжет>.mjs, механика записи — lib/. Подробнее — README.md.
import { execFileSync, spawn } from "node:child_process";
import { mkdirSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import puppeteer from "puppeteer-core";
import { Actor, sleep } from "./lib/actor.mjs";
import { assemble } from "./lib/assemble.mjs";
import { Lab } from "./lib/lab.mjs";
import { Recorder } from "./lib/recorder.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const repo = resolve(here, "../..");
const argv = process.argv.slice(2);
const flag = (name) => argv.includes(`--${name}`);
const opt = (name, def) => {
  const i = argv.indexOf(`--${name}`);
  return i >= 0 ? argv[i + 1] : def;
};
const scenarioId = argv.find((a, i) => !a.startsWith("--") && !argv[i - 1]?.match(/^--(blocks|lab|url|out)$/)) ?? "pool-exhausted";
const base = opt("url", "http://127.0.0.1:8090");
const out = resolve(repo, opt("out", `.data/demo-video/${scenarioId}`));
const only = opt("blocks", "")?.split(",").filter(Boolean).map(Number);

const { blocks } = await import(`./scenarios/${scenarioId}.mjs`);
const chosen = blocks.filter((b) => !only?.length || only.includes(b.n));
if (!chosen.length) throw new Error("ни одного блока не выбрано");

const up = async () => fetch(base + "/api/scenarios").then((r) => r.ok, () => false);
const firecrackers = () => {
  try { return execFileSync("pgrep", ["-x", "firecracker"], { encoding: "utf8" }).trim(); } catch { return ""; }
};

// Сервер: свой поднимаем через scripts/run.sh и гасим в конце, чужой не трогаем.
let server = null;
if (!(await up())) {
  console.log("сервер не отвечает, запускаю scripts/run.sh");
  server = spawn(join(repo, "scripts/run.sh"), [], { cwd: repo, detached: true, stdio: ["ignore", "ignore", "inherit"] });
  for (let i = 0; !(await up()); i++) {
    if (i > 600 || server.exitCode !== null) throw new Error("сервер не поднялся");
    await sleep(500);
  }
}
const stopServer = () => {
  if (server && server.exitCode === null) try { process.kill(-server.pid, "SIGTERM"); } catch {}
};

const lab = new Lab(base, null);
if (opt("lab")) lab.id = opt("lab");
else if (firecrackers()) {
  stopServer();
  throw new Error("уже работает виртуалка: на ноутбуке стенды только по одному. Закрой её или передай --lab <id>");
}

mkdirSync(join(out, "parts"), { recursive: true });
const browser = await puppeteer.launch({
  executablePath: process.env.CHROME ?? "/usr/bin/google-chrome",
  headless: true,
  args: ["--hide-scrollbars", "--mute-audio", "--window-size=1280,720", "--lang=ru-RU", "--font-render-hinting=none"],
  defaultViewport: { width: 1280, height: 720, deviceScaleFactor: 1.5 },
});
const done = [];
try {
  const page = await browser.newPage();
  page.on("pageerror", (e) => console.error("страница:", e.message));
  lab.page = page;
  const actor = new Actor(page);
  await actor.install();
  const rec = new Recorder(page, { fps: 30 });
  /** Показывает начало ожидания, вырезает середину и возвращает в кадр конец: момент, которого ждали. */
  const skip = async (cond, { show = 1.5, backfill = 1, after = 0 } = {}) => {
    await actor.hold(show);
    rec.pause();
    await cond();
    await sleep(after * 1000);
    rec.resume({ backfill });
  };
  const ctx = { page, actor, lab, rec, skip, base };
  if (lab.id) {
    // Отладка с середины: открыть уже живой стенд, как его видит дежурный.
    await page.goto(`${base}/#lab=${lab.id}`, { waitUntil: "networkidle0" });
    await page.waitForSelector(".xterm-rows .xterm-cursor", { timeout: 60000 });
    await page.waitForSelector(".monaco .view-lines", { timeout: 60000 });
    await sleep(1500);
  }

  for (const b of chosen) {
    const file = join(out, "parts", `${b.name}.mkv`);
    console.log(`блок ${b.name}: запись`);
    await rec.start(file);
    try {
      await b.run(ctx);
      await actor.hold(b.tail ?? 2.5); // неподвижный хвост под голос
    } finally {
      const sec = await rec.stop();
      console.log(`блок ${b.name}: ${sec.toFixed(1)} с, кадров от браузера ${rec.received}`);
      done.push({ ...b, file, sec });
    }
  }
} finally {
  await browser.close();
  if (!flag("keep")) await lab.stop();
  stopServer();
}

if (!flag("no-assemble")) await assemble(out, done);
console.log(lab.id && flag("keep") ? `стенд оставлен: --lab ${lab.id}` : "готово");
