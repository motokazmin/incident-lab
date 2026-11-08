-- Фон ходит по товарам 1..200, проверки — по остальным: прогретый кэш их не спрячет.
CREATE TABLE products (id int PRIMARY KEY, name text NOT NULL, price int NOT NULL);
INSERT INTO products SELECT i, 'Товар ' || i, 100 + i * 7 % 900 FROM generate_series(1, 5000) i;
