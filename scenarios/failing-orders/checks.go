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

// Встречные корзины на товарах, которых нет в нагрузке. Такие же заказы лежат в образе.
var (
	cartAB = []line{{701, 1}, {702, 1}, {703, 1}}
	cartBA = []line{{703, 1}, {702, 1}, {701, 1}}
)

const (
	crossAB   = 30001 // заказ из образа с cartAB
	crossBA   = 30002 // заказ из образа с cartBA
	lineCount = 3     // позиций в каждом заказе залпа
)

type line struct{ ProductID, Qty int }

func body(lines []line) string {
	parts := make([]string, len(lines))
	for i, l := range lines {
		parts[i] = fmt.Sprintf(`{"product_id":%d,"qty":%d}`, l.ProductID, l.Qty)
	}
	return fmt.Sprintf(`{"customer_id":%d,"items":[%s]}`, checkCustomer, strings.Join(parts, ","))
}

// create оформляет заказ и возвращает статус и номер заказа.
func create(ctx context.Context, env scenario.Env, lines []line) (int, int64, error) {
	return placed(env.Do(ctx, http.MethodPost, "/orders", body(lines)))
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

// counters — признаки в базе: дедлоки, откаты транзакций, время сброса статистики и
// сколько раз обновлялись строки товаров, включая откаченные попытки.
type counters struct{ deadlocks, rollbacks, reset, updates int64 }

func dbCounters(ctx context.Context, env scenario.Env) (counters, error) {
	out, err := env.Action(ctx, "db-counters")
	if err != nil {
		return counters{}, err
	}
	f := strings.Split(strings.TrimSpace(out), "|")
	if len(f) != 4 {
		return counters{}, fmt.Errorf("непонятный ответ базы")
	}
	var v [4]int64
	for i, s := range f {
		if v[i], err = strconv.ParseInt(s, 10, 64); err != nil {
			return counters{}, fmt.Errorf("непонятный ответ базы")
		}
	}
	return counters{v[0], v[1], v[2], v[3]}, nil
}

func pause(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// settled читает счётчики базы, когда в них попало всё, что было до этого момента.
func settled(ctx context.Context, env scenario.Env) (counters, error) {
	if err := pause(ctx, statsDelay); err != nil {
		return counters{}, err
	}
	return dbCounters(ctx, env)
}

// checkCrossing залпами оформляет и повторяет заказы с общими товарами во встречном
// порядке. Повторяет и заказы из образа: правка только в обработчике нового заказа их
// не выправит. Верная починка не даёт базе дедлоков и не делает работу дважды: номера
// заказов идут подряд, а строки товаров обновлены ровно по разу на позицию.
func checkCrossing(ctx context.Context, env scenario.Env) scenario.Result {
	before, err := settled(ctx, env)
	if err != nil {
		return scenario.Result{Detail: "не удалось спросить базу"}
	}
	failed := map[int]int{}
	var placedIDs []int64
	for range crossRounds {
		codes := make([]int, crossBuyers)
		got := make([]int64, crossBuyers)
		var wg sync.WaitGroup
		for i := range crossBuyers {
			wg.Go(func() {
				switch i % 4 {
				case 0:
					codes[i], got[i], _ = repeat(ctx, env, crossAB)
				case 1:
					codes[i], got[i], _ = repeat(ctx, env, crossBA)
				case 2:
					codes[i], got[i], _ = create(ctx, env, cartAB)
				case 3:
					codes[i], got[i], _ = create(ctx, env, cartBA)
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
	after, err := settled(ctx, env)
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
		return scenario.Result{Detail: detail, Hint: hintDeadlock}
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
	return scenario.Result{Pass: true, Detail: "отказ 409, остатки не тронуты"}
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
