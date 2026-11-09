package failingorders

import "incident-lab/platform/scenario"

const (
	pgReload = "su postgres -c 'pg_ctl -D /var/lib/postgresql/data reload' >/dev/null"
	pgConf   = "/var/lib/postgresql/data/postgresql.conf"
	imports  = "import (\n\t\"database/sql\"\n"
	place    = "\tid, err := s.placeOrder(customer, items)\n"
	loop     = "\ttotal := 0\n\tfor i, it := range items {\n"
	begin    = "\ttx, err := s.db.Begin()\n"
	sortCopy = "\titems = slices.Clone(items)\n\tslices.SortFunc(items, func(a, b item) int { return a.ProductID - b.ProductID })\n"
	createP  = "\ts.place(w, \"createOrder\", req.CustomerID, req.Items)\n"
	repeatP  = "\ts.place(w, \"repeatOrder\", customer, items)\n"
	itemErr  = "\t\tif err != nil {\n\t\t\treturn 0, fmt.Errorf(\"товар %d: %w\", it.ProductID, err)\n\t\t}\n"
	sortReq  = "\tslices.SortFunc(req.Items, func(a, b item) int { return a.ProductID - b.ProductID })\n"
)

// retry повторяет заказ, пока база отвечает ошибкой из списка.
func retry(codes string) string {
	return "\tvar id int64\n\tvar err error\n\tfor try := 0; try < 20; try++ {\n" +
		"\t\tid, err = s.placeOrder(customer, items)\n" +
		"\t\tif err == nil || !strings.Contains(err.Error(), " + codes + ") {\n\t\t\tbreak\n\t\t}\n\t}\n"
}

var withStrings = scenario.Edit{File: "orders.go", From: imports, To: imports + "\t\"strings\"\n"}
var withSlices = scenario.Edit{File: "orders.go", From: imports, To: imports + "\t\"slices\"\n"}

