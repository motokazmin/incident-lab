// Package firecracker запускает песочницы в микро-виртуалках: без сети, с одним
// каналом vsock. Базовый диск общий и только для чтения, диск стенда одноразовый.
// Лимиты живут здесь, сценарий их не видит. Jailer пока не подключён, см. ADR 0003.
package firecracker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"incident-lab/platform/internal/ports"
)

// Config — параметры и лимиты всех песочниц.
type Config struct {
	Binary          string
	Kernel          string
	ImagesDir       string
	RunDir          string // короткий путь: unix-сокет ограничен 108 байтами
	VCPUs           int
	MemMiB          int
	LabDiskMiB      int
	DiskBytesPerSec int64
	DiskOpsPerSec   int64
}

// Defaults — конфиг, с которым работает пользователь: сервер и e2e берут один и тот же,
// иначе тесты проверяли бы не те лимиты. data — каталог .data (абсолютный путь), runDir —
// свой у каждого запуска: New чистит его целиком.
func Defaults(data, runDir string) Config {
	return Config{
		Binary:          filepath.Join(data, "fc/firecracker"),
		Kernel:          filepath.Join(data, "fc/vmlinux"),
		ImagesDir:       filepath.Join(data, "images"),
		RunDir:          runDir,
		VCPUs:           2,
		MemMiB:          1024,
		LabDiskMiB:      1024,
		DiskBytesPerSec: 100 << 20,
		DiskOpsPerSec:   5000,
	}
}

// Provider создаёт виртуалки.
type Provider struct {
	cfg Config
}

