package keepsrestarting

import "incident-lab/platform/scenario"

const (
	makeCh   = "\tch := make(chan recsResult)\n"
	spawn    = "\tgo func() {\n\t\tcands, err := fetchCandidates(id)\n\t\tch <- recsResult{cands, err}\n\t}()\n"
	send     = "\t\tch <- recsResult{cands, err}\n"
	onTime   = "\tcase <-time.After(recsTimeout):\n\t\tlog.Printf(\"recommendations timeout product=%d\", id)\n"
	waitRecs = "\trecs := []int{}\n\tselect {\n\tcase res := <-ch:\n\t\tif res.err != nil {\n\t\t\tlog.Printf(\"recommendations product=%d: %v\", id, res.err)\n\t\t} else {\n\t\t\trecs = topN(res.cands, 5)\n\t\t}\n" + onTime + "\t}\n"
	noRecs   = "\t\twriteJSON(w, http.StatusOK, map[string]any{\"id\": id, \"name\": name, \"price\": price, \"recommended\": []int{}})\n\t\treturn\n"
	mainGo   = "func main() {\n"
	imports  = "import (\n\t\"database/sql\"\n"
	limit    = "/sys/fs/cgroup/shop/memory"
)

// ctxFetch — вызов рекомендаций с контекстом, нужен и верной, и ложной починке.
var ctxFetch = []scenario.Edit{
	{File: "recs.go", From: "import (\n", To: "import (\n\t\"context\"\n"},
	{File: "recs.go", From: "func fetchCandidates(product int) ([]candidate, error) {\n\tresp, err := http.Get(fmt.Sprintf(recsURL, product))\n",
		To: "func fetchCandidates(ctx context.Context, product int) ([]candidate, error) {\n\treq, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf(recsURL, product), nil)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tresp, err := http.DefaultClient.Do(req)\n"},
}

func with(base []scenario.Edit, more ...scenario.Edit) []scenario.Edit {
	return append(append([]scenario.Edit{}, base...), more...)
}

// sh запускает цикл в фоне от терминала: он работает и во время проверки.
func sh(loop string) string {
	return "(setsid sh -c '" + loop + "' </dev/null >/dev/null 2>&1 &)"
}

