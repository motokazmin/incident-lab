// labagent работает внутри песочницы. Пользователь там root и может подменить
// его, поэтому каркас не верит ничему из сказанного агентом.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/creack/pty"
	"github.com/mdlayher/vsock"

	"incident-lab/platform/internal/agentproto"
)

const (
	bootScript = "/etc/lab/boot"
	bootLog    = "/var/log/lab-boot.log"
	actionsDir = "/etc/lab/actions"
	workDir    = "/srv/app"
	targetAddr = "127.0.0.1:8080"
)

var actionName = regexp.MustCompile(`^[a-z0-9-]+$`)

func main() {
	// Без агента стенд выключается, поэтому OOM killer его не трогает. Запущенным процессам
	// оценка возвращается в 0 (см. command), иначе они унаследовали бы защиту.
	if err := os.WriteFile("/proc/self/oom_score_adj", []byte("-1000"), 0); err != nil {
		log.Print(err)
	}
	var ready atomic.Bool
	var bootErr atomic.Value
	go func() {
		if err := runBoot(bootScript, bootLog); err != nil {
			bootErr.Store(err.Error())
			return
		}
		ready.Store(true)
	}()

	go serve(agentproto.PortService, relay)
	go serve(agentproto.PortPTY, terminal)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		if e := bootErr.Load(); e != nil {
			http.Error(w, e.(string), http.StatusInternalServerError)
			return
		}
		if !ready.Load() {
			http.Error(w, "booting", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /files", listFiles)
	mux.HandleFunc("GET /file", readFile)
	mux.HandleFunc("PUT /file", writeFile)
	mux.HandleFunc("POST /action/{name}", runAction)

	l, err := vsock.Listen(agentproto.PortControl, nil)
	if err != nil {
		log.Fatal(err)
	}
	log.Fatal(http.Serve(l, mux))
}

// unprotect сбрасывает унаследованную от агента защиту от OOM и заменяет себя программой.
const unprotect = `echo 0 >/proc/self/oom_score_adj && exec "$@"`

// command запускает программу без защиты агента от OOM killer.
func command(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "/bin/sh", append([]string{"-c", unprotect, "lab", name}, args...)...)
}

// runBoot запускает скрипт сюжета с выводом в файл. Через канал нельзя: фоновый демон,
// оставшийся без перенаправления, держал бы его открытым, и запуск не закончился бы.
func runBoot(script, logPath string) error {
	f, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd := command(context.Background(), script)
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Run(); err != nil {
		out, _ := os.ReadFile(logPath)
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

func serve(port uint32, h func(net.Conn)) {
	l, err := vsock.Listen(port, nil)
	if err != nil {
		log.Fatal(err)
	}
	for {
		c, err := l.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go h(c)
	}
}

func relay(c net.Conn) {
	defer c.Close()
	t, err := net.DialTimeout("tcp", targetAddr, time.Second)
	if err != nil {
		return
	}
	defer t.Close()
	done := make(chan struct{}, 2)
	go func() { io.Copy(t, c); done <- struct{}{} }()
	go func() { io.Copy(c, t); done <- struct{}{} }()
	<-done
}

func terminal(c net.Conn) {
	defer c.Close()
	cmd := command(context.Background(), "/bin/bash", "-l")
	cmd.Dir = workDir
	cmd.Env = []string{"TERM=xterm-256color", "HOME=/root", "PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C.UTF-8", "GOFLAGS=-mod=vendor"}
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 120, Rows: 30})
	if err != nil {
		return
	}
	defer func() {
		f.Close()
		cmd.Process.Kill()
		cmd.Wait()
	}()
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := f.Read(buf)
			if n > 0 && agentproto.WriteFrame(c, agentproto.FrameData, buf[:n]) != nil {
				break
			}
			if err != nil {
				break
			}
		}
		c.Close()
	}()
	for {
		typ, p, err := agentproto.ReadFrame(c)
		if err != nil {
			return
		}
		switch typ {
		case agentproto.FrameData:
			if _, err := f.Write(p); err != nil {
				return
			}
		case agentproto.FrameResize:
			if cols, rows, ok := agentproto.ParseResize(p); ok {
				pty.Setsize(f, &pty.Winsize{Cols: cols, Rows: rows})
			}
		}
	}
}

// inWorkDir приводит относительный путь к абсолютному внутри рабочего каталога.
func inWorkDir(rel string) (string, bool) {
	p := filepath.Join(workDir, filepath.Clean("/"+rel))
	return p, strings.HasPrefix(p, workDir+"/")
}

func listFiles(w http.ResponseWriter, _ *http.Request) {
	files := []string{}
	filepath.WalkDir(workDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (d.Name() == "vendor" || d.Name() == "bin") {
			return filepath.SkipDir
		}
		if !d.IsDir() && len(files) < agentproto.MaxFileNumber {
			rel, _ := filepath.Rel(workDir, p)
			files = append(files, rel)
		}
		return nil
	})
	json.NewEncoder(w).Encode(files)
}

func readFile(w http.ResponseWriter, r *http.Request) {
	p, ok := inWorkDir(r.URL.Query().Get("path"))
	if !ok {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	http.ServeFile(w, r, p)
}

func writeFile(w http.ResponseWriter, r *http.Request) {
	p, ok := inWorkDir(r.URL.Query().Get("path"))
	if !ok {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, agentproto.MaxFileSize))
	if err != nil {
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func runAction(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !actionName.MatchString(name) {
		http.Error(w, "bad action", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, err := command(ctx, filepath.Join(actionsDir, name)).CombinedOutput()
	if err != nil {
		http.Error(w, string(out)+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Write(out)
}
