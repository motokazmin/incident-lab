CREATE TABLE products (id int PRIMARY KEY, name text NOT NULL, stock int NOT NULL CHECK (stock >= 0));
CREATE TABLE orders (id bigserial PRIMARY KEY, product_id int NOT NULL REFERENCES products, qty int NOT NULL, created_at timestamptz NOT NULL DEFAULT now());
INSERT INTO products SELECT i, 'Товар ' || i, 100000 FROM generate_series(1, 197) i;
INSERT INTO products SELECT i, 'Товар ' || i, 0 FROM generate_series(198, 200) i;
-- Последние единицы: проверка разыгрывает за них гонку. Каждая попытка тратит несколько товаров.
INSERT INTO products SELECT i, 'Товар ' || i, 1 FROM generate_series(201, 600) i;
