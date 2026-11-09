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
-- Встречная пара для проверки: те же корзины проверка оформляет и через POST /orders.
INSERT INTO orders (id, customer_id, total) VALUES (30001, 9001, 0), (30002, 9001, 0);
INSERT INTO order_items (order_id, product_id, position, qty, price) VALUES
  (30001, 701, 1, 1, 8010), (30001, 702, 2, 1, 8020), (30001, 703, 3, 1, 8030),
  (30002, 703, 1, 1, 8030), (30002, 702, 2, 1, 8020), (30002, 701, 3, 1, 8010);
SELECT setval('orders_id_seq', 30002);
