package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
)

type orderRequest struct {
	ProductID int `json:"product_id"`
	Qty       int `json:"qty"`
}

func (s *server) getProduct(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	var name string
	var stock int
	err = s.db.QueryRowContext(r.Context(), `SELECT name, stock FROM products WHERE id = $1`, id).Scan(&name, &stock)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "no such product")
		return
	}
	if err != nil {
		log.Printf("getProduct: %v", err)
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": name, "stock": stock})
}

func (s *server) createOrder(w http.ResponseWriter, r *http.Request) {
	var req orderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Qty <= 0 {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}

	tx, err := s.db.Begin()
	if err != nil {
		log.Printf("createOrder: begin: %v", err)
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}

	var stock int
	err = tx.QueryRow(`SELECT stock FROM products WHERE id = $1 FOR UPDATE`, req.ProductID).Scan(&stock)
	if errors.Is(err, sql.ErrNoRows) {
		tx.Rollback()
		writeError(w, http.StatusNotFound, "no such product")
		return
	}
	if err != nil {
		tx.Rollback()
		log.Printf("createOrder: select: %v", err)
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	if stock < req.Qty {
		writeError(w, http.StatusConflict, "sold out")
		return
	}

	if _, err := tx.Exec(`UPDATE products SET stock = stock - $1 WHERE id = $2`, req.Qty, req.ProductID); err != nil {
		tx.Rollback()
		log.Printf("createOrder: update: %v", err)
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	var orderID int64
	err = tx.QueryRow(`INSERT INTO orders (product_id, qty) VALUES ($1, $2) RETURNING id`, req.ProductID, req.Qty).Scan(&orderID)
	if err != nil {
		tx.Rollback()
		log.Printf("createOrder: insert: %v", err)
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	if err := tx.Commit(); err != nil {
		log.Printf("createOrder: commit: %v", err)
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"order_id": orderID})
}
