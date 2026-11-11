CREATE TABLE products (id int PRIMARY KEY, name text NOT NULL, price int NOT NULL);
CREATE TABLE orders (id bigserial PRIMARY KEY, customer_id int NOT NULL, product_id int NOT NULL REFERENCES products, total int NOT NULL, created_at timestamptz NOT NULL DEFAULT now());
-- Очередь фоновых задач: письмо о заказе. Задачу кладёт та же транзакция, что и заказ.
CREATE TABLE jobs (id bigserial PRIMARY KEY, order_id bigint NOT NULL REFERENCES orders, status text NOT NULL DEFAULT 'new', created_at timestamptz NOT NULL DEFAULT now(), sent_at timestamptz);
CREATE INDEX jobs_new ON jobs (id) WHERE status = 'new';

INSERT INTO products SELECT i, 'Товар ' || i, 1000 + i * 10 FROM generate_series(1, 200) i;

-- История: заказы за прошлые недели, письма о них давно ушли.
INSERT INTO orders (id, customer_id, product_id, total, created_at)
SELECT i, 1 + i % 500, 1 + i % 200, 1000 + (1 + i % 200) * 10, now() - (30000 - i) * interval '1 minute' FROM generate_series(1, 30000) i;
INSERT INTO jobs (order_id, status, created_at, sent_at)
SELECT id, 'sent', created_at, created_at + interval '1 second' FROM orders ORDER BY id;
SELECT setval('orders_id_seq', 30000);
