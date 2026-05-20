package main

import (
	"context"
	"database/sql"
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
		log.Println("Файл .env не найден, используем системные переменные")
	}

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	dbUrl := os.Getenv("DB_URL")
	if apiKey == "" || dbUrl == "" {
		log.Fatal("Критическая ошибка! Проверь ANTHROPIC_API_KEY и DB_URL")
	}

	// Инициализация стандартного драйвера sql
	db, err := sql.Open("postgres", dbUrl)
	if err != nil {
		log.Fatal("Ошибка подключения к БД:", err)
	}
	defer db.Close()

	// Проверка соединения
	if err := db.Ping(); err != nil {
		log.Fatal("База данных недоступна:", err)
	}

	agent := &Agent{
		claudeClient: anthropic.NewClient(apiKey),
		db:           db,
	}

	http.HandleFunc("/api/v1/chat", agent.handleChat)
	fmt.Println("🤖 Джарвис (Stateless) готов. Сервер на :8081")

	server := &http.Server{
		Addr:         ":8081",
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}
	log.Fatal(server.ListenAndServe())
}

func (a *Agent) handleChat(w http.ResponseWriter, r *http.Request) {
	userMessage := r.FormValue("message")
	telegramIDStr := r.FormValue("user_id")
	telegramID, _ := strconv.ParseInt(telegramIDStr, 10, 64)

	// 1. ПОЛУЧАЕМ ВНУТРЕННИЙ ID ПОЛЬЗОВАТЕЛЯ
	var userID int
	err := a.db.QueryRow("SELECT id FROM users WHERE telegram_id = $1", telegramID).Scan(&userID)
	if err != nil {
		// Если пользователя нет, Java-бот должен был его создать. Но на всякий случай:
		log.Printf("Пользователь %d не найден в базе", telegramID)
		http.Error(w, "User not found", http.StatusForbidden)
		return
	}

	// 2. СОХРАНЯЕМ ВХОДЯЩЕЕ СООБЩЕНИЕ
	_, err = a.db.Exec("INSERT INTO messages (user_id, role, content) VALUES ($1, 'user', $2)", userID, userMessage)
	if err != nil {
		log.Printf("Ошибка записи сообщения пользователя: %v", err)
	}

	// 3. ПОЛУЧАЕМ ИСТОРИЮ (последние 10 сообщений)
	history, err := a.getHistory(userID)
	if err != nil {
		log.Printf("Ошибка получения истории: %v", err)
	}

	// 4. ФОРМИРУЕМ МАССИВ ДЛЯ CLAUDE
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

	// 5. ЗАПРОС К CLAUDE
	resp, err := a.claudeClient.CreateMessages(context.Background(), anthropic.MessagesRequest{
		Model:     "claude-haiku-4-5", // Исправлено на актуальную модель
		MaxTokens: 2048,
		System: `Твоя роль: AI-ассистент Jarvis. 
Твой протокол вывода: СТРОГИЙ HTML.
ЗАПРЕТЫ:
1. ВСЕГДА УДАЛЯЙ СИМВОЛЫ # И * . КАТЕГОРИЧЕСКИ ЗАПРЕЩЕНО использовать Markdown-разметку.
2. Используй ТОЛЬКО HTML-теги: <b>, <i>, <u>, <code>.
3. Списки оформляй через эмодзи (• или 🔹).`,
		Messages: messages,
	})

	if err != nil {
		log.Printf("Ошибка Claude: %v", err)
		http.Error(w, "Ошибка Claude: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// 6. ОБРАБОТКА ОТВЕТА И СОХРАНЕНИЕ
	if len(resp.Content) > 0 {
		responseText := resp.Content[0].GetText()

		// Сохраняем ответ ассистента в базу
		_, err = a.db.Exec("INSERT INTO messages (user_id, role, content, tokens_used) VALUES ($1, 'assistant', $2, $3)",
			userID, responseText, resp.Usage.InputTokens+resp.Usage.OutputTokens)
		if err != nil {
			log.Printf("Ошибка записи ответа ассистента: %v", err)
		}

		fmt.Fprint(w, responseText)
	}
}

func (a *Agent) getHistory(userID int) ([]ChatMessage, error) {
	rows, err := a.db.Query(`
		SELECT role, content 
		FROM messages 
		WHERE user_id = $1 
		ORDER BY created_at DESC 
		LIMIT 10`, userID)
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
		// Разворачиваем историю в правильном порядке (от старых к новым)
		history = append([]ChatMessage{m}, history...)
	}
	return history, nil
}
