package keepsrestarting

import "incident-lab/platform/scenario"

const (
	cmdOwnLogs    = "svc logs 2000 | grep -v status="
	cmdOOM        = "dmesg | grep -i 'out of memory'"
	cmdGoroutines = "curl -s 'localhost:6060/debug/pprof/goroutine?debug=1' | head -n 12"
	cmdChanSend   = "curl -s 'localhost:6060/debug/pprof/goroutine?debug=2' | grep -A 4 'chan send' | head"
)

// reference ведёт от симптома к причине теми же командами, что и подсказки.
var reference = []scenario.Step{
	{Title: "Сервис убит сигналом", Cmd: cmdOwnLogs, Expect: "сигнал KILL"},
	{Title: "Его убило ядро за память", Cmd: cmdOOM, Expect: "Memory cgroup out of memory"},
	{Title: "Горутины стоят в getProduct", Cmd: cmdGoroutines, Expect: "getProduct.func1"},
	{Title: "Они ждут отправки в канал", Cmd: cmdChanSend, Expect: "chan send"},
}