// New проверяет конфиг и создаёт провайдер.
func New(cfg Config) (*Provider, error) {
	for _, p := range []string{cfg.Binary, cfg.Kernel, cfg.ImagesDir} {
		if _, err := os.Stat(p); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(cfg.RunDir, 0o700); err != nil {
		return nil, err
	}
	if err := removeStale(cfg.RunDir); err != nil {
		return nil, err
	}
	return &Provider{cfg: cfg}, nil
}

var (
	imageName = regexp.MustCompile(`^[a-z0-9-]+$`)
	sandboxID = regexp.MustCompile(`^[0-9a-f]{12}$`)
)

// removeStale убирает диски виртуалок, переживших сервер: сами они умирают вместе
// с ним, а каталоги остаются. Диски песочниц никогда не используются повторно.
func removeStale(runDir string) error {
	entries, err := os.ReadDir(runDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() && sandboxID.MatchString(e.Name()) {
			if err := os.RemoveAll(filepath.Join(runDir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// Create поднимает новую виртуалку. Состояние никогда не переиспользуется.
func (p *Provider) Create(ctx context.Context, image string) (ports.Sandbox, error) {
	if !imageName.MatchString(image) {
		return nil, fmt.Errorf("firecracker: плохое имя образа %q", image)
	}
	root := filepath.Join(p.cfg.ImagesDir, image+".ext4")
	if _, err := os.Stat(root); err != nil {
		return nil, err
	}
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, err
	}
	id := hex.EncodeToString(raw[:])
	dir := filepath.Join(p.cfg.RunDir, id)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, err
	}
	s := &sandbox{id: id, dir: dir, done: make(chan struct{}), console: &tail{limit: 16 << 10}}
	if err := p.start(ctx, s, root); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	return s, nil
}

func (p *Provider) start(ctx context.Context, s *sandbox, root string) error {
	disk := filepath.Join(s.dir, "lab.ext4")
	if err := makeDisk(ctx, disk, p.cfg.LabDiskMiB); err != nil {
		return err
	}
	cfgPath := filepath.Join(s.dir, "vm.json")
	data, err := json.Marshal(vmConfig(p.cfg, root, disk, s.vsockPath()))
	if err != nil {
		return err
	}
	if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
		return err
	}
	cmd := exec.Command(p.cfg.Binary, "--no-api", "--config-file", cfgPath, "--level", "Error")
	cmd.Stdout, cmd.Stderr = s.console, s.console
	cmd.Env = []string{} // окружение сервера виртуалке не нужно
	// Виртуалка не переживает сервер: иначе после падения каркаса останутся стенды без лимита времени.
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		return err
	}
	s.cmd = cmd
	go func() {
		cmd.Wait()
		close(s.done)
	}()
	return nil
}

func makeDisk(ctx context.Context, path string, mib int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	err = f.Truncate(int64(mib) << 20)
	f.Close()
	if err != nil {
		return err
	}
	mkfs, err := exec.LookPath("mkfs.ext4")
	if err != nil {
		mkfs = "/sbin/mkfs.ext4"
	}
	out, err := exec.CommandContext(ctx, mkfs, "-q", "-F", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mkfs: %w: %s", err, out)
	}
	return nil
}

func vmConfig(c Config, root, disk, vsock string) map[string]any {
	limiter := map[string]any{
		"bandwidth": map[string]any{"size": c.DiskBytesPerSec, "refill_time": 1000},
		"ops":       map[string]any{"size": c.DiskOpsPerSec, "refill_time": 1000},
	}
	// Сетевых интерфейсов и MMDS нет намеренно: выйти из песочницы можно только через vsock.
	return map[string]any{
		"boot-source": map[string]any{
			"kernel_image_path": c.Kernel,
			"boot_args":         "console=ttyS0 reboot=k panic=1 pci=off ro init=/sbin/lab-init",
		},
		"drives": []map[string]any{
			{"drive_id": "root", "path_on_host": root, "is_root_device": true, "is_read_only": true, "rate_limiter": limiter},
			{"drive_id": "lab", "path_on_host": disk, "is_root_device": false, "is_read_only": false, "rate_limiter": limiter},
		},
		"machine-config": map[string]any{"vcpu_count": c.VCPUs, "mem_size_mib": c.MemMiB},
		"vsock":          map[string]any{"guest_cid": 3, "uds_path": vsock},
		"entropy":        map[string]any{},
	}
}

type sandbox struct {
	id      string
	dir     string
	cmd     *exec.Cmd
	done    chan struct{}
	console *tail
	once    sync.Once
}

func (s *sandbox) ID() string            { return s.id }
func (s *sandbox) Done() <-chan struct{} { return s.done }
func (s *sandbox) vsockPath() string     { return filepath.Join(s.dir, "v.sock") }

// Dial проходит рукопожатие Firecracker: CONNECT <порт> → OK <порт хоста>.
func (s *sandbox) Dial(ctx context.Context, port uint32) (net.Conn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", s.vsockPath())
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(3 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	c.SetDeadline(deadline)
	if _, err := fmt.Fprintf(c, "CONNECT %d\n", port); err != nil {
		c.Close()
		return nil, err
	}
	// Читаем побайтно: всё после перевода строки уже принадлежит потоку.
	line, err := readLine(c, 64)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("vsock: %w", err)
	}
	if !strings.HasPrefix(line, "OK ") {
		c.Close()
		return nil, fmt.Errorf("vsock: неожиданный ответ %q", line)
	}
	c.SetDeadline(time.Time{})
	return c, nil
}

func readLine(c net.Conn, limit int) (string, error) {
	var b strings.Builder
	one := make([]byte, 1)
	for b.Len() < limit {
		if _, err := c.Read(one); err != nil {
			return "", err
		}
		if one[0] == '\n' {
			return b.String(), nil
		}
		b.WriteByte(one[0])
	}
	return "", errors.New("слишком длинная строка рукопожатия")
}

func (s *sandbox) Close() error {
	s.once.Do(func() {
		if s.cmd != nil && s.cmd.Process != nil {
			s.cmd.Process.Kill()
			<-s.done
		}
		os.RemoveAll(s.dir)
	})
	return nil
}

// Console возвращает хвост консоли: только для отладки запуска.
func (s *sandbox) Console() string { return s.console.String() }

// tail хранит только последние limit байт вывода: консолью управляет гость.
type tail struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-t.limit:]...)
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
