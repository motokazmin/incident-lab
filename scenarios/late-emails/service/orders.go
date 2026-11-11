package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
)

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
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		log.Printf("getProduct: %v", err)
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": name, "price": price})
}

func (s *server) getOrder(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	var customer, product, total int
	err = s.db.QueryRowContext(r.Context(), `SELECT customer_id, product_id, total FROM orders WHERE id = $1`, id).Scan(&customer, &product, &total)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		log.Printf("getOrder: %v", err)
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "customer_id": customer, "product_id": product, "total": total})
}

// createOrder оформляет заказ и ставит в очередь письмо о нём одной транзакцией:
// заказ без письма или письмо без заказа невозможны.
func (s *server) createOrder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CustomerID int `json:"customer_id"`
		ProductID  int `json:"product_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.CustomerID < 1 || req.ProductID < 1 {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		log.Printf("createOrder: %v", err)
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRow(`INSERT INTO orders (customer_id, product_id, total)
		SELECT $1, id, price FROM products WHERE id = $2 RETURNING id`, req.CustomerID, req.ProductID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "no such product")
		return
	}
	if err == nil {
		_, err = tx.Exec(`INSERT INTO jobs (order_id) VALUES ($1)`, id)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		log.Printf("createOrder: %v", err)
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"order_id": id})
}
