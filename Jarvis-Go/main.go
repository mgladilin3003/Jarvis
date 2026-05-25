package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	"github.com/liushuangls/go-anthropic/v2"
)

var regQuestions = []string{
	"Как тебя зовут и сколько тебе лет?",
	"На каких языках тебе комфортнее общаться?",
	"Чем ты занимаешься по жизни (работа/хобби)?",
	"Есть ли что-то важное, что мне стоит знать (аллергии, предпочтения)?",
}

type AgentResponse struct {
	Text  string `json:"text"`
	Model string `json:"model"`
}

type Agent struct {
	claudeClient *anthropic.Client
	db           *sql.DB
}

func main() {
	_ = godotenv.Load()
	apiKey, dbUrl := os.Getenv("ANTHROPIC_API_KEY"), os.Getenv("DB_URL")
	db, err := sql.Open("postgres", dbUrl)
	if err != nil {
		log.Fatal(err)
	}

	agent := &Agent{claudeClient: anthropic.NewClient(apiKey), db: db}
	http.HandleFunc("/api/v1/chat", agent.handleChat)
	fmt.Println("🤖 Джарвис готов. Порт :8081")
	log.Fatal(http.ListenAndServe(":8081", nil))
}

func (a *Agent) handleChat(w http.ResponseWriter, r *http.Request) {
	msg := r.FormValue("message")
	uid := r.FormValue("user_id")
	sid := r.FormValue("session_id")

	// 1. Запись сообщения пользователя
	var userMessageID int64
	err := a.db.QueryRow("INSERT INTO messages (user_id, session_id, role, content) VALUES ($1, $2, 'user', $3) RETURNING id",
		uid, sid, msg).Scan(&userMessageID)
	if err != nil {
		log.Printf("❌ Ошибка записи сообщения: %v", err)
	}

	memCount := a.getMemCount(uid)
	memories := a.getMemories(uid)

	systemPrompt := fmt.Sprintf(`Ты Джарвис. Регистрация. 
    Текущий этап: %d из %d. 
    Вопрос пользователю: "%s".
    Уже известно о пользователе: %s.
    ИНСТРУКЦИЯ:
    1. Ответь пользователю дружелюбно.
    2. Если пользователь ответил на вопрос, СРАЗУ ПОСЛЕ ответа напиши разделитель ---JSON--- и после него JSON: {"key":"...","value":"...","cat":"profile"}.
    3. НЕ ПОВТОРЯЙ вопросы, на которые уже есть ответы в базе.`,
		memCount+1, len(regQuestions), regQuestions[memCount], memories)

	resp, err := a.claudeClient.CreateMessages(context.Background(), anthropic.MessagesRequest{
		Model:     anthropic.Model("claude-haiku-4-5"),
		MaxTokens: 1024,
		System:    systemPrompt,
		Messages:  []anthropic.Message{{Role: "user", Content: []anthropic.MessageContent{anthropic.NewTextMessageContent(msg)}}},
	})

	if err != nil {
		log.Printf("❌ API Error: %v", err)
		http.Error(w, "Claude error", 500)
		return
	}

	// 2. Обновление токенов пользователя
	if userMessageID != 0 {
		a.db.Exec("UPDATE messages SET tokens_used = $1 WHERE id = $2", resp.Usage.InputTokens, userMessageID)
	}

	fullText := resp.Content[0].GetText()

	// 3. Парсинг данных
	var cleanText string
	if strings.Contains(fullText, "---JSON---") {
		parts := strings.Split(fullText, "---JSON---")
		cleanText = parts[0]

		jsonPart := parts[1]
		re := regexp.MustCompile(`\{.*\}`)
		match := re.FindString(jsonPart)
		if match != "" {
			var m map[string]string
			if err := json.Unmarshal([]byte(match), &m); err == nil {
				a.db.Exec("INSERT INTO memories (user_id, fact_key, fact_value, fact_category) VALUES ($1, $2, $3, $4)", uid, m["key"], m["value"], m["cat"])
				log.Printf("✅ Записано: %s", m["key"])
			}
		}
	} else {
		cleanText = fullText
	}

	// 4. Запись ответа бота с его токенами
	finalText := a.cleanTrash(cleanText)
	a.db.Exec(`INSERT INTO messages (user_id, session_id, role, content, tokens_used) VALUES ($1, $2, 'assistant', $3, $4)`,
		uid, sid, finalText, resp.Usage.OutputTokens)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(AgentResponse{Text: finalText, Model: "claude-haiku-4-5"})
}

func (a *Agent) cleanTrash(text string) string {
	reTags := regexp.MustCompile(`(?i)\[JSON\].*?\[/JSON\]`)
	text = reTags.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, "---JSON---", "")
	return strings.TrimSpace(text)
}

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
