package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"
)

const pageSize = 20

type orderRequest struct {
	CustomerID int `json:"customer_id"`
	ProductID  int `json:"product_id"`
	Qty        int `json:"qty"`
}

type order struct {
	ID        int64     `json:"id"`
	ProductID int       `json:"product_id"`
	Qty       int       `json:"qty"`
	CreatedAt time.Time `json:"created_at"`
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

// getOrder отдаёт один заказ.
func (s *server) getOrder(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	var customer int
	var o order
	err = s.db.QueryRowContext(r.Context(), `SELECT id, customer_id, product_id, qty, created_at FROM orders WHERE id = $1`, id).
		Scan(&o.ID, &customer, &o.ProductID, &o.Qty, &o.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "no such order")
		return
	}
	if err != nil {
		log.Printf("getOrder: %v", err)
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": o.ID, "customer_id": customer, "product_id": o.ProductID, "qty": o.Qty, "created_at": o.CreatedAt})
}

// listOrders отдаёт последние заказы покупателя.
func (s *server) listOrders(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT id, product_id, qty, created_at FROM orders WHERE customer_id = $1 ORDER BY created_at DESC LIMIT $2`, id, pageSize)
	if err != nil {
		log.Printf("listOrders: %v", err)
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	list := []order{}
	for rows.Next() {
		var o order
		if err := rows.Scan(&o.ID, &o.ProductID, &o.Qty, &o.CreatedAt); err != nil {
			log.Printf("listOrders: scan: %v", err)
			writeError(w, http.StatusInternalServerError, "db error")
			return
		}
		list = append(list, o)
	}
	if err := rows.Err(); err != nil {
		log.Printf("listOrders: %v", err)
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"orders": list})
}

func (s *server) createOrder(w http.ResponseWriter, r *http.Request) {
	var req orderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Qty <= 0 {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		log.Printf("createOrder: begin: %v", err)
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer tx.Rollback()

	res, err := tx.Exec(`UPDATE products SET stock = stock - $1 WHERE id = $2 AND stock >= $1`, req.Qty, req.ProductID)
	if err != nil {
		log.Printf("createOrder: update: %v", err)
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusConflict, "sold out")
		return
	}
	var orderID int64
	err = tx.QueryRow(`INSERT INTO orders (customer_id, product_id, qty) VALUES ($1, $2, $3) RETURNING id`, req.CustomerID, req.ProductID, req.Qty).Scan(&orderID)
	if err != nil {
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
