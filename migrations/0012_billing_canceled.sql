-- +goose Up
ALTER TABLE ma_orders DROP CONSTRAINT IF EXISTS ma_orders_status_check;
ALTER TABLE ma_orders ADD CONSTRAINT ma_orders_status_check
    CHECK (status IN ('pending','paid','failed','refunded','canceled'));

-- +goose Down
ALTER TABLE ma_orders DROP CONSTRAINT IF EXISTS ma_orders_status_check;
ALTER TABLE ma_orders ADD CONSTRAINT ma_orders_status_check
    CHECK (status IN ('pending','paid','failed','refunded'));
