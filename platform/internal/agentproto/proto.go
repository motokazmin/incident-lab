// Package agentproto — протокол между каркасом и агентом в песочнице. Leaf-пакет.
// Всё, что приходит от агента, — данные злоумышленника: размеры ограничены здесь.
package agentproto

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Порты vsock внутри песочницы.
const (
	PortControl uint32 = 1024 // HTTP агента: готовность, файлы, действия
	PortService uint32 = 1025 // ретранслятор к учебному сервису
	PortPTY     uint32 = 1026 // терминал пользователя
)

// Лимиты на данные из песочницы.
const (
	MaxFrame      = 64 << 10
	MaxFileSize   = 1 << 20
	MaxActionOut  = 64 << 10
	MaxFileList   = 16 << 10
	MaxFileNumber = 200
)

// Типы кадров терминала.
const (
	FrameData   byte = 1
	FrameResize byte = 2 // cols, rows: два uint16
)

// ErrFrameTooBig — кадр больше MaxFrame.
var ErrFrameTooBig = errors.New("agentproto: кадр слишком большой")

// WriteFrame пишет кадр: тип, длина, данные.
func WriteFrame(w io.Writer, typ byte, p []byte) error {
	if len(p) > MaxFrame {
		return ErrFrameTooBig
	}
	buf := make([]byte, 5+len(p))
	buf[0] = typ
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(p)))
	copy(buf[5:], p)
	_, err := w.Write(buf)
	return err
}

// ReadFrame читает кадр. Длина из заголовка проверяется до выделения памяти.
func ReadFrame(r io.Reader) (byte, []byte, error) {
	var h [5]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(h[1:5])
	if n > MaxFrame {
		return 0, nil, fmt.Errorf("%w: %d", ErrFrameTooBig, n)
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(r, p); err != nil {
		return 0, nil, err
	}
	return h[0], p, nil
}

// ResizePayload кодирует размер терминала.
func ResizePayload(cols, rows uint16) []byte {
	p := make([]byte, 4)
	binary.BigEndian.PutUint16(p[0:2], cols)
	binary.BigEndian.PutUint16(p[2:4], rows)
	return p
}

// ParseResize разбирает размер терминала.
func ParseResize(p []byte) (cols, rows uint16, ok bool) {
	if len(p) != 4 {
		return 0, 0, false
	}
	return binary.BigEndian.Uint16(p[0:2]), binary.BigEndian.Uint16(p[2:4]), true
}
