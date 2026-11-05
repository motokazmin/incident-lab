// Package ports — узкие интерфейсы каркаса. Leaf-пакет.
package ports

import (
	"context"
	"net"
)

// Sandbox — запущенная изолированная машина. Единственный путь внутрь — Dial.
type Sandbox interface {
	ID() string
	// Dial открывает поток к порту агента внутри песочницы.
	Dial(ctx context.Context, port uint32) (net.Conn, error)
	// Done закрывается, когда машина перестала существовать.
	Done() <-chan struct{}
	// Close уничтожает машину и её диск. Повторный вызов безопасен.
	Close() error
}

// SandboxProvider создаёт песочницы. Лимиты задаёт реализация, а не вызывающий:
// сценарий называет только образ.
type SandboxProvider interface {
	Create(ctx context.Context, image string) (Sandbox, error)
}
