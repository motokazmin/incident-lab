// Package agentclient — HTTP-клиент каркаса к агенту. Ответы агента считаются
// данными злоумышленника: размеры обрезаются, таймауты задаёт каркас.
package agentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"incident-lab/platform/internal/agentproto"
	"incident-lab/platform/internal/ports"
)

// Client обращается к агенту одной песочницы.
type Client struct {
	http *http.Client
}

// New создаёт клиент поверх песочницы.
func New(s ports.Sandbox) *Client {
	return &Client{http: &http.Client{Timeout: 40 * time.Second, Transport: Transport(s, agentproto.PortControl)}}
}

// Transport строит транспорт, который ходит в указанный порт песочницы.
func Transport(s ports.Sandbox, port uint32) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return s.Dial(ctx, port)
		},
		MaxIdleConnsPerHost:    32,
		IdleConnTimeout:        20 * time.Second,
		ResponseHeaderTimeout:  40 * time.Second,
		MaxResponseHeaderBytes: 16 << 10,
		DisableCompression:     true,
	}
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, limit int64) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://agent"+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, 0, err
	}
	if int64(len(data)) > limit {
		return nil, resp.StatusCode, errors.New("agentclient: ответ больше лимита")
	}
	return data, resp.StatusCode, nil
}

// Ready ждёт, пока сценарий внутри песочницы поднимется.
func (c *Client) Ready(ctx context.Context) error {
	var last error
	for {
		data, st, err := c.do(ctx, http.MethodGet, "/healthz", nil, 1024)
		switch {
		case err == nil && st == http.StatusOK:
			return nil
		case err == nil && st == http.StatusInternalServerError:
			return fmt.Errorf("запуск сценария упал: %.1000q", data)
		case err != nil:
			last = err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w (последняя ошибка: %v)", ctx.Err(), last)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Files возвращает список файлов учебного сервиса.
func (c *Client) Files(ctx context.Context) ([]string, error) {
	data, st, err := c.do(ctx, http.MethodGet, "/files", nil, agentproto.MaxFileList)
	if err != nil {
		return nil, err
	}
	if st != http.StatusOK {
		return nil, fmt.Errorf("agentclient: статус %d", st)
	}
	var files []string
	if err := json.Unmarshal(data, &files); err != nil {
		return nil, err
	}
	if len(files) > agentproto.MaxFileNumber {
		files = files[:agentproto.MaxFileNumber]
	}
	return files, nil
}

// ReadFile читает файл учебного сервиса.
func (c *Client) ReadFile(ctx context.Context, path string) ([]byte, error) {
	data, st, err := c.do(ctx, http.MethodGet, "/file?path="+url.QueryEscape(path), nil, agentproto.MaxFileSize)
	if err != nil {
		return nil, err
	}
	if st != http.StatusOK {
		return nil, fmt.Errorf("agentclient: статус %d", st)
	}
	return data, nil
}

// WriteFile записывает файл учебного сервиса.
func (c *Client) WriteFile(ctx context.Context, path string, data []byte) error {
	if len(data) > agentproto.MaxFileSize {
		return errors.New("agentclient: файл больше лимита")
	}
	_, st, err := c.do(ctx, http.MethodPut, "/file?path="+url.QueryEscape(path), data, 1024)
	if err != nil {
		return err
	}
	if st != http.StatusOK {
		return fmt.Errorf("agentclient: статус %d", st)
	}
	return nil
}

// Action выполняет действие сценария и возвращает его вывод.
func (c *Client) Action(ctx context.Context, name string) (string, error) {
	data, st, err := c.do(ctx, http.MethodPost, "/action/"+url.PathEscape(name), nil, agentproto.MaxActionOut)
	if err != nil {
		return "", err
	}
	if st != http.StatusOK {
		return "", fmt.Errorf("действие %s: статус %d: %.200q", name, st, data)
	}
	return string(data), nil
}
