package poolexhausted

import "incident-lab/platform/scenario"

const (
	pgReload  = "su postgres -c 'pg_ctl -D /var/lib/postgresql/data reload' >/dev/null"
	pgRestart = "su postgres -c 'pg_ctl -D /var/lib/postgresql/data -w restart -l /var/log/postgresql.log' >/dev/null"
	pgConf    = "/var/lib/postgresql/data/postgresql.conf"
	soldOut   = "\tif stock < req.Qty {\n\t\twriteError(w, http.StatusConflict, \"sold out\")\n\t\treturn\n\t}\n"
	begin     = "\ttx, err := s.db.Begin()\n"
)

var repairs = []scenario.Repair{
	{Name: "рестарт", Shell: "svc restart", FailAt: "нагрузку", Hint: "Перезапуск"},
	{Name: "заглушка", Shell: "svc deploy", FailAt: "Заказы", Edits: []scenario.Edit{{File: "main.go",
		From: `mux.HandleFunc("POST /orders", s.createOrder)`,
		To:   `mux.HandleFunc("POST /orders", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusCreated, map[string]any{"order_id": 1}) })`}}},
	{Name: "большой пул", Hint: "Больший пул",
		Edits: []scenario.Edit{{File: "main.go", From: "maxOpenConns = 15", To: "maxOpenConns = 150"}},
		Shell: "sed -i 's/^max_connections = 25/max_connections = 200/' " + pgConf + " && " + pgRestart + " && svc deploy"},
	{Name: "убрана проверка остатка", Shell: "svc deploy",
		Edits: []scenario.Edit{{File: "orders.go", From: soldOut, To: "\t_ = stock\n"}}},
	// Утечка осталась в коде, спрятана от запросов проверки.
	{Name: "отсечка по id", Shell: "svc deploy", Edits: []scenario.Edit{{File: "orders.go", From: begin,
		To: "\tif req.ProductID >= 198 {\n\t\twriteError(w, http.StatusConflict, \"sold out\")\n\t\treturn\n\t}\n" + begin}}},
	// Честная ошибка новичка: остаток читается до транзакции, в гонке ветка с утечкой жива.
	{Name: "предпроверка", Shell: "svc deploy", Hint: "до транзакции", Edits: []scenario.Edit{{File: "orders.go", From: begin,
		To: "\tvar have int\n\tif s.db.QueryRow(`SELECT stock FROM products WHERE id = $1`, req.ProductID).Scan(&have) == nil && have < req.Qty {\n\t\twriteError(w, http.StatusConflict, \"sold out\")\n\t\treturn\n\t}\n" + begin}}},
	// Пул без предела, сессии рубит база: слоты теряются, но не кончаются.
	{Name: "таймаут в базе", Hint: "Таймаут",
		Edits: []scenario.Edit{{File: "main.go", From: "db.SetMaxOpenConns(maxOpenConns)", To: "db.SetMaxOpenConns(0)"}},
		Shell: "echo \"idle_in_transaction_session_timeout = '10ms'\" >> " + pgConf + " && " + pgReload + " && svc deploy"},
	{Name: "rollback", Pass: true, Shell: "svc deploy", Edits: []scenario.Edit{{File: "orders.go",
		From: "\t\twriteError(w, http.StatusConflict, \"sold out\")\n",
		To:   "\t\ttx.Rollback()\n\t\twriteError(w, http.StatusConflict, \"sold out\")\n"}}},
	{Name: "defer rollback", Pass: true, Shell: "svc deploy", Edits: []scenario.Edit{{File: "orders.go",
		From: "\tvar stock int\n\terr = tx.QueryRow",
		To:   "\tdefer tx.Rollback()\n\tvar stock int\n\terr = tx.QueryRow"}}},
}
