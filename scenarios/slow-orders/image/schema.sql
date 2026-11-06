CREATE TABLE products (id int PRIMARY KEY, name text NOT NULL, stock int NOT NULL CHECK (stock >= 0));
CREATE TABLE orders (id bigserial PRIMARY KEY, customer_id int NOT NULL, product_id int NOT NULL REFERENCES products, qty int NOT NULL, created_at timestamptz NOT NULL DEFAULT now());
INSERT INTO products SELECT i, 'Товар ' || i, 100000000 FROM generate_series(1, 200) i;

-- История магазина. Всё задано формулами: проверка знает ожидаемый список, не спрашивая базу,
-- а дата растёт вместе с id и у покупателя нет двух заказов с одним временем.
-- Редкие покупатели, 30001–30200: по три старых заказа. Заказов поновее у них нет.
INSERT INTO orders (id, customer_id, product_id, qty, created_at)
SELECT i, 30001 + (i - 1) / 3, i % 100 + 1, 1, timestamptz '2023-01-01 00:00:00+00' + i * interval '30 seconds'
FROM generate_series(1, 600) i;
-- Обычные покупатели, 1–20000: по 200 заказов равномерно по всей истории.
INSERT INTO orders (id, customer_id, product_id, qty, created_at)
SELECT i, i % 20000 + 1, i % 100 + 1, 1, timestamptz '2023-01-01 00:00:00+00' + i * interval '30 seconds'
FROM generate_series(1001, 4001000) i;
SELECT setval('orders_id_seq', 4001000);
-- Иначе первый проход допишет отметки видимости во все страницы таблицы прямо под нагрузкой.
VACUUM (FREEZE, ANALYZE) orders;
