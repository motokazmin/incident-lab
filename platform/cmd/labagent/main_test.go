package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func script(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "boot")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// Демон, оставшийся без перенаправления вывода, не задерживает запуск стенда.
func TestRunBootDoesNotWaitForDaemon(t *testing.T) {
	done := make(chan error, 1)
	go func() { done <- runBoot(script(t, "sleep 30 &"), filepath.Join(t.TempDir(), "log")) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("запуск ждёт демона")
	}
}

func TestRunBootReportsOutput(t *testing.T) {
	err := runBoot(script(t, "echo сломалось; exit 3"), filepath.Join(t.TempDir(), "log"))
	if err == nil || !strings.Contains(err.Error(), "сломалось") {
		t.Fatalf("ошибка без вывода: %v", err)
	}
}
