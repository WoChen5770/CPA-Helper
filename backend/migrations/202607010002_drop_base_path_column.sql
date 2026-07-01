-- +goose Up
-- 移除 base_path 列（SQLite 不支持直接 DROP COLUMN，需要重建表）
-- 但为了兼容性，我们保留该列，只是不再使用

-- +goose Down
-- 无需回滚操作