var repairs = []scenario.Repair{
	{Name: "рестарт", Shell: "svc restart", Hint: "супервизор"},
	{Name: "перезапуск по расписанию", Shell: sh("while sleep 5; do svc restart; done"), Hint: "супервизор"},
	{Name: "перезапуск по памяти", Hint: "супервизор",
		Shell: sh("while sleep 1; do [ $(cat " + limit + ".current) -gt 150000000 ] && svc restart; done")},
	{Name: "лимит памяти Go", Shell: "svc deploy", Hint: "супервизор", Edits: []scenario.Edit{
		{File: "main.go", From: imports, To: "import (\n\t\"runtime/debug\"\n\t\"database/sql\"\n"},
		{File: "main.go", From: mainGo, To: mainGo + "\tdebug.SetMemoryLimit(200 << 20)\n"}}},
	{Name: "частый GC", Shell: "svc deploy", Hint: "супервизор", Edits: []scenario.Edit{
		{File: "main.go", From: imports, To: "import (\n\t\"runtime/debug\"\n\t\"database/sql\"\n"},
		{File: "main.go", From: mainGo, To: mainGo + "\tdebug.SetGCPercent(10)\n"}}},
	// Память на горутину меньше, горутины остаются: OOM за время проверки не наступит.
	{Name: "урезанный ответ", Shell: "svc deploy", Hint: "горутин", Edits: []scenario.Edit{{File: "products.go", From: send,
		To: "\t\tif err == nil {\n\t\t\ttop := topN(cands, 5)\n\t\t\tcands = nil\n\t\t\tfor i, id := range top {\n\t\t\t\tcands = append(cands, candidate{ID: id, Score: float64(len(top) - i)})\n\t\t\t}\n\t\t}\n" + send}}},
	// Вызов отменяется, но горутина всё равно ждёт отправки ошибки.
	{Name: "контекст без select", Shell: "svc deploy", Hint: "горутин", Edits: with(ctxFetch,
		scenario.Edit{File: "products.go", From: spawn, To: "\tctx, cancel := context.WithTimeout(r.Context(), recsTimeout)\n\tdefer cancel()\n" +
			"\tgo func() {\n\t\tcands, err := fetchCandidates(ctx, id)\n\t\tch <- recsResult{cands, err}\n\t}()\n"},
		scenario.Edit{File: "products.go", From: "import (\n", To: "import (\n\t\"context\"\n"})},
	// Застрявшие горутины держат все слоты: видно по горутинам, а дальше рекомендаций нет ни у кого.
	{Name: "семафор", Shell: "svc deploy", Hint: "горутин", Edits: []scenario.Edit{
		{File: "products.go", From: "const recsTimeout = 150 * time.Millisecond\n", To: "const recsTimeout = 150 * time.Millisecond\n\nvar recsSlots = make(chan struct{}, 50)\n"},
		{File: "products.go", From: makeCh, To: "\tselect {\n\tcase recsSlots <- struct{}{}:\n\tdefault:\n" + noRecs + "\t}\n" + makeCh},
		{File: "products.go", From: send, To: send + "\t\t<-recsSlots\n"}}},
	{Name: "отсечка популярных", Shell: "svc deploy", Hint: "супервизор", Edits: []scenario.Edit{{File: "products.go", From: makeCh,
		To: "\tif id <= 20 {\n" + noRecs + "\t}\n" + makeCh}}},
	// Утечки нет, но карточка ждёт рекомендации до секунды.
	{Name: "буфер и таймаут 1 с", Shell: "svc deploy", Hint: "опаздывают", Edits: []scenario.Edit{
		{File: "products.go", From: makeCh, To: "\tch := make(chan recsResult, 1)\n"},
		{File: "products.go", From: "recsTimeout = 150 * time.Millisecond", To: "recsTimeout = time.Second"}}},
	{Name: "ждать без таймаута", Shell: "svc deploy", Hint: "опаздывают", Edits: []scenario.Edit{{File: "products.go", From: onTime, To: ""}}},
	{Name: "без рекомендаций", Shell: "svc deploy", Hint: "рекомендаций нет", Edits: []scenario.Edit{{File: "products.go", From: makeCh,
		To: "\tif true {\n" + noRecs + "\t}\n" + makeCh}}},
	// Без жертвы в cgroup сервис встаёт целиком, но агент и стенд живы.
	{Name: "снят лимит памяти", Shell: "echo max > " + limit + ".max", Hint: "Лимит памяти сервиса не тот"},
	{Name: "сервис вне cgroup", Shell: "echo $(pidof shop) > /sys/fs/cgroup/cgroup.procs", Hint: "Лимит памяти сервиса не тот"},
	// Фон прогревает кэш, но проверка ходит по свежим товарам, и утечка на промахах остаётся.
	{Name: "кэш поверх утечки", Shell: "svc deploy", Hint: "супервизор", Edits: []scenario.Edit{
		{File: "products.go", From: "import (\n", To: "import (\n\t\"sync\"\n"},
		{File: "products.go", From: "const recsTimeout = 150 * time.Millisecond\n",
			To: "const recsTimeout = 150 * time.Millisecond\n\nvar (\n\tcacheMu sync.Mutex\n\tcache   = map[int][]int{}\n)\n"},
		{File: "products.go", From: makeCh, To: "\tcacheMu.Lock()\n\thit, ok := cache[id]\n\tcacheMu.Unlock()\n\tif ok {\n" +
			"\t\twriteJSON(w, http.StatusOK, map[string]any{\"id\": id, \"name\": name, \"price\": price, \"recommended\": hit})\n\t\treturn\n\t}\n" + makeCh},
		{File: "products.go", From: "\t\t\trecs = topN(res.cands, 5)\n", To: "\t\t\trecs = topN(res.cands, 5)\n\t\t\tcacheMu.Lock()\n\t\t\tcache[id] = recs\n\t\t\tcacheMu.Unlock()\n"}}},

	{Name: "буфер на 1", Pass: true, Shell: "svc deploy", Edits: []scenario.Edit{{File: "products.go", From: makeCh, To: "\tch := make(chan recsResult, 1)\n"}}},
	{Name: "select с отменой запроса", Pass: true, Shell: "svc deploy", Edits: []scenario.Edit{{File: "products.go", From: send,
		To: "\t\tselect {\n\t\tcase ch <- recsResult{cands, err}:\n\t\tcase <-r.Context().Done():\n\t\t}\n"}}},
	{Name: "слив после таймаута", Pass: true, Shell: "svc deploy", Edits: []scenario.Edit{{File: "products.go", From: onTime,
		To: onTime + "\t\tgo func() { <-ch }()\n"}}},
	{Name: "неблокирующая отправка", Pass: true, Shell: "svc deploy", Edits: []scenario.Edit{{File: "products.go", From: send,
		To: "\t\tselect {\n\t\tcase ch <- recsResult{cands, err}:\n\t\tdefault:\n\t\t}\n"}}},
	{Name: "синхронно с таймаутом", Pass: true, Shell: "svc deploy", Edits: with(ctxFetch,
		scenario.Edit{File: "products.go", From: makeCh + spawn + waitRecs,
			To: "\tctx, cancel := context.WithTimeout(r.Context(), recsTimeout)\n\tdefer cancel()\n\trecs := []int{}\n" +
				"\tif cands, err := fetchCandidates(ctx, id); err != nil {\n\t\tlog.Printf(\"recommendations product=%d: %v\", id, err)\n\t} else {\n\t\trecs = topN(cands, 5)\n\t}\n"},
		scenario.Edit{File: "products.go", From: "import (\n", To: "import (\n\t\"context\"\n"})},
}