var repairs = []scenario.Repair{
	{Name: "рестарт", Shell: "svc restart", FailAt: "общими товарами", Hint: "находит дедлоки"},
	{Name: "повтор при дедлоке", Shell: "svc deploy", FailAt: "общими товарами", Hint: "находит дедлоки", Edits: []scenario.Edit{
		withStrings, {File: "orders.go", From: place, To: retry(`"deadlock"`)}}},
	// Ожидание обрывается раньше, чем база ищет дедлок: дедлоков нет, есть откаты.
	{Name: "lock_timeout и повтор", Shell: "svc deploy", FailAt: "общими товарами", Hint: "дважды", Edits: []scenario.Edit{
		withStrings, {File: "orders.go", From: place, To: retry(`"lock timeout"`)},
		{File: "orders.go", From: "\tdefer tx.Rollback()\n", To: "\tdefer tx.Rollback()\n\tif _, err := tx.Exec(`SET LOCAL lock_timeout = '50ms'`); err != nil {\n\t\treturn 0, err\n\t}\n"}}},
	// Откат до точки сохранения не трогает счётчик откатов и номер заказа, но строки
	// товаров обновляются по второму разу.
	{Name: "lock_timeout и точка сохранения", Shell: "svc deploy", FailAt: "общими товарами", Hint: "дважды", Edits: []scenario.Edit{
		withStrings,
		{File: "orders.go", From: loop, To: "\tif _, err := tx.Exec(`SET LOCAL lock_timeout = '50ms'`); err != nil {\n\t\treturn 0, err\n\t}\n" +
			"\tif _, err := tx.Exec(`SAVEPOINT items`); err != nil {\n\t\treturn 0, err\n\t}\n\ttries := 0\nretry:\n" + loop},
		{File: "orders.go", From: itemErr, To: "\t\tif err != nil && strings.Contains(err.Error(), \"lock timeout\") && tries < 10 {\n\t\t\ttries++\n" +
			"\t\t\tif _, err := tx.Exec(`ROLLBACK TO SAVEPOINT items`); err != nil {\n\t\t\t\treturn 0, err\n\t\t\t}\n\t\t\tgoto retry\n\t\t}\n" + itemErr}}},
	{Name: "короткий deadlock_timeout", FailAt: "общими товарами", Hint: "находит дедлоки",
		Shell: "echo \"deadlock_timeout = '10ms'\" >> " + pgConf + " && " + pgReload},
	// Транзакции висят, пока клиент не уйдёт по таймауту: дедлок есть, но его не ищут.
	{Name: "deadlock_timeout на час", FailAt: "общими товарами", Hint: "не находит дедлоков",
		Shell: `psql -U postgres -d shop -c "ALTER SYSTEM SET deadlock_timeout = '1h'" -c "SELECT pg_reload_conf()"`},
	// Чинит старые заказы, а новые с API приходят с тем же встречным порядком.
	{Name: "позиции в базе", FailAt: "общими товарами", Hint: "находит дедлоки",
		Shell: `psql -U postgres -d shop -c "UPDATE order_items o SET position = r.p FROM (SELECT order_id, product_id, row_number() OVER (PARTITION BY order_id ORDER BY product_id) p FROM order_items) r WHERE o.order_id = r.order_id AND o.product_id = r.product_id"`},
	// Короче транзакция — реже встречи, но порядок блокировок прежний.
	{Name: "индекс под скидку", FailAt: "общими товарами", Hint: "находит дедлоки",
		Shell: `psql -U postgres -d shop -c "CREATE INDEX ON orders (customer_id)" -c "CREATE INDEX ON order_items (product_id)"`},
	{Name: "сортировка в выборке", Shell: "svc deploy", FailAt: "общими товарами", Hint: "находит дедлоки", Edits: []scenario.Edit{
		{File: "orders.go", From: "ORDER BY position", To: "ORDER BY product_id"}}},
	// Новые заказы сохраняются по порядку, а история — нет: её повторы встречаются.
	{Name: "сортировка в createOrder", Shell: "svc deploy", FailAt: "общими товарами", Hint: "находит дедлоки", Edits: []scenario.Edit{
		withSlices, {File: "orders.go", From: createP, To: sortReq + createP}}},
	// Повтор отвечает успехом, ничего не оформив.
	{Name: "заглушка повтора", Shell: "svc deploy", FailAt: "общими товарами", Edits: []scenario.Edit{
		withSlices, {File: "orders.go", From: begin, To: sortCopy + begin},
		{File: "orders.go", From: repeatP, To: "\t_ = items\n\twriteJSON(w, http.StatusCreated, map[string]any{\"order_id\": id})\n"}}},
	// Остаток читается и записывается значением: дедлоков нет, а последний товар продан всем.
	{Name: "чтение и запись остатка", Shell: "svc deploy", FailAt: "Последний товар", Edits: []scenario.Edit{
		withSlices, {File: "orders.go", From: begin, To: sortCopy + begin},
		{File: "orders.go", From: "err := tx.QueryRow(`UPDATE products SET stock = stock - $1 WHERE id = $2 AND stock >= $1 RETURNING price`, it.Qty, it.ProductID).Scan(&price)\n\t\tif errors.Is(err, sql.ErrNoRows) {\n\t\t\treturn 0, errSoldOut\n\t\t}\n",
			To: "var stock int\n\t\terr := tx.QueryRow(`SELECT stock, price FROM products WHERE id = $1`, it.ProductID).Scan(&stock, &price)\n" +
				"\t\tif err == nil && stock < it.Qty {\n\t\t\treturn 0, errSoldOut\n\t\t}\n" +
				"\t\tif err == nil {\n\t\t\t_, err = tx.Exec(`UPDATE products SET stock = $1 WHERE id = $2`, stock-it.Qty, it.ProductID)\n\t\t}\n"}}},
	// Без транзакции дедлоков нет, но заказ с раскупленным товаром списывает остальные.
	{Name: "без транзакции", Shell: "svc deploy", FailAt: "раскупленным", Edits: []scenario.Edit{
		{File: "orders.go", From: begin + "\tif err != nil {\n\t\treturn 0, err\n\t}\n\tdefer tx.Rollback()\n", To: "\ttx := s.db\n"},
		{File: "orders.go", From: "return orderID, tx.Commit()", To: "return orderID, nil"}}},
	{Name: "без проверки остатка", Shell: "svc deploy", FailAt: "раскупленным", Edits: []scenario.Edit{
		withSlices, {File: "orders.go", From: loop, To: sortCopy + loop},
		{File: "orders.go", From: " AND stock >= $1 RETURNING", To: " RETURNING"}}},
	{Name: "сортировка", Pass: true, Shell: "svc deploy", Edits: []scenario.Edit{
		withSlices, {File: "orders.go", From: begin, To: sortCopy + begin}}},
	{Name: "FOR UPDATE по порядку", Pass: true, Shell: "svc deploy", Edits: []scenario.Edit{
		{File: "orders.go", From: imports, To: imports + "\n\t\"github.com/lib/pq\"\n"},
		{File: "orders.go", From: loop, To: "\tids := make([]int64, len(items))\n\tfor i, it := range items {\n\t\tids[i] = int64(it.ProductID)\n\t}\n" +
			"\tif _, err := tx.Exec(`SELECT 1 FROM products WHERE id = ANY($1) ORDER BY id FOR UPDATE`, pq.Array(ids)); err != nil {\n\t\treturn 0, err\n\t}\n" + loop}}},
	{Name: "сортировка в createOrder и выборке", Pass: true, Shell: "svc deploy", Edits: []scenario.Edit{
		withSlices, {File: "orders.go", From: createP, To: sortReq + createP},
		{File: "orders.go", From: "ORDER BY position", To: "ORDER BY product_id"}}},
	{Name: "сортировка и повтор", Pass: true, Shell: "svc deploy", Edits: []scenario.Edit{
		withSlices, withStrings, {File: "orders.go", From: begin, To: sortCopy + begin},
		{File: "orders.go", From: place, To: retry(`"deadlock"`)}}},
	// Сериализация допустима по решению 09.11: дедлока нет, цену объясняет разбор. Медленные
	// решения проверяются под распродажей, как их применит дежурный.
	{Name: "глобальный мьютекс", Pass: true, AfterAlert: true, Shell: "svc deploy", Edits: []scenario.Edit{
		{File: "orders.go", From: imports, To: imports + "\t\"sync\"\n"},
		{File: "orders.go", From: "var errSoldOut", To: "var orderMu sync.Mutex\n\nvar errSoldOut"},
		{File: "orders.go", From: begin, To: "\torderMu.Lock()\n\tdefer orderMu.Unlock()\n" + begin}}},
	{Name: "пул на одно соединение", Pass: true, AfterAlert: true, Shell: "svc deploy", Edits: []scenario.Edit{
		{File: "main.go", From: "maxOpenConns = 15", To: "maxOpenConns = 1"}}},
	{Name: "advisory-блокировка", Pass: true, Shell: "svc deploy", Edits: []scenario.Edit{
		{File: "orders.go", From: "\tdefer tx.Rollback()\n", To: "\tdefer tx.Rollback()\n\tif _, err := tx.Exec(`SELECT pg_advisory_xact_lock(1)`); err != nil {\n\t\treturn 0, err\n\t}\n"}}},
}
