// Composition root: конфиг → песочницы → сценарии → стенды → шлюз.
package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"incident-lab/platform/internal/gateway"
	"incident-lab/platform/internal/lab"
	"incident-lab/platform/internal/sandbox/firecracker"
	"incident-lab/scenarios"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8090", "адрес сервера, только loopback")
	data := flag.String("data", ".data", "каталог с Firecracker, ядром и образами")
	web := flag.String("web", "web/dist", "собранный веб-интерфейс")
	flag.Parse()

	mustLoopback(*addr)
	provider, err := firecracker.New(sandboxConfig(*data))
	if err != nil {
		log.Fatal(err)
	}
	labs := lab.NewManager(provider, scenarios.All(), lab.Limits{
		MaxLabs:     2,
		MaxLifetime: 90 * time.Minute,
		IdleTimeout: 2 * time.Minute,
	})
	log.Printf("http://%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, gateway.New(labs, *web)))
}

// mustLoopback не даёт открыть стенды наружу: jailer не подключён, см. ADR 0003.
func mustLoopback(addr string) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		log.Fatal(err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		log.Fatalf("адрес %q не loopback: наружу сервер не открывается", addr)
	}
}

func sandboxConfig(data string) firecracker.Config {
	abs, err := filepath.Abs(data)
	if err != nil {
		log.Fatal(err)
	}
	return firecracker.Defaults(abs, filepath.Join(abs, "run"))
}
