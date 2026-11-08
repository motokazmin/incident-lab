package main

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"
)

// Карточка не должна ждать рекомендаций: без них она всё равно полезна.
const recsTimeout = 150 * time.Millisecond

func (s *server) getProduct(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	var name string
	var price int
	err = s.db.QueryRowContext(r.Context(), `SELECT name, price FROM products WHERE id = $1`, id).Scan(&name, &price)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "no such product")
		return
	}
	if err != nil {
		log.Printf("getProduct: %v", err)
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}

	ch := make(chan recsResult)
	go func() {
		cands, err := fetchCandidates(id)
		ch <- recsResult{cands, err}
	}()
	recs := []int{}
	select {
	case res := <-ch:
		if res.err != nil {
			log.Printf("recommendations product=%d: %v", id, res.err)
		} else {
			recs = topN(res.cands, 5)
		}
	case <-time.After(recsTimeout):
		log.Printf("recommendations timeout product=%d", id)
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": name, "price": price, "recommended": recs})
}
