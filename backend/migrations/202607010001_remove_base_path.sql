-- +goose Up
-- 移除不再需要的 base_path 配置（如果存在）
-- 注意：SQLite 不支持 DROP COLUMN，但由于这个字段已经不再使用，保留也无影响

-- +goose Down
-- 无需回滚操作
