package failingorders

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"incident-lab/platform/scenario"
)

const (
	checkCustomer = 9001
	crossRounds   = 10
	crossBuyers   = 16
	soldOutID     = 199
	lastUnitFrom  = 801
	lastUnitTo    = 1200
	lastUnits     = 6
	lastBuyers    = 8
)

// Простаивающее соединение PostgreSQL 15+ сбрасывает статистику раз в 10 с: читаем
// после этого срока, иначе хвост прошлой проверки попадёт в эту. Тест укорачивает.
var statsDelay = 11 * time.Second

// Залп — встречные корзины на товарах, которых нет в нагрузке. У каждого покупателя
// залпа свой клиент и своя четвёртая позиция: блокировка по клиенту или по составу корзины
// иначе выстроила бы весь залп в очередь и прошла бы проверку, а в нагрузке клиенты и
// корзины разные, и дедлоки остаются. Такие же заказы лежат в образе (schema.sql).
const (
	crossFirstOrder    = 30001 // заказ из образа покупателя i — crossFirstOrder+i
	crossFirstCustomer = 9101  // клиент покупателя i — crossFirstCustomer+i
	crossFirstOwn      = 731   // своя позиция покупателя i — crossFirstOwn+i
	lineCount          = 4     // позиций в каждом заказе залпа
	atomicRepeatOrder  = 30017 // заказ из образа с раскупленным товаром последним
)

// crossCart — корзина покупателя i: у чётных общие товары по возрастанию, у нечётных
// встречно, своя позиция последней.
func crossCart(i int) []line {
	shared := []line{{701, 1}, {702, 1}, {703, 1}}
	if i%2 == 1 {
		slices.Reverse(shared)
	}
	return append(shared, line{crossFirstOwn + i, 1})
}

type line struct{ ProductID, Qty int }

func body(customer int, lines []line) string {
	parts := make([]string, len(lines))
	for i, l := range lines {
		parts[i] = fmt.Sprintf(`{"product_id":%d,"qty":%d}`, l.ProductID, l.Qty)
	}
	return fmt.Sprintf(`{"customer_id":%d,"items":[%s]}`, customer, strings.Join(parts, ","))
}

// create оформляет заказ клиента проверки и возвращает статус и номер заказа.
func create(ctx context.Context, env scenario.Env, lines []line) (int, int64, error) {
	return createAs(ctx, env, checkCustomer, lines)
}

func createAs(ctx context.Context, env scenario.Env, customer int, lines []line) (int, int64, error) {
	return placed(env.Do(ctx, http.MethodPost, "/orders", body(customer, lines)))
}

func repeat(ctx context.Context, env scenario.Env, id int64) (int, int64, error) {
	return placed(env.Do(ctx, http.MethodPost, fmt.Sprintf("/orders/%d/repeat", id), ""))
}

func placed(st int, resp []byte, err error) (int, int64, error) {
	if err != nil || st != http.StatusCreated {
		return st, 0, err
	}
	var r struct {
		OrderID *int64 `json:"order_id"`
	}
	if json.Unmarshal(resp, &r) != nil || r.OrderID == nil {
		return st, 0, fmt.Errorf("ответ без номера заказа")
	}
	return st, *r.OrderID, nil
}

func stock(ctx context.Context, env scenario.Env, id int) (int, error) {
	st, resp, err := env.Do(ctx, http.MethodGet, "/products/"+strconv.Itoa(id), "")
	if err != nil {
		return 0, err
	}
	if st != http.StatusOK {
		return 0, fmt.Errorf("товар %d: статус %d", id, st)
	}
	var p struct {
		ID    *int `json:"id"`
		Stock *int `json:"stock"`
	}
	if json.Unmarshal(resp, &p) != nil || p.ID == nil || p.Stock == nil || *p.ID != id {
		return 0, fmt.Errorf("ответ не похож на товар %d", id)
	}
	return *p.Stock, nil
}

func stocks(ctx context.Context, env scenario.Env, lines []line) ([]int, error) {
	out := make([]int, len(lines))
	for i, l := range lines {
		s, err := stock(ctx, env, l.ProductID)
		if err != nil {
			return nil, err
		}
		out[i] = s
	}
	return out, nil
}

// counters — признаки в базе: дедлоки, откаты транзакций, время сброса статистики,
// сколько раз обновлялись строки товаров, включая откаченные попытки, и сколько
// соединений ещё заняты работой. busy не счётчик: это снимок в момент чтения.
type counters struct{ deadlocks, rollbacks, reset, updates, busy int64 }

