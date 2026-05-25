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
	"strings"

	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	"github.com/liushuangls/go-anthropic/v2"
)

// Твои вопросы
var regQuestions = []string{
	"Йо! Я Джарвис, твой новый цифровой бро. Рад, что ты меня подрубил. Чтобы я не был просто железкой, давай познакомимся по-человечески. Как тебя величать и сколько тебе лет?",
	"Живем в мире, где всё перемешано. На каких языках тебе комфортнее штурмовать этот мир? Русский, английский, иврит — пиши, на чем удобно, я пойму всё.",
	"Чем дышишь? Расскажи в паре слов, чем занимаешься по жизни. Ты программист, который сутками фиксит баги, или человек, который предпочитает движ и реальный мир?",
	"Есть ли что-то, о чем я обязан знать, чтобы не накосячить? Ну, там, аллергия на плохие новости по утрам или особая любовь к мемам?",
}

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

func main() {
	_ = godotenv.Load()
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	dbUrl := os.Getenv("DB_URL")

	db, err := sql.Open("postgres", dbUrl)
	if err != nil {
		log.Fatal("Ошибка подключения к БД:", err)
	}

	agent := &Agent{
		claudeClient: anthropic.NewClient(apiKey),
		db:           db,
	}

	http.HandleFunc("/api/v1/chat", agent.handleChat)
	log.Println("🤖 Джарвис на связи. Порт :8081")
	log.Fatal(http.ListenAndServe(":8081", nil))
}

func (a *Agent) handleChat(w http.ResponseWriter, r *http.Request) {
	msg := r.FormValue("message")
	userID, _ := strconv.Atoi(r.FormValue("user_id"))
	sessionID, _ := strconv.Atoi(r.FormValue("session_id"))
	uidStr := strconv.Itoa(userID)

	// Сохраняем входящее сообщение
	a.db.Exec("INSERT INTO messages (user_id, session_id, role, content) VALUES ($1, $2, 'user', $3)", userID, sessionID, msg)

	// 1. Управление логикой
	memCount := a.getMemCount(uidStr)
	var systemPrompt string

	if memCount < len(regQuestions) {
		// FLOW: Регистрация
		systemPrompt = fmt.Sprintf("Ты Джарвис. Идет регистрация (%d/%d). Вопрос: %s. Если пользователь ответил, извлеки факт и сохрани в JSON: {\"key\": \"...\", \"value\": \"...\", \"cat\": \"profile\"}.", memCount+1, len(regQuestions), regQuestions[memCount])
	} else if msg == "/new chat" {
		// FLOW: Новый чат
		systemPrompt = "Ты Джарвис. Пользователь начал новый чат. Задай настройки стиля, цели и особенностей (как в Википедии или кратко). Извлеки ответы в JSON: {\"key\": \"style/goal/features\", \"value\": \"...\", \"cat\": \"session\"}."
	} else {
		// FLOW: Нормальное общение
		profile := a.getMemories(uidStr)
		systemPrompt = fmt.Sprintf("Ты Джарвис. Профиль пользователя: %s. Твоя задача: отвечать в стиле, заданном пользователем, используя IT-сленг если нужно. СТРОГИЙ HTML.", profile)
	}

	// 2. Отправка в Claude
	model := "claude-haiku-4-5"
	resp, err := a.claudeClient.CreateMessages(context.Background(), anthropic.MessagesRequest{
		Model:    anthropic.Model(model),
		System:   systemPrompt,
		Messages: []anthropic.Message{{Role: "user", Content: []anthropic.MessageContent{anthropic.NewTextMessageContent(msg)}}},
	})

	if err != nil {
		log.Println("Claude Error:", err)
		http.Error(w, "Claude error", 500)
		return
	}

	responseText := resp.Content[0].GetText()

	// 3. Парсинг и сохранение
	if jsonStr := a.extractJSON(responseText); jsonStr != "" {
		var m map[string]string
		if err := json.Unmarshal([]byte(jsonStr), &m); err == nil {
			a.db.Exec("INSERT INTO memories (user_id, fact_key, fact_value, fact_category) VALUES ($1, $2, $3, $4)", uidStr, m["key"], m["value"], m["cat"])
		}
	}

	// 4. Сохранение ответа и возврат
	a.db.Exec("INSERT INTO messages (user_id, session_id, role, content) VALUES ($1, $2, 'assistant', $3)", userID, sessionID, responseText)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(AgentResponse{Text: responseText, Model: model})
}

// ХЕЛПЕРЫ
func (a *Agent) getMemCount(uid string) int {
	var count int
	a.db.QueryRow("SELECT COUNT(*) FROM memories WHERE user_id = $1", uid).Scan(&count)
	return count
}

func (a *Agent) getMemories(uid string) string {
	rows, _ := a.db.Query("SELECT fact_key, fact_value FROM memories WHERE user_id = $1", uid)
	var sb strings.Builder
	for rows.Next() {
		var k, v string
		rows.Scan(&k, &v)
		sb.WriteString(k + ": " + v + "; ")
	}
	return sb.String()
}

func (a *Agent) extractJSON(text string) string {
	s, e := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if s != -1 && e != -1 {
		return text[s : e+1]
	}
	return ""
}
