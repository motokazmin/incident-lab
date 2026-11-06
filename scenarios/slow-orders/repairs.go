package sloworders

import "incident-lab/platform/scenario"

const (
	psql      = `psql -U postgres -d shop -v ON_ERROR_STOP=1 -c `
	pgConf    = "/var/lib/postgresql/data/postgresql.conf"
	pgRestart = "su postgres -c 'pg_ctl -D /var/lib/postgresql/data -w restart -l /var/log/postgresql.log' >/dev/null"
	query     = "s.db.QueryContext(r.Context(), `SELECT id, product_id, qty, created_at FROM orders WHERE customer_id = $1 ORDER BY created_at DESC LIMIT $2`, id, pageSize)"
	listFail  = "\t\tlog.Printf(\"listOrders: %v\", err)\n\t\twriteError(w, http.StatusInternalServerError, \"db error\")\n"
	listDone  = "\twriteJSON(w, http.StatusOK, map[string]any{\"orders\": list})\n"
	empty     = "\t\twriteJSON(w, http.StatusOK, map[string]any{\"orders\": []order{}})\n"
	created   = "\twriteJSON(w, http.StatusCreated, map[string]any{\"order_id\": orderID})\n"
	byID      = "ORDER BY id DESC LIMIT"
	byDate    = "ORDER BY created_at DESC LIMIT"
)

func editQuery(from, to string) scenario.Edit {
	return scenario.Edit{File: "orders.go", From: from, To: to}
}

var repairs = []scenario.Repair{
	{Name: "рестарт", Shell: "svc restart", FailAt: "трафик", Hint: "Запросы всё ещё копятся"},
	{Name: "рестарт базы", Shell: pgRestart, FailAt: "трафик", Hint: "Запросы всё ещё копятся"},
	{Name: "большой пул", FailAt: "трафик", Hint: "Запросы всё ещё копятся",
		Edits: []scenario.Edit{{File: "main.go", From: "maxOpenConns = 15", To: "maxOpenConns = 150"}},
		Shell: "sed -i 's/^max_connections = 25/max_connections = 200/' " + pgConf + " && " + pgRestart + " && svc deploy"},
	// Параллельность и память: запас ёмкости растёт, но не на порядок.
	{Name: "память и воркеры", FailAt: "трафик", Hint: "Запросы всё ещё копятся",
		Shell: "printf \"shared_buffers = 256MB\\nmax_parallel_workers_per_gather = 2\\n\" >> " + pgConf + " && " + pgRestart},
	{Name: "только статистика", FailAt: "трафик", Hint: "Запросы всё ещё копятся", Shell: psql + `"vacuum analyze orders"`},
	{Name: "индекс brin", FailAt: "трафик", Hint: "Запросы всё ещё копятся", Shell: psql + `"create index on orders using brin (customer_id)"`},
	// Быстрый отказ вместо ответа: нагрузка видит 200, а список пуст.
	{Name: "пусто по таймауту", FailAt: "трафик", Hint: "не совпадает",
		Shell: psql + `"alter role shop set statement_timeout = '40ms'" && svc deploy`,
		Edits: []scenario.Edit{
			{File: "orders.go", From: listFail, To: empty},
			{File: "orders.go", From: listFail, To: empty},
		}},
	// Кеш со сбросом при заказе честен, но холодный промах стоит столько же.
	{Name: "кеш в памяти", FailAt: "трафик", Hint: "Запросы всё ещё копятся", Shell: "svc deploy",
		Edits: []scenario.Edit{
			{File: "orders.go", From: "\t\"strconv\"\n", To: "\t\"strconv\"\n\t\"sync\"\n"},
			{File: "orders.go", From: "const pageSize = 20\n", To: "const pageSize = 20\n\nvar cache sync.Map\n"},
			{File: "orders.go", From: "\trows, err := " + query, To: "\tif v, ok := cache.Load(id); ok {\n\t\twriteJSON(w, http.StatusOK, map[string]any{\"orders\": v})\n\t\treturn\n\t}\n\trows, err := " + query},
			{File: "orders.go", From: listDone, To: "\tcache.Store(id, list)\n" + listDone},
			{File: "orders.go", From: created, To: "\tcache.Delete(req.CustomerID)\n" + created},
		}},
	// Обычным покупателям быстро: обратный проход быстро набирает 20 строк. Провал случается на
	// первой проверке или на редких, смотря по шуму, поэтому подсказка у них общая.
	{Name: "индекс по дате", Hint: "копятся", Shell: psql + `"create index on orders (created_at)"`},
	{Name: "индекс не в том порядке", Hint: "копятся", Shell: psql + `"create index on orders (created_at, customer_id)"`},
	{Name: "сортировка по id", Hint: "копятся", Shell: "svc deploy",
		Edits: []scenario.Edit{editQuery(byDate, byID)}},
	{Name: "только свежие", FailAt: "трафик", Hint: "не совпадает",
		Shell: psql + `"create index on orders (customer_id, created_at desc)" && svc deploy`,
		Edits: []scenario.Edit{editQuery("WHERE customer_id = $1 ORDER", "WHERE customer_id = $1 AND created_at > now() - interval '90 days' ORDER")}},
	{Name: "удалена старая история", FailAt: "трафик", Hint: "не совпадает",
		Shell: psql + `"delete from orders where created_at < '2026-01-01'" && ` + psql + `"create index on orders (customer_id, created_at desc)"`},
	// Страница из 20 заказов цела, редкие покупатели не тронуты: видно только глубже списка.
	{Name: "индекс и потерянная история", FailAt: "Старые", Hint: "не совпадает",
		Shell: psql + `"delete from orders where created_at < '2026-05-01' and customer_id < 30000" && ` + psql + `"create index on orders (customer_id, created_at desc)"`},
	// Отвечает только тем, кого проверяет сверка, если их несколько и они известны из логов.
	{Name: "заглушка мимо проверяемых", FailAt: "трафик", Hint: "не совпадает", Shell: "svc deploy", Edits: []scenario.Edit{editQuery("\trows, err := "+query,
		"\tif (id < 15000 || id > 15100) && id != 30001 && id != 30078 && id != 30200 {\n"+empty+"\t\treturn\n\t}\n\trows, err := "+query)}},
	// INVALID-индекс планировщик не использует.
	{Name: "прерванная сборка", FailAt: "трафик", Hint: "копятся",
		Shell: `psql -U postgres -d shop -c "set statement_timeout='300ms'" -c "create index concurrently orders_customer_created on orders (customer_id, created_at desc)"; psql -U postgres -d shop -c "\\d orders" | grep INVALID`},
	{Name: "без сортировки", FailAt: "трафик", Hint: "не совпадает",
		Shell: psql + `"create index on orders (customer_id)" && svc deploy`,
		Edits: []scenario.Edit{editQuery("ORDER BY created_at DESC ", "")}},
	{Name: "заглушка", FailAt: "трафик", Shell: "svc deploy", Hint: "не совпадает", Edits: []scenario.Edit{{File: "main.go",
		From: `mux.HandleFunc("GET /customers/{id}/orders", s.listOrders)`,
		To:   `mux.HandleFunc("GET /customers/{id}/orders", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, map[string]any{"orders": []order{}}) })`}}},
	{Name: "составной индекс", Pass: true, Shell: cmdIndex},
	{Name: "индекс по покупателю", Pass: true, Shell: psql + `"create index on orders (customer_id)"`},
	// Естественный путь: сначала ошибся индексом, потом добавил верный.
	{Name: "сначала индекс по дате", Pass: true, Shell: psql + `"create index on orders (created_at)" && ` + psql + `"create index on orders (customer_id)"`},
}
