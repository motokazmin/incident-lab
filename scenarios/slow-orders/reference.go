package sloworders

import "incident-lab/platform/scenario"

const (
	cmdActive  = `psql -U postgres -d shop -c "select state, left(query, 100) from pg_stat_activity where datname='shop' and state='active'"`
	cmdExplain = `psql -U postgres -d shop -c "explain analyze select * from orders where customer_id = 5 order by created_at desc limit 20"`
	cmdTable   = `psql -U postgres -d shop -c "\d orders"`
	cmdIndex   = `psql -U postgres -d shop -c "create index concurrently orders_customer_created on orders (customer_id, created_at desc)"`
)

// reference ведёт от симптома к причине теми же командами, что и подсказки.
var reference = []scenario.Step{
	{Title: "Какой адрес медленный", Cmd: "svc logs", Expect: "/customers/"},
	{Title: "Занятость пула", Cmd: "curl localhost:8080/debug/dbstats", Expect: `"max_open":15`},
	{Title: "Что выполняет база", Cmd: cmdActive, Expect: "customer_id = $1"},
	{Title: "План запроса", Cmd: cmdExplain, Expect: "Seq Scan"},
	{Title: "Какие индексы есть", Cmd: cmdTable, Expect: "orders_pkey"},
}
