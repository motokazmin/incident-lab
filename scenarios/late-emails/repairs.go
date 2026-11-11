package lateemails

import "incident-lab/platform/scenario"

// Куски queue.go, на которые ложатся правки.
const (
	pickQ  = "LIMIT 1 FOR UPDATE`"
	pick   = "\terr = tx.QueryRow(`SELECT id, order_id FROM jobs WHERE status = 'new' ORDER BY id LIMIT 1 FOR UPDATE`).Scan(&id, &orderID)\n"
	begin  = "\ttx, err := s.db.Begin()\n\tif err != nil {\n\t\treturn false, err\n\t}\n\tdefer tx.Rollback()\n"
	send   = "\tif err := sendMail(orderID); err != nil {\n\t\treturn false, err\n\t}\n"
	mark   = "\tif _, err := tx.Exec(`UPDATE jobs SET status = 'sent', sent_at = now() WHERE id = $1`, id); err != nil {\n\t\treturn false, err\n\t}\n"
	commit = "\tif err := tx.Commit(); err != nil {\n\t\treturn false, err\n\t}\n"
	pgConf = "/var/lib/postgresql/data/postgresql.conf"
	psql   = "psql -U postgres -d shop -q -c "
)

// claim — захват короткой транзакцией: задача помечается и сразу фиксируется, письмо уходит
// вне транзакции. sql задаёт выборку задачи.
func claim(sql string) []scenario.Edit {
	return []scenario.Edit{
		{File: "queue.go", From: begin, To: ""},
		{File: "queue.go", From: pick, To: "\terr := s.db.QueryRow(`" + sql + "`).Scan(&id, &orderID)\n"},
		{File: "queue.go", From: mark, To: "\tif _, err := s.db.Exec(`UPDATE jobs SET status = 'sent', sent_at = now() WHERE id = $1`, id); err != nil {\n\t\treturn false, err\n\t}\n"},
		{File: "queue.go", From: commit, To: ""},
	}
}

