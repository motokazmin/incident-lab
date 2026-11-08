# Архитектура

Детали — [plan.md](plan.md), решения — [adr/](adr/).

## Зоны

- **Каркас, `platform/`** (меняется редко): шлюз, стенды, песочницы, нагрузка, проверяющий, агент в госте.
- **Прикладной код, `scenarios/`** (меняется часто): сюжеты инцидентов и их список `scenarios.All()`. Сюжет — папка, см. ниже. Сервис — отдельный Go-модуль с `vendor`, сети в песочнице нет.
- **Веб, `web/`:** TypeScript, Vite, xterm.js, Monaco. Графики — свой SVG.

Сюжет задаёт нагрузку, алерт, проверки и тексты. Лимиты и жизненный цикл у каркаса.

## Папка сюжета

Новый сюжет — папка `scenarios/<id>/` и строка в `scenarios.All()`, см. [ADR 0005](adr/0005-scenario-is-a-folder.md).

```text
scenarios/<id>/
  scenario.go             декларация: ID, тексты, алерт, нагрузка
  checks.go               проверки починки
  repairs.go              верные и ложные починки для e2e
  reference.go            эталонный путь, команды из подсказок
  briefing.md debrief.md hints.md
  image/  Dockerfile boot service.env schema.sql postgresql.lab.conf actions/
  service/                учебный сервис с vendor
```

Общая обвязка лежит в `platform/guest` и попадает в базовый образ: `svc`, `lab-pg-start`, `lab-service-start` (супервизор: строка о выходе, счётчик перезапусков, cgroup по `MEMORY_MAX`), `lab-service-run` (с `MEMORY_MAX` — запуск с копии в tmpfs), `lab-setup`, действия `restart-service`, `service-restarts`, а для сюжетов с `MEMORY_MAX` — `memory-limit` и `oom-events`. Агент защищён от OOM killer, см. [ADR 0007](adr/0007-service-crash-and-memory-limit.md). Вход стенда всегда `127.0.0.1:8080`, код всегда в `/srv/app`.

Сборка: `scripts/build-images.sh [id]`. Проверка: `TestContract` без виртуалок, `go test -tags e2e ./platform/internal/lab/ -parallel 1 -run TestScenario/<id>` на настоящих.

Сюжеты сейчас: `pool-exhausted`, `slow-orders`, `keeps-restarting`. У `keeps-restarting` есть второй процесс — заглушка чужого сервиса рекомендаций в `recs/`, бинарник `/opt/ml/recs` вне `/srv/app`, его запускает `boot` сюжета. `Scenario.Rise` поднимает нагрузку по расписанию, см. [ADR 0006](adr/0006-traffic-reveals-the-fault.md).

## Основные сущности

| Сущность | Где | Роль |
|---|---|---|
| Scenario | `platform/scenario` | Контракт: нагрузка, алерт, проверки, тексты, для тестов — починки и эталонный путь |
| Env | `platform/scenario` | Всё, что разрешено проверке: запрос, нагрузка, именованное действие |
| Manager, Lab | `platform/internal/lab` | Лимиты мест и времени, состояния стенда, алерт, лента событий |
| Sandbox, SandboxProvider | `platform/internal/ports` | Узкие интерфейсы песочницы; единственный путь внутрь — `Dial` |
| Provider | `platform/internal/sandbox/firecracker` | Запуск виртуалок, лимиты, одноразовые диски |
| Load | `platform/internal/load` | Нагрузка снаружи песочницы, план по сиду, посекундные метрики |
| Verifier | `platform/internal/verifier` | Идёт по проверкам до первого провала |
| Gateway | `platform/internal/gateway` | Единственный путь из браузера: API, терминал, статика, защита Host и Origin |
| labagent | `platform/cmd/labagent` | Агент в госте. Не доверенный: пользователь там root |

## Поток данных

```text
Браузер ─ HTTP/WS ─> Gateway ─> Lab ─> Sandbox.Dial ─ vsock ─> labagent ─┬─ 1024 управление
                                 │                                       ├─ 1025 ретранслятор к сервису
                                 └─ Load, Verifier (снаружи)             └─ 1026 терминал
```

Графики и алерт строятся из того, что нагрузка измерила снаружи. Из песочницы в браузер попадают только текст терминала и файлы в редакторе, оба показываются как текст. Проверка — обратная связь, а не доказательство, см. [ADR 0004](adr/0004-verification-is-feedback.md).

## Запуск стенда

Firecracker → `/sbin/lab-init` (overlay на диск стенда) → `labagent` → `/etc/lab/boot` сюжета (`lab-pg-start`, `lab-service-start`) → нагрузка, с `Rise` сперва тихая часть → наблюдение за алертом. Агент завершился — виртуалка выключается, стенд заканчивается.

## Composition root

`platform/cmd/server/main.go`: конфиг → провайдер песочниц → `scenarios.All()` → менеджер стендов → шлюз. Других мест сборки зависимостей нет.

## Граф зависимостей

`cmd/server → gateway → lab → (verifier, load, agentclient) → (ports, agentproto, scenario)`; `cmd/server → sandbox/firecracker → ports`; `cmd/server → scenarios → scenarios/* → scenario`.

`ports`, `agentproto`, `scenario` — leaf-пакеты, циклов нет. Отдельного пакета `api` нет: HTTP живёт в шлюзе, разделять пока нечего. Управляющая база не нужна: стенды одноразовые и живут в памяти сервера.
