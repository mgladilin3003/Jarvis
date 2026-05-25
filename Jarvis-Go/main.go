package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	"github.com/liushuangls/go-anthropic/v2"
)

type AgentResponse struct {
	Text      string `json:"text"`
	TokensIn  int    `json:"tokens_in"`
	TokensOut int    `json:"tokens_out"`
	Model     string `json:"model"`
}

type Agent struct {
	claudeClient *anthropic.Client
	db           *sql.DB
}

type ChatMessage struct {
	Role    string
	Content string
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("Файл .env не найден")
	}

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	dbUrl := os.Getenv("DB_URL")
	if apiKey == "" || dbUrl == "" {
		log.Fatal("Критическая ошибка переменных окружения")
	}

	db, err := sql.Open("postgres", dbUrl)
	if err != nil {
		log.Fatal("Ошибка БД:", err)
	}
	defer db.Close()

	agent := &Agent{
		claudeClient: anthropic.NewClient(apiKey),
		db:           db,
	}

	http.HandleFunc("/api/v1/chat", agent.handleChat)
	fmt.Println("🤖 Джарвис готов. Порт :8081")

	server := &http.Server{
		Addr:         ":8081",
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}
	log.Fatal(server.ListenAndServe())
}

func (a *Agent) handleChat(w http.ResponseWriter, r *http.Request) {
	userMessage := r.FormValue("message")
	userID, _ := strconv.Atoi(r.FormValue("user_id"))
	sessionID, _ := strconv.Atoi(r.FormValue("session_id"))

	// 1. Сохраняем сообщение пользователя и СРАЗУ ПОЛУЧАЕМ ЕГО ID
	var userMessageID int
	err := a.db.QueryRow(`
		INSERT INTO messages (user_id, session_id, role, content) 
		VALUES ($1, $2, 'user', $3) RETURNING id`,
		userID, sessionID, userMessage).Scan(&userMessageID)

	if err != nil {
		log.Printf("DB Error (User message): %v", err)
	}

	// 2. Получаем историю конкретно этой сессии
	history, err := a.getHistory(userID, sessionID)
	if err != nil {
		log.Printf("History Error: %v", err)
	}

	var messages []anthropic.Message
	for _, msg := range history {
		role := anthropic.RoleUser
		if msg.Role == "assistant" {
			role = anthropic.RoleAssistant
		}
		messages = append(messages, anthropic.Message{
			Role:    role,
			Content: []anthropic.MessageContent{anthropic.NewTextMessageContent(msg.Content)},
		})
	}

	modelName := "claude-haiku-4-5"
	resp, err := a.claudeClient.CreateMessages(context.Background(), anthropic.MessagesRequest{
		Model:     anthropic.Model(modelName),
		MaxTokens: 2048,
		System:    "Твоя роль: AI-ассистент Jarvis. Вывод: СТРОГИЙ HTML. ЗАПРЕТ: Markdown (#, *). Теги: <b>, <i>, <u>, <code>.",
		Messages:  messages,
	})

	if err != nil {
		log.Printf("Claude Error: %v", err)
		http.Error(w, err.Error(), 500)
		return
	}

	if len(resp.Content) > 0 {
		responseText := resp.Content[0].GetText()

		// 3. ОБНОВЛЯЕМ строку пользователя (записываем потраченные токены на запрос)
		_, err = a.db.Exec("UPDATE messages SET tokens_used = $1 WHERE id = $2",
			resp.Usage.InputTokens, userMessageID)
		if err != nil {
			log.Printf("DB Error (Update user tokens): %v", err)
		}

		// 4. СОХРАНЯЕМ ответ ассистента (пишем токены, потраченные только на генерацию ответа)
		_, err = a.db.Exec(`
			INSERT INTO messages (user_id, session_id, role, content, tokens_used) 
			VALUES ($1, $2, 'assistant', $3, $4)`,
			userID, sessionID, responseText, resp.Usage.OutputTokens)
		if err != nil {
			log.Printf("DB Error (Assistant message): %v", err)
		}

		// Устанавливаем заголовок JSON и отправляем ответ в Java-бот
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		json.NewEncoder(w).Encode(AgentResponse{
			Text:      responseText,
			TokensIn:  resp.Usage.InputTokens,
			TokensOut: resp.Usage.OutputTokens,
			Model:     modelName,
		})
	}
}

func (a *Agent) getHistory(userID int, sessionID int) ([]ChatMessage, error) {
	rows, err := a.db.Query(`
		SELECT role, content 
		FROM messages 
		WHERE user_id = $1 AND session_id = $2
		ORDER BY created_at DESC 
		LIMIT 10`, userID, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []ChatMessage
	for rows.Next() {
		var m ChatMessage
		if err := rows.Scan(&m.Role, &m.Content); err != nil {
			return nil, err
		}
		history = append([]ChatMessage{m}, history...)
	}
	return history, nil
}
