#!/usr/bin/env bash
# e2e на настоящих виртуалках по уровням, см. docs/adr/0008-e2e-levels.md.
# Виртуалки идут строго по одной: параллельный прогон вешает машину.
#
#   e2e.sh changed [base]   то, что требует дифф от base (по умолчанию HEAD), — держит коммит
#   e2e.sh plan [base]      только показать, что выбрал бы changed
#   e2e.sh contract         тесты каркаса: всё, кроме TestScenario
#   e2e.sh scenario id ...  все случаи названных сюжетов
#   e2e.sh smoke [--feature memory|rise] [id ...]   по два случая на сюжет
#   e2e.sh all              всё
#
# Лог каждого прогона — .data/e2e/<режим>-<время>.log, код выхода — как у go test.
set -euo pipefail
cd "$(dirname "$0")/.."
pkg=./platform/internal/lab/

# Сюжеты, на которых проверяется каркас, — константы fwPlain и fwMemory из e2e_test.go.
fw=$(grep -oE '^\s*fw(Plain|Memory)\s*=\s*"[^"]+"' platform/internal/lab/e2e_test.go | grep -oE '[a-z0-9-]+"$' | tr -d '"' | tr '\n' ' ')
fw=${fw% }
[ "$(wc -w <<<"$fw")" = 2 ] || { echo "не нашёл fwPlain и fwMemory в e2e_test.go" >&2; exit 1; }

all_ids() { (cd scenarios && ls -d */ | tr -d /); }

has_feature() { # id feature
  case $2 in
    memory) grep -q '^MEMORY_MAX=' "scenarios/$1/image/service.env" ;;
    rise) grep -q 'Rise:' scenarios/"$1"/*.go ;;
    *) echo "неизвестный признак $2: memory или rise" >&2; exit 2 ;;
  esac
}

guard() {
  # Виртуалки прошлого прогона гаснут ещё пару секунд после выхода go test.
  local i
  for i in $(seq 20); do
    pgrep -x firecracker >/dev/null || return 0
    sleep 0.5
  done
  if pgrep -x firecracker >/dev/null; then
    echo "уже работает виртуалка (сервер со стендом или другой прогон): останови её, виртуалки идут по одной" >&2
    exit 1
  fi
}

gotest() { # имя-лога, аргументы go test
  local name=$1; shift
  mkdir -p .data/e2e
  local log=.data/e2e/$name-$(date +%Y%m%d-%H%M%S).log
  echo "== $name: go test $* (лог $log)"
  local start=$SECONDS rc=0
  nice -n 10 ionice -c3 go test -tags e2e "$pkg" -count=1 -parallel 1 -v "$@" 2>&1 | tee "$log" || rc=${PIPESTATUS[0]}
  echo "== $name: код $rc за $(( (SECONDS-start)/60 )) мин $(( (SECONDS-start)%60 )) с"
  return "$rc"
}

run_regex() { # id ... → ^TestScenario$/^(a|b)$
  local IFS='|'
  echo "^TestScenario\$/^($*)\$"
}

contract() {
  guard
  scripts/build-images.sh $fw
  gotest contract -timeout 1h -skip '^TestScenario$'
}

scenario() {
  guard
  scripts/build-images.sh "$@"
  gotest "scenario-$(tr ' ' + <<<"$*")" -timeout 3h -run "$(run_regex "$@")"
}

smoke() {
  local feature="" ids=()
  while [ $# -gt 0 ]; do
    case $1 in
      --feature) feature=$2; shift 2 ;;
      *) ids+=("$1"); shift ;;
    esac
  done
  [ ${#ids[@]} -gt 0 ] || mapfile -t ids < <(all_ids)
  if [ -n "$feature" ]; then
    local picked=()
    for id in "${ids[@]}"; do if has_feature "$id" "$feature"; then picked+=("$id"); fi; done
    ids=("${picked[@]}")
    [ ${#ids[@]} -gt 0 ] || { echo "нет сюжетов с признаком $feature"; return 0; }
  fi
  guard
  scripts/build-images.sh "${ids[@]}"
  LAB_E2E_CASES=smoke gotest smoke -timeout 3h -run "$(run_regex "${ids[@]}")"
}

# Разбор диффа: заполняет need_contract, need_base, need_ids, skipped.
classify() {
  local base=$1 f id
  need_contract=0 need_base=0 need_ids=() skipped=()
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    case $f in
      scenarios/scenarios.go | scenarios/scenarios_test.go) need_contract=1 ;;
      scenarios/*/*)
        id=${f#scenarios/}; id=${id%%/*}
        if [ ! -d "scenarios/$id" ]; then
          [[ " ${skipped[*]} " == *" сюжет $id удалён "* ]] || skipped+=("сюжет $id удалён ")
          continue
        fi
        [[ " ${need_ids[*]} " == *" $id "* ]] || need_ids+=("$id")
        if [[ " $fw " == *" $id "* ]]; then need_contract=1; fi ;;
      platform/guest/base.Dockerfile)
        need_contract=1
        # Версии Alpine и пакетов меняют поведение самих поломок, контракт этого не видит.
        if git diff -U0 "$base" -- "$f" | grep -qE '^[+-](FROM|RUN apk)'; then need_base=1; fi ;;
      scripts/setup-firecracker.sh) need_contract=1 need_base=1 ;;
      platform/* | scripts/build-images.sh | go.mod | go.sum) need_contract=1 ;;
      *) skipped+=("$f") ;;
    esac
  done < <(git diff --name-only "$base"; git ls-files --others --exclude-standard)
}

show_plan() {
  echo "дифф от $1:"
  [ $need_contract = 0 ] || echo "  contract — правка каркаса или сюжета каркаса ($fw)"
  [ ${#need_ids[@]} = 0 ] || echo "  scenario ${need_ids[*]} — правка сюжета"
  [ $need_base = 0 ] || echo "  smoke всех сюжетов до коммита — версии базового образа, ядра или Firecracker"
  [ $need_base = 1 ] || [ $need_contract = 0 ] || echo "  smoke всех сюжетов — в CI после пуша"
  [ ${#skipped[@]} = 0 ] || printf '  без e2e: %s\n' "${skipped[@]}"
  if [ $need_contract = 0 ] && [ ${#need_ids[@]} = 0 ]; then
    echo "  e2e не нужен; интерфейс — глазами"
  fi
}

changed() {
  local base=${1:-HEAD}
  classify "$base"
  show_plan "$base"
  [ $need_contract = 0 ] || contract
  [ ${#need_ids[@]} = 0 ] || scenario "${need_ids[@]}"
  [ $need_base = 0 ] || smoke
}

cmd=${1:-}; shift || true
case $cmd in
  changed) changed "$@" ;;
  plan) classify "${1:-HEAD}"; show_plan "${1:-HEAD}" ;;
  contract) contract ;;
  scenario) [ $# -gt 0 ] || { echo "нужен id сюжета" >&2; exit 2; }; scenario "$@" ;;
  smoke) smoke "$@" ;;
  all) guard; scripts/build-images.sh; gotest all -timeout 6h ;;
  *) sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
esac
