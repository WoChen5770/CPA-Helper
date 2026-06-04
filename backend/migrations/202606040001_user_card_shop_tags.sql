-- +goose Up
CREATE TABLE IF NOT EXISTS user_card_shop_tags (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id INTEGER NOT NULL,
	tag VARCHAR(32) NOT NULL,
	position INTEGER NOT NULL,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL,
	FOREIGN KEY(user_id) REFERENCES users(id)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_user_card_shop_tags_user_tag_ci
	ON user_card_shop_tags(user_id, lower(tag));

CREATE UNIQUE INDEX IF NOT EXISTS uq_user_card_shop_tags_user_position
	ON user_card_shop_tags(user_id, position);

CREATE INDEX IF NOT EXISTS ix_user_card_shop_tags_user_id
	ON user_card_shop_tags(user_id);

-- +goose Down
DROP TABLE IF EXISTS user_card_shop_tags;
