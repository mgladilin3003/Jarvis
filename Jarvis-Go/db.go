package main

import (
	"database/sql"
)

type DBClient struct {
	Conn *sql.DB
}

// MessageHistory - структура для передачи истории в агента
type MessageHistory struct {
	Request  string
	Response string
}

// NewDBClient создает подключение и сразу готовит таблицы
func NewDBClient(dsn string) (*DBClient, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}

	// Инициализация схем при старте
	query := `
		CREATE TABLE IF NOT EXISTS users (
			telegram_id BIGINT PRIMARY KEY,
			username TEXT,
			tokens_balance INTEGER DEFAULT 50000,
			is_blocked BOOLEAN DEFAULT FALSE
		);
		CREATE TABLE IF NOT EXISTS message_history (
			id SERIAL PRIMARY KEY,
			telegram_id BIGINT,
			request_text TEXT,
			response_text TEXT,
			tokens_used INTEGER,
			cost_usd NUMERIC(10, 6),
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS user_memory (
			id SERIAL PRIMARY KEY,
			telegram_id BIGINT REFERENCES users(telegram_id),
			fact TEXT,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);`

	_, err = db.Exec(query)
	if err != nil {
		return nil, err
	}

	return &DBClient{Conn: db}, nil
}

func (db *DBClient) EnsureUser(id int64) error {
	_, err := db.Conn.Exec(`
		INSERT INTO users (telegram_id) 
		VALUES ($1) 
		ON CONFLICT (telegram_id) DO NOTHING`, id)
	return err
}

func (db *DBClient) LogUsage(id int64, req, resp string, totalTokens int, _ float64) error {
	cost := (float64(totalTokens) / 1_000_000 * InputCostPerMillion) +
		(float64(totalTokens) / 1_000_000 * OutputCostPerMillion)

	_, err := db.Conn.Exec(`
		INSERT INTO message_history (telegram_id, request_text, response_text, tokens_used, cost_usd) 
		VALUES ($1, $2, $3, $4, $5)`,
		id, req, resp, totalTokens, cost)

	if err != nil {
		return err
	}

	_, err = db.Conn.Exec(`
		UPDATE users 
		SET tokens_balance = tokens_balance - $1 
		WHERE telegram_id = $2`,
		totalTokens, id)

	return err
}

// --- МЕТОДЫ ДЛЯ ПАМЯТИ И ИСТОРИИ ---

func (db *DBClient) GetUserContext(id int64) (string, error) {
	var username string
	err := db.Conn.QueryRow("SELECT COALESCE(username, 'Друг') FROM users WHERE telegram_id = $1", id).Scan(&username)
	return username, err
}

func (db *DBClient) SaveFact(id int64, fact string) error {
	_, err := db.Conn.Exec("INSERT INTO user_memory (telegram_id, fact) VALUES ($1, $2)", id, fact)
	return err
}

func (db *DBClient) GetUserFacts(id int64) ([]string, error) {
	rows, err := db.Conn.Query("SELECT fact FROM user_memory WHERE telegram_id = $1", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facts []string
	for rows.Next() {
		var fact string
		if err := rows.Scan(&fact); err != nil {
			return nil, err
		}
		facts = append(facts, fact)
	}
	return facts, nil
}

// GetLastMessages извлекает историю для кратковременной памяти
func (db *DBClient) GetLastMessages(id int64, limit int) ([]MessageHistory, error) {
	rows, err := db.Conn.Query(`
		SELECT request_text, response_text 
		FROM (
			SELECT request_text, response_text, created_at 
			FROM message_history 
			WHERE telegram_id = $1 
			ORDER BY created_at DESC 
			LIMIT $2
		) AS sub 
		ORDER BY created_at ASC`, id, limit)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []MessageHistory
	for rows.Next() {
		var m MessageHistory
		if err := rows.Scan(&m.Request, &m.Response); err != nil {
			return nil, err
		}
		history = append(history, m)
	}
	return history, nil
}