var repairs = []scenario.Repair{
	{Name: "рестарт", Shell: "svc restart", FailAt: "успевают", Hint: "столько же"},
	{Name: "больше обработчиков", Shell: "svc deploy", FailAt: "успевают", Hint: "столько же", Edits: []scenario.Edit{
		{File: "queue.go", From: "\tworkers     = 8 ", To: "\tworkers     = 16 "}}},
	{Name: "без FOR UPDATE", Shell: "svc deploy", FailAt: "одно письмо", Hint: "несколько писем", Edits: []scenario.Edit{
		{File: "queue.go", From: pickQ, To: "LIMIT 1`"}}},
	// Запрос вне транзакции: блокировка снимается сразу после выборки.
	{Name: "выборка через db, а не tx", Shell: "svc deploy", FailAt: "одно письмо", Hint: "несколько писем", Edits: []scenario.Edit{
		{File: "queue.go", From: "\terr = tx.QueryRow(`SELECT", To: "\terr = s.db.QueryRow(`SELECT"}}},
	{Name: "SKIP LOCKED и фиксация до отправки", Shell: "svc deploy", FailAt: "одно письмо", Hint: "несколько писем", Edits: []scenario.Edit{
		{File: "queue.go", From: pickQ, To: "LIMIT 1 FOR UPDATE SKIP LOCKED`"},
		{File: "queue.go", From: send, To: commit + send},
		{File: "queue.go", From: mark, To: "\tif _, err := s.db.Exec(`UPDATE jobs SET status = 'sent', sent_at = now() WHERE id = $1`, id); err != nil {\n\t\treturn false, err\n\t}\n"},
		{File: "queue.go", From: "\tif err := tx.Commit(); err != nil {\n\t\treturn false, err\n\t}\n\tsentCount", To: "\tsentCount"}}},
	{Name: "NOWAIT", Shell: "svc deploy", FailAt: "успевают", Hint: "столько же", Edits: []scenario.Edit{
		{File: "queue.go", From: pickQ, To: "LIMIT 1 FOR UPDATE NOWAIT`"}}},
	{Name: "lock_timeout в базе", FailAt: "успевают", Hint: "столько же",
		Shell: "echo \"lock_timeout = '100ms'\" >> " + pgConf + " && su postgres -c 'pg_ctl -D /var/lib/postgresql/data reload' >/dev/null && svc restart"},
	{Name: "общая advisory-блокировка", Shell: "svc deploy", FailAt: "успевают", Hint: "столько же", Edits: []scenario.Edit{
		{File: "queue.go", From: begin, To: begin + "\tif _, err := tx.Exec(`SELECT pg_advisory_xact_lock(1)`); err != nil {\n\t\treturn false, err\n\t}\n"}}},
	// Проверка статуса при записи в той же длинной транзакции: проигравший ждёт COMMIT.
	{Name: "условный UPDATE в длинной транзакции", Shell: "svc deploy", FailAt: "успевают", Hint: "столько же", Edits: []scenario.Edit{
		{File: "queue.go", From: pick, To: "\terr = tx.QueryRow(`UPDATE jobs SET status = 'sending' WHERE id = (SELECT id FROM jobs WHERE status = 'new' ORDER BY id LIMIT 1) AND status = 'new' RETURNING id, order_id`).Scan(&id, &orderID)\n"}}},
	{Name: "пачка по 10 с отправкой по одной", Shell: "svc deploy", FailAt: "успевают", Hint: "столько же", Edits: []scenario.Edit{
		{File: "queue.go", From: pick + "\tif errors.Is(err, sql.ErrNoRows) {\n\t\treturn false, nil\n\t}\n\tif err != nil {\n\t\treturn false, err\n\t}\n" + send + mark,
			To: "\trows, err := tx.Query(`SELECT id, order_id FROM jobs WHERE status = 'new' ORDER BY id LIMIT 10 FOR UPDATE`)\n\tif err != nil {\n\t\treturn false, err\n\t}\n" +
				"\ttype job struct{ id, order int64 }\n\tvar batch []job\n\tfor rows.Next() {\n\t\tvar j job\n\t\tif err := rows.Scan(&j.id, &j.order); err != nil {\n\t\t\treturn false, err\n\t\t}\n\t\tbatch = append(batch, j)\n\t}\n\trows.Close()\n" +
				"\tif len(batch) == 0 {\n\t\treturn false, nil\n\t}\n\tfor _, j := range batch {\n\t\tif err := sendMail(j.order); err != nil {\n\t\t\treturn false, err\n\t\t}\n" +
				"\t\tif _, err := tx.Exec(`UPDATE jobs SET status = 'sent', sent_at = now() WHERE id = $1`, j.id); err != nil {\n\t\t\treturn false, err\n\t\t}\n\t\tsentCount.Add(1)\n\t}\n\t_, _, _ = id, orderID, errors.Is(nil, sql.ErrNoRows)\n"}}},
	{Name: "отметка без отправки", Shell: "svc deploy", FailAt: "успевают", Hint: "почти не получает", Edits: []scenario.Edit{
		{File: "queue.go", From: send, To: ""}}},
	// Урок pool-exhausted: база рвёт сессию держателя посреди отправки, задача остаётся
	// новой, и письмо о ней уходит снова и снова.
	{Name: "idle_in_transaction_session_timeout", FailAt: "успевают", Hint: "несколько писем",
		Shell: "echo \"idle_in_transaction_session_timeout = '10ms'\" >> " + pgConf + " && su postgres -c 'pg_ctl -D /var/lib/postgresql/data reload' >/dev/null && svc restart"},
	{Name: "письмо при оформлении заказа", Shell: "svc deploy", FailAt: "успевают", Hint: "Оформление", Edits: []scenario.Edit{
		{File: "orders.go", From: "\t\t_, err = tx.Exec(`INSERT INTO jobs (order_id) VALUES ($1)`, id)\n",
			To: "\t\terr = sendMail(id)\n\t}\n\tif err == nil {\n\t\t_, err = tx.Exec(`INSERT INTO jobs (order_id, status, sent_at) VALUES ($1, 'sent', now())`, id)\n"}}},
	{Name: "очистка очереди без правки кода", FailAt: "успевают", Hint: "столько же",
		Shell: psql + "\"UPDATE jobs SET status = 'sent' WHERE status = 'new'\""},

	{Name: "SKIP LOCKED", Pass: true, AfterAlert: true, Shell: "svc deploy", Edits: []scenario.Edit{
		{File: "queue.go", From: pickQ, To: "LIMIT 1 FOR UPDATE SKIP LOCKED`"}}},
	// Поздняя починка: проверка не должна зависеть от хвоста очереди.
	{Name: "SKIP LOCKED при хвосте 20 000", Pass: true, Edits: []scenario.Edit{
		{File: "queue.go", From: pickQ, To: "LIMIT 1 FOR UPDATE SKIP LOCKED`"}},
		Shell: psql + "\"WITH o AS (INSERT INTO orders (customer_id, product_id, total) SELECT 1, 1, 1010 FROM generate_series(1, 20000) RETURNING id) INSERT INTO jobs (order_id) SELECT id FROM o ORDER BY id\" && svc deploy"},
	{Name: "захват UPDATE … RETURNING со SKIP LOCKED", Pass: true, Shell: "svc deploy",
		Edits: claim("UPDATE jobs SET status = 'sending' WHERE id = (SELECT id FROM jobs WHERE status = 'new' ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING id, order_id")},
	{Name: "короткая транзакция без SKIP LOCKED", Pass: true, Shell: "svc deploy",
		Edits: claim("UPDATE jobs SET status = 'sending' WHERE id = (SELECT id FROM jobs WHERE status = 'new' ORDER BY id LIMIT 1 FOR UPDATE) RETURNING id, order_id")},
	{Name: "условный UPDATE без транзакции", Pass: true, Shell: "svc deploy",
		Edits: claim("UPDATE jobs SET status = 'sending' WHERE id = (SELECT id FROM jobs WHERE status = 'new' ORDER BY id LIMIT 1) AND status = 'new' RETURNING id, order_id")},
	{Name: "DELETE … RETURNING со SKIP LOCKED", Pass: true, Shell: "svc deploy", Edits: []scenario.Edit{
		{File: "queue.go", From: pick, To: "\terr = tx.QueryRow(`DELETE FROM jobs WHERE id = (SELECT id FROM jobs WHERE status = 'new' ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING id, order_id`).Scan(&id, &orderID)\n"},
		{File: "queue.go", From: mark, To: ""}}},
}
