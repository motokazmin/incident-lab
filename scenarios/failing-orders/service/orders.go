package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
)

type item struct {
	ProductID int `json:"product_id"`
	Qty       int `json:"qty"`
}

type orderRequest struct {
	CustomerID int    `json:"customer_id"`
	Items      []item `json:"items"`
}

var errSoldOut = errors.New("sold out")

func (s *server) getProduct(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	var name string
	var price, stock int
	err = s.db.QueryRowContext(r.Context(), `SELECT name, price, stock FROM products WHERE id = $1`, id).Scan(&name, &price, &stock)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "no such product")
		return
	}
	if err != nil {
		log.Printf("getProduct: %v", err)
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": name, "price": price, "stock": stock})
}

func (s *server) getOrder(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	var customer, total int
	err = s.db.QueryRowContext(r.Context(), `SELECT customer_id, total FROM orders WHERE id = $1`, id).Scan(&customer, &total)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "no such order")
		return
	}
	if err != nil {
		log.Printf("getOrder: %v", err)
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	items, err := s.orderItems(id)
	if err != nil {
		log.Printf("getOrder: %v", err)
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "customer_id": customer, "total": total, "items": items})
}

// orderItems возвращает позиции в том порядке, в каком их добавлял покупатель.
func (s *server) orderItems(orderID int64) ([]item, error) {
	rows, err := s.db.Query(`SELECT product_id, qty FROM order_items WHERE order_id = $1 ORDER BY position`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.ProductID, &it.Qty); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

func (s *server) createOrder(w http.ResponseWriter, r *http.Request) {
	var req orderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.CustomerID <= 0 || len(req.Items) == 0 || len(req.Items) > 20 {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	seen := map[int]bool{}
	for _, it := range req.Items {
		if it.Qty <= 0 || seen[it.ProductID] {
			writeError(w, http.StatusBadRequest, "bad item")
			return
		}
		seen[it.ProductID] = true
	}
	s.place(w, "createOrder", req.CustomerID, req.Items)
}

// repeatOrder оформляет заказ заново с тем же составом: кнопка «Повторить заказ».
func (s *server) repeatOrder(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	var customer int
	err = s.db.QueryRow(`SELECT customer_id FROM orders WHERE id = $1`, id).Scan(&customer)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "no such order")
		return
	}
	if err != nil {
		log.Printf("repeatOrder: %v", err)
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	items, err := s.orderItems(id)
	if err != nil {
		log.Printf("repeatOrder: %v", err)
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	s.place(w, "repeatOrder", customer, items)
}

func (s *server) place(w http.ResponseWriter, op string, customer int, items []item) {
	id, err := s.placeOrder(customer, items)
	if errors.Is(err, errSoldOut) {
		writeError(w, http.StatusConflict, "sold out")
		return
	}
	if err != nil {
		log.Printf("%s: %v", op, err)
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"order_id": id})
}

// placeOrder записывает заказ и списывает товары со склада одной транзакцией.
func (s *server) placeOrder(customer int, items []item) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var orderID int64
	if err := tx.QueryRow(`INSERT INTO orders (customer_id) VALUES ($1) RETURNING id`, customer).Scan(&orderID); err != nil {
		return 0, fmt.Errorf("заказ: %w", err)
	}
	total := 0
	for i, it := range items {
		var price int
		err := tx.QueryRow(`UPDATE products SET stock = stock - $1 WHERE id = $2 AND stock >= $1 RETURNING price`, it.Qty, it.ProductID).Scan(&price)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, errSoldOut
		}
		if err != nil {
			return 0, fmt.Errorf("товар %d: %w", it.ProductID, err)
		}
		// Постоянному покупателю товара — скидка 5%.
		var bought int
		if err := tx.QueryRow(`SELECT count(*) FROM order_items i JOIN orders o ON o.id = i.order_id WHERE o.customer_id = $1 AND i.product_id = $2`, customer, it.ProductID).Scan(&bought); err != nil {
			return 0, fmt.Errorf("скидка %d: %w", it.ProductID, err)
		}
		if bought >= 3 {
			price = price * 95 / 100
		}
		if _, err := tx.Exec(`INSERT INTO order_items (order_id, product_id, position, qty, price) VALUES ($1, $2, $3, $4, $5)`, orderID, it.ProductID, i+1, it.Qty, price); err != nil {
			return 0, fmt.Errorf("позиция %d: %w", it.ProductID, err)
		}
		if _, err := tx.Exec(`INSERT INTO stock_movements (product_id, order_id, qty) VALUES ($1, $2, $3)`, it.ProductID, orderID, -it.Qty); err != nil {
			return 0, fmt.Errorf("движение %d: %w", it.ProductID, err)
		}
		total += price * it.Qty
	}
	if _, err := tx.Exec(`UPDATE orders SET total = $1 WHERE id = $2`, total, orderID); err != nil {
		return 0, fmt.Errorf("сумма: %w", err)
	}
	return orderID, tx.Commit()
}
