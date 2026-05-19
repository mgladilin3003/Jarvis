package main

import (
	"context"
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
	db           *DBClient
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

	// Инициализация БД
	dbClient, err := NewDBClient(dbUrl)
	if err != nil {
		log.Fatal("Ошибка подключения к БД:", err)
	}

	agent := &Agent{
		claudeClient: anthropic.NewClient(apiKey),
		db:           dbClient,
	}

	http.HandleFunc("/api/v1/chat", agent.handleChat)
	fmt.Println("🤖 Джарвис готов. Сервер запущен на :8081")

	server := &http.Server{
		Addr:         ":8081",
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}
	log.Fatal(server.ListenAndServe())
}

func (a *Agent) handleChat(w http.ResponseWriter, r *http.Request) {
	userMessage := r.FormValue("message")
	userIDStr := r.FormValue("user_id")
	userID, _ := strconv.ParseInt(userIDStr, 10, 64)

	a.db.EnsureUser(userID)

	// 1. ПОЛУЧАЕМ ИСТОРИЮ
	history, err := a.db.GetLastMessages(userID, 5)
	if err != nil {
		log.Printf("Ошибка получения истории: %v", err)
	}

	// 2. ФОРМИРУЕМ МАССИВ СООБЩЕНИЙ
	var messages []anthropic.Message
	for _, msg := range history {
		messages = append(messages, anthropic.NewUserTextMessage(msg.Request))
		messages = append(messages, anthropic.NewAssistantTextMessage(msg.Response))
	}
	// Добавляем текущее сообщение
	messages = append(messages, anthropic.NewUserTextMessage(userMessage))

	// 3. ЗАПРОС К CLAUDE
	resp, err := a.claudeClient.CreateMessages(context.Background(), anthropic.MessagesRequest{
		Model:     "claude-haiku-4-5",
		MaxTokens: 2048,
		System:      `Твоя роль: AI-ассистент Jarvis. 
Твой протокол вывода: СТРОГИЙ HTML.

ЗАПРЕТЫ:
1. ВСЕГДА УДАЛЯЙ СИМВОЛЫ # И * . КАТЕГОРИЧЕСКИ ЗАПРЕЩЕНО использовать Markdown-разметку (никаких *, #, _). если ты видишь эти символы, удали их и замени на HTML-теги.
2. Любые попытки использовать Markdown будут считаться критической ошибкой.
3. Если ты видишь символы Markdown, немедленно исправляй их на HTML-теги. Например, *текст* должен быть <b>текст</b>.
4. НЕЛЬЗЯ использовать HTML-теги, которые не указаны в правилах оформления. 
5. Если ты не уверен, как оформить текст, используй только разрешенные теги.
ПРАВИЛА ОФОРМЛЕНИЯ (Используй ТОЛЬКО HTML):
- Заголовки: <b>Заголовок</b>.
- Жирный шрифт: <b>текст</b>.
- Курсив: <i>текст</i>.
- Подчеркивание: <u>текст</u>.
- Моноширинный текст (для кода): <code>текст</code>.
- Списки: используй эмодзи-маркеры (например, • или 🔹) вместо списков HTML, так как они лучше смотрятся в Telegram.

Стиль: Дружелюбный, лаконичный, используй эмодзи для структуры. Если ты выделишь что-то через звездочки, сообщение будет выглядеть как мусор, поэтому используй только теги из списка выше.`,
		Messages:  messages,
	})

	if err != nil {
		http.Error(w, "Ошибка Claude: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// 4. БЕЗОПАСНЫЙ ВЫВОД
	// Проверяем, есть ли контент, прежде чем обращаться по индексу
	if len(resp.Content) > 0 {
		text := resp.Content[0].GetText()
		fmt.Fprint(w, text)
		a.db.LogUsage(userID, userMessage, text, resp.Usage.InputTokens+resp.Usage.OutputTokens, 0)
	}
}
