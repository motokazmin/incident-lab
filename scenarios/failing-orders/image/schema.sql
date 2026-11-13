CREATE TABLE products (id int PRIMARY KEY, name text NOT NULL, price int NOT NULL, stock int NOT NULL CHECK (stock >= 0));
CREATE TABLE orders (id bigserial PRIMARY KEY, customer_id int NOT NULL, total int NOT NULL DEFAULT 0, created_at timestamptz NOT NULL DEFAULT now());
-- position — порядок, в котором покупатель добавлял товары в корзину.
CREATE TABLE order_items (order_id bigint NOT NULL REFERENCES orders, product_id int NOT NULL REFERENCES products, position int NOT NULL, qty int NOT NULL, price int NOT NULL, PRIMARY KEY (order_id, product_id));
CREATE TABLE stock_movements (id bigserial PRIMARY KEY, product_id int NOT NULL REFERENCES products, order_id bigint NOT NULL REFERENCES orders, qty int NOT NULL, at timestamptz NOT NULL DEFAULT now());

INSERT INTO products SELECT i, 'Товар ' || i, 1000 + i * 10, 10000000 FROM generate_series(1, 197) i;
INSERT INTO products SELECT i, 'Товар ' || i, 1000 + i * 10, 0 FROM generate_series(198, 200) i;
-- Товары для проверки: нагрузка их не трогает.
INSERT INTO products SELECT i, 'Товар ' || i, 1000 + i * 10, 10000000 FROM generate_series(701, 760) i;
-- Последние единицы: проверка разыгрывает за них гонку.
INSERT INTO products SELECT i, 'Товар ' || i, 1000 + i * 10, 1 FROM generate_series(801, 1200) i;

-- История заказов: в каждом два популярных товара из 1–3 и один обычный, порядок у всех свой.
INSERT INTO orders (id, customer_id, total, created_at)
SELECT i, 1 + i % 500, 0, now() - (30000 - i) * interval '1 minute' FROM generate_series(1, 30000) i;
INSERT INTO order_items (order_id, product_id, position, qty, price)
SELECT o, p, pos, 1, 1000 + p * 10 FROM (
  SELECT i AS o, 1 + i % 3 AS p, 1 AS pos FROM generate_series(1, 30000) i
  UNION ALL SELECT i, 1 + (i + 1 + (i / 3) % 2) % 3, 2 FROM generate_series(1, 30000) i
  UNION ALL SELECT i, 7 + i % 93, 3 FROM generate_series(1, 30000) i
) t;
UPDATE orders o SET total = s.t FROM (SELECT order_id, sum(qty * price) t FROM order_items GROUP BY 1) s WHERE s.order_id = o.id;
-- Встречные заказы для проверки (checks.go, crossCart): покупатель i повторяет заказ 30001+i
-- клиента 9101+i. У чётных товары 701, 702, 703, у нечётных встречно, четвёртая позиция —
-- своя, 731+i. Те же корзины проверка оформляет и через POST /orders.
INSERT INTO orders (id, customer_id, total) SELECT 30001 + i, 9101 + i, 0 FROM generate_series(0, 15) i;
INSERT INTO order_items (order_id, product_id, position, qty, price)
SELECT 30001 + i, p, pos, 1, 1000 + p * 10 FROM generate_series(0, 15) i,
  LATERAL (VALUES (CASE WHEN i % 2 = 0 THEN 701 ELSE 703 END, 1), (702, 2),
                  (CASE WHEN i % 2 = 0 THEN 703 ELSE 701 END, 3), (731 + i, 4)) v(p, pos);
-- Повтор с раскупленным товаром последним (checkAtomic): отказ не должен списать остальное.
INSERT INTO orders (id, customer_id, total) VALUES (30017, 9001, 0);
INSERT INTO order_items (order_id, product_id, position, qty, price) VALUES
  (30017, 723, 1, 1, 8230), (30017, 724, 2, 1, 8240), (30017, 199, 3, 1, 2990);
UPDATE orders o SET total = s.t FROM (SELECT order_id, sum(qty * price) t FROM order_items WHERE order_id > 30000 GROUP BY 1) s WHERE s.order_id = o.id;
SELECT setval('orders_id_seq', 30017);
