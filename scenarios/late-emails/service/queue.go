package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const (
	workers     = 8 // обработчиков очереди писем
	pollEvery   = 200 * time.Millisecond
	reportEvery = 10 * time.Second
	mailURL     = "http://127.0.0.1:9025/send"
)

var (
	mailClient = &http.Client{Timeout: 5 * time.Second}
	sentCount  atomic.Int64 // писем отправлено с прошлого отчёта
)

// startWorkers запускает обработчиков очереди писем и отчёт о ней в журнале сервиса.
func (s *server) startWorkers(ctx context.Context, wg *sync.WaitGroup) {
	for n := 1; n <= workers; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.worker(ctx, n)
		}()
	}
	go s.report(ctx)
}

// worker берёт задачи, пока сервис не останавливают. Начатое письмо дописывается.
func (s *server) worker(ctx context.Context, n int) {
	for ctx.Err() == nil {
		sent, err := s.sendNext()
		if err != nil {
			log.Printf("обработчик %d: %v", n, err)
			sleep(ctx, time.Second)
			continue
		}
		if !sent {
			sleep(ctx, pollEvery)
		}
	}
}

// sendNext отправляет письмо по самой старой задаче. false — очередь пуста.
func (s *server) sendNext() (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var id, orderID int64
	err = tx.QueryRow(`SELECT id, order_id FROM jobs WHERE status = 'new' ORDER BY id LIMIT 1 FOR UPDATE`).Scan(&id, &orderID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := sendMail(orderID); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`UPDATE jobs SET status = 'sent', sent_at = now() WHERE id = $1`, id); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	sentCount.Add(1)
	return true, nil
}

// sendMail передаёт письмо о заказе почтовому шлюзу.
func sendMail(orderID int64) error {
	resp, err := mailClient.Post(fmt.Sprintf("%s?order=%d", mailURL, orderID), "text/plain", nil)
	if err != nil {
		return fmt.Errorf("почтовый шлюз: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("почтовый шлюз: статус %d", resp.StatusCode)
	}
	return nil
}

// report раз в reportEvery пишет в журнал, сколько писем ушло и сколько ждёт.
func (s *server) report(ctx context.Context) {
	t := time.NewTicker(reportEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		var waiting int
		var oldest float64
		err := s.db.QueryRowContext(ctx, `SELECT count(*), coalesce(extract(epoch FROM now() - min(created_at)), 0) FROM jobs WHERE status = 'new'`).Scan(&waiting, &oldest)
		if err != nil {
			log.Printf("очередь писем: %v", err)
			continue
		}
		log.Printf("очередь писем: отправлено за %s — %d, ждут — %d, самое старое ждёт %s",
			reportEvery, sentCount.Swap(0), waiting, (time.Duration(oldest) * time.Second).String())
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