func dbCounters(ctx context.Context, env scenario.Env) (counters, error) {
	out, err := env.Action(ctx, "db-counters")
	if err != nil {
		return counters{}, err
	}
	f := strings.Split(strings.TrimSpace(out), "|")
	if len(f) != 5 {
		return counters{}, fmt.Errorf("непонятный ответ базы")
	}
	var v [5]int64
	for i, s := range f {
		if v[i], err = strconv.ParseInt(s, 10, 64); err != nil {
			return counters{}, fmt.Errorf("непонятный ответ базы")
		}
	}
	return counters{v[0], v[1], v[2], v[3], v[4]}, nil
}

func pause(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// drainLimit — сколько ждать, пока база доделает работу залпа. Обёртка HTTP отвечает
// клиенту 503 через 1 с, а транзакция идёт дальше: после залпа с ошибками в базе ещё
// десятки транзакций в очереди к пулу и к строкам товаров. Тест укорачивает.
var (
	drainLimit = 30 * time.Second
	roundDrain = 10 * time.Second // между раундами залпа
	drainPoll  = 500 * time.Millisecond
	roundSlack = 2 * time.Second // на один оставшийся раунд залпа: ответы приходят за 1 с
)

// left — сколько осталось до конца бюджета проверки; без срока — сколько угодно.
func left(ctx context.Context) time.Duration {
	if dl, ok := ctx.Deadline(); ok {
		return time.Until(dl)
	}
	return time.Duration(1<<62 - 1)
}

// drain ждёт до limit, пока в базе не останется занятых соединений, и возвращает последний
// снимок счётчиков. idle — база освободилась в срок.
func drain(ctx context.Context, env scenario.Env, limit time.Duration) (c counters, idle bool, err error) {
	// Срок по часам, а не по числу опросов: опрос через psql в госте сам занимает время.
	until := time.Now().Add(limit)
	for {
		if c, err = dbCounters(ctx, env); err != nil {
			return counters{}, false, err
		}
		if c.busy == 0 || !time.Now().Before(until) {
			return c, c.busy == 0, nil
		}
		if err = pause(ctx, drainPoll); err != nil {
			return counters{}, false, err
		}
	}
}

// settled читает счётчики базы, когда в них попало всё, что было до этого момента:
// сначала ждёт, пока соединения освободятся, затем — пока придёт их статистика.
// idle — база освободилась в срок; иначе работа прошлого залпа попадёт в следующий.
//
// Ожидание не длиннее, чем оставляет бюджет проверки за вычетом паузы статистики: если
// база так и не освободилась, замер всё равно состоится, а не упрётся в конец бюджета.
func settled(ctx context.Context, env scenario.Env) (c counters, idle bool, err error) {
	d, idle, err := drain(ctx, env, min(drainLimit, left(ctx)-statsDelay-roundSlack))
	if err != nil {
		return counters{}, false, err
	}
	if err = pause(ctx, statsDelay); err != nil {
		return counters{}, false, err
	}
	if c, err = dbCounters(ctx, env); err == nil && !idle {
		c.busy = d.busy // занятые на конце ожидания: их и называет подсказка
	}
	return c, idle, err
}

// Состав залпа: покупатель i делает kind[i % len(kind)].
type kind int

const (
	repeatAB kind = iota
	repeatBA
	createAB
	createBA
)

var (
	mixed   = []kind{repeatAB, repeatBA, createAB, createBA}
	repeats = []kind{repeatAB, repeatBA}
	creates = []kind{createAB, createBA}
)

// salvo даёт crossRounds залпов по crossBuyers одновременных заказов и возвращает статусы
// неудачных и номера оформленных.
func salvo(ctx context.Context, env scenario.Env, kinds []kind) (map[int]int, []int64) {
	failed := map[int]int{}
	var placedIDs []int64
	for round := range crossRounds {
		// Брошенные клиентом транзакции прошлого раунда доделываются в базе: без ожидания
		// они копятся от раунда к раунду, и сколько ждать в конце, решает планировщик.
		// Ждём не дольше, чем оставляет бюджет: оставшимся раундам и итоговому замеру
		// времени должно хватить и тогда, когда база не освобождается вовсе.
		if round > 0 {
			reserve := drainLimit + statsDelay + time.Duration(crossRounds-round+1)*roundSlack
			_, _, _ = drain(ctx, env, min(roundDrain, left(ctx)-reserve))
		}
		codes := make([]int, crossBuyers)
		got := make([]int64, crossBuyers)
		var wg sync.WaitGroup
		for i := range crossBuyers {
			wg.Go(func() {
				// Чётность i задаёт направление корзины, kinds — путь: AB и BA у обоих путей.
				switch kinds[i%len(kinds)] {
				case repeatAB, repeatBA:
					codes[i], got[i], _ = repeat(ctx, env, int64(crossFirstOrder+i))
				case createAB, createBA:
					codes[i], got[i], _ = createAs(ctx, env, crossFirstCustomer+i, crossCart(i))
				}
			})
		}
		wg.Wait()
		for i, c := range codes {
			if c != http.StatusCreated {
				failed[c]++
				continue
			}
			placedIDs = append(placedIDs, got[i])
		}
	}
	return failed, placedIDs
}

// checkCrossing залпами оформляет и повторяет заказы с общими товарами во встречном
// порядке. Повторяет и заказы из образа: правка только в обработчике нового заказа их
// не выправит. Верная починка не даёт базе дедлоков и не делает работу дважды: номера
// заказов идут подряд, а строки товаров обновлены ровно по разу на позицию.
func checkCrossing(ctx context.Context, env scenario.Env) scenario.Result {
	before, _, err := settled(ctx, env)
	if err != nil {
		return scenario.Result{Detail: "не удалось спросить базу"}
	}
	failed, placedIDs := salvo(ctx, env, mixed)
	after, idle, err := settled(ctx, env)
	if err != nil {
		return scenario.Result{Detail: "не удалось спросить базу"}
	}
	ok := int64(len(placedIDs))
	dDead, dRoll, dUpd := after.deadlocks-before.deadlocks, after.rollbacks-before.rollbacks, after.updates-before.updates
	detail := fmt.Sprintf("%d заказов, неудачных %d, дедлоков в базе %d, обновлений товаров %d на %d позиций", crossRounds*crossBuyers, sum(failed), dDead, dUpd, ok*lineCount)
	slices.Sort(placedIDs)
	switch {
	case after.reset != before.reset || dDead < 0 || dRoll < 0 || dUpd < 0:
		return scenario.Result{Detail: "статистика базы сброшена во время проверки"}
	case dDead > 0:
		hint, split := hintDeadlock, ""
		if idle {
			hint, split = diagnose(ctx, env, after)
		} else {
			split = fmt.Sprintf("; разбивки по путям нет: база не освободилась за %d с, заняты соединений: %d", int(drainLimit.Seconds()), after.busy)
		}
		return scenario.Result{Detail: detail + split, Hint: hint}
	case len(slices.Compact(slices.Clone(placedIDs))) != len(placedIDs):
		return scenario.Result{Detail: detail + ", один номер заказа выдан дважды"}
	case sum(failed) > 0 && dRoll > 0:
		return scenario.Result{Detail: detail + ", статусы " + codesText(failed), Hint: hintRollback}
	case sum(failed) > 0:
		return scenario.Result{Detail: detail + ", статусы " + codesText(failed), Hint: hintStuck}
	case ok > 0 && placedIDs[ok-1]-placedIDs[0]+1 != ok:
		// Номер берётся из последовательности, а она не откатывается: дыра — брошенная попытка.
		return scenario.Result{Detail: fmt.Sprintf("%s, номера заказов %d–%d с пропусками", detail, placedIDs[0], placedIDs[ok-1]), Hint: hintRollback}
	case dUpd != ok*lineCount:
		return scenario.Result{Detail: detail, Hint: hintRollback}
	}
	return scenario.Result{Pass: true, Detail: detail}
}

// probeBudget — сколько времени должно остаться на диагностику: два залпа и две паузы
// статистики. Не хватает — подсказка прежняя, без разбивки по путям.
var probeBudget = 50 * time.Second

// diagnose разбивает смешанный залп на пути: только повторы, затем только новые заказы.
// Нагрузка на графике шлёт одни повторы, поэтому правка одного пути успокаивает график,
// а смешанный залп падает. Подсказка говорит, что увидела база, а не что сделал дежурный.
// Любой сбой диагностики оставляет прежнюю подсказку.
func diagnose(ctx context.Context, env scenario.Env, from counters) (string, string) {
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < probeBudget {
		return hintDeadlock, ""
	}
	r, mid, ok := probePath(ctx, env, from, repeats)
	if !ok {
		return hintDeadlock, ""
	}
	c, _, ok := probePath(ctx, env, mid, creates)
	if !ok {
		return hintDeadlock, ""
	}
	split := fmt.Sprintf("; по отдельности: в повторах %s, в новых заказах %s", r, c)
	switch {
	case r.clean && c.clean:
		return hintCrossPaths, split
	case r.clean && c.deadlocks > 0:
		return hintCreatesCross, split
	case c.clean && r.deadlocks > 0:
		return hintRepeatsCross, split
	}
	return hintDeadlock, split
}

// probe — залп по одному пути. clean — путь прошёл все признаки смешанного залпа:
// все заказы оформлены, номера подряд, строки товаров обновлены по разу, дедлоков нет.
// Ноль дедлоков у пути, который отвечает ошибкой или заглушкой, — не чистый путь.
type probe struct {
	deadlocks int64
	clean     bool
	why       string // почему путь не чист, кроме дедлоков
}

func (p probe) String() string {
	s := fmt.Sprintf("дедлоков %d", p.deadlocks)
	if p.why != "" {
		s += " (" + p.why + ")"
	}
	return s
}

func probePath(ctx context.Context, env scenario.Env, before counters, kinds []kind) (probe, counters, bool) {
	failed, ids := salvo(ctx, env, kinds)
	after, idle, err := settled(ctx, env)
	if err != nil || !idle {
		return probe{}, after, false
	}
	dDead, dUpd := after.deadlocks-before.deadlocks, after.updates-before.updates
	if after.reset != before.reset || dDead < 0 || dUpd < 0 || after.rollbacks < before.rollbacks {
		return probe{}, after, false
	}
	n := int64(len(ids))
	slices.Sort(ids)
	why := ""
	switch {
	case sum(failed) > 0:
		why = "статусы " + codesText(failed)
	case n != crossRounds*crossBuyers:
		why = fmt.Sprintf("оформлено %d", n)
	case len(slices.Compact(slices.Clone(ids))) != len(ids):
		why = "номер заказа выдан дважды"
	case ids[n-1]-ids[0]+1 != n:
		why = "номера с пропусками"
	case dUpd != n*lineCount:
		why = fmt.Sprintf("обновлений товаров %d на %d позиций", dUpd, n*lineCount)
	}
	return probe{dDead, dDead == 0 && why == "", why}, after, true
}

func sum(m map[int]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func codesText(m map[int]int) string {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%d×%d", k, m[k])
	}
	return strings.Join(parts, ", ")
}

func checkRepeat(ctx context.Context, env scenario.Env) scenario.Result {
	cart := []line{{711, 1}, {712, 2}, {713, 3}}
	st, id, err := create(ctx, env, cart)
	if err != nil || st != http.StatusCreated {
		return scenario.Result{Detail: fmt.Sprintf("заказ не создан: статус %d, %v", st, err)}
	}
	before, err := stocks(ctx, env, cart)
	if err != nil {
		return scenario.Result{Detail: err.Error()}
	}
	st, again, err := repeat(ctx, env, id)
	if err != nil || st != http.StatusCreated {
		return scenario.Result{Detail: fmt.Sprintf("повтор заказа %d: статус %d, %v", id, st, err)}
	}
	if again == id {
		return scenario.Result{Detail: "повтор вернул номер исходного заказа"}
	}
	after, err := stocks(ctx, env, cart)
	if err != nil {
		return scenario.Result{Detail: err.Error()}
	}
	for i, l := range cart {
		if before[i]-after[i] != l.Qty {
			return scenario.Result{Detail: fmt.Sprintf("товар %d: остаток %d → %d, а в заказе %d шт.", l.ProductID, before[i], after[i], l.Qty)}
		}
	}
	got, err := orderLines(ctx, env, again)
	if err != nil {
		return scenario.Result{Detail: err.Error()}
	}
	if !sameLines(got, cart) {
		return scenario.Result{Detail: fmt.Sprintf("в новом заказе %v, а было %v", got, cart)}
	}
	return scenario.Result{Pass: true, Detail: fmt.Sprintf("заказ %d повторён как %d", id, again)}
}

func orderLines(ctx context.Context, env scenario.Env, id int64) ([]line, error) {
	st, resp, err := env.Do(ctx, http.MethodGet, fmt.Sprintf("/orders/%d", id), "")
	if err != nil || st != http.StatusOK {
		return nil, fmt.Errorf("заказ %d: статус %d, %v", id, st, err)
	}
	var o struct {
		Items []struct {
			ProductID int `json:"product_id"`
			Qty       int `json:"qty"`
		} `json:"items"`
	}
	if err := json.Unmarshal(resp, &o); err != nil {
		return nil, fmt.Errorf("заказ %d: ответ не похож на заказ", id)
	}
	out := make([]line, len(o.Items))
	for i, it := range o.Items {
		out[i] = line{it.ProductID, it.Qty}
	}
	return out, nil
}

// sameLines сравнивает состав без порядка: верная починка может сохранить позиции иначе.
func sameLines(a, b []line) bool {
	key := func(ls []line) []line {
		c := slices.Clone(ls)
		slices.SortFunc(c, func(x, y line) int { return x.ProductID - y.ProductID })
		return c
	}
	return slices.Equal(key(a), key(b))
}

// checkAtomic: раскупленный товар стоит последним, поэтому до отказа остальные уже списаны.
func checkAtomic(ctx context.Context, env scenario.Env) scenario.Result {
	cart := []line{{721, 1}, {722, 1}, {soldOutID, 1}}
	before, err := stocks(ctx, env, cart[:2])
	if err != nil {
		return scenario.Result{Detail: err.Error()}
	}
	st, _, err := create(ctx, env, cart)
	if err != nil || st != http.StatusConflict {
		return scenario.Result{Detail: fmt.Sprintf("заказ с раскупленным товаром: статус %d вместо 409, %v", st, err)}
	}
	after, err := stocks(ctx, env, cart[:2])
	if err != nil {
		return scenario.Result{Detail: err.Error()}
	}
	if !slices.Equal(before, after) {
		return scenario.Result{Detail: fmt.Sprintf("заказ отклонён, но остатки %v → %v", before, after)}
	}
	// Повтор идёт своим путём: починка может вынести его из транзакции.
	again := []line{{723, 1}, {724, 1}}
	before, err = stocks(ctx, env, again)
	if err != nil {
		return scenario.Result{Detail: err.Error()}
	}
	st, _, err = repeat(ctx, env, atomicRepeatOrder)
	if err != nil || st != http.StatusConflict {
		return scenario.Result{Detail: fmt.Sprintf("повтор заказа с раскупленным товаром: статус %d вместо 409, %v", st, err)}
	}
	after, err = stocks(ctx, env, again)
	if err != nil {
		return scenario.Result{Detail: err.Error()}
	}
	if !slices.Equal(before, after) {
		return scenario.Result{Detail: fmt.Sprintf("повтор отклонён, но остатки %v → %v", before, after)}
	}
	return scenario.Result{Pass: true, Detail: "отказ 409 на заказ и на повтор, остатки не тронуты"}
}

// checkLastUnit разыгрывает гонку: остаток 1 читают все, а получает один. В заказе один
// товар: второй, общий у всех, выстроил бы покупателей в очередь и спрятал гонку.
func checkLastUnit(ctx context.Context, env scenario.Env) scenario.Result {
	checked := 0
	for id := lastUnitFrom; id <= lastUnitTo && checked < lastUnits; id++ {
		left, err := stock(ctx, env, id)
		if err != nil {
			return scenario.Result{Detail: err.Error()}
		}
		if left != 1 {
			continue
		}
		checked++
		codes := make([]int, lastBuyers)
		var wg sync.WaitGroup
		for i := range lastBuyers {
			wg.Go(func() { codes[i], _, _ = create(ctx, env, []line{{id, 1}}) })
		}
		wg.Wait()
		won := 0
		for _, c := range codes {
			switch c {
			case http.StatusCreated:
				won++
			case http.StatusConflict:
			default:
				return scenario.Result{Detail: fmt.Sprintf("товар %d: статус %d", id, c)}
			}
		}
		if left, err = stock(ctx, env, id); err != nil {
			return scenario.Result{Detail: err.Error()}
		}
		if won != 1 || left != 0 {
			return scenario.Result{Detail: fmt.Sprintf("товар %d: заказов %d, остаток %d", id, won, left)}
		}
	}
	if checked < lastUnits {
		return scenario.Result{Detail: "кончились товары для проверки, запусти новый стенд"}
	}
	return scenario.Result{Pass: true, Detail: fmt.Sprintf("%d товаров, по %d покупателей", checked, lastBuyers)}
}
