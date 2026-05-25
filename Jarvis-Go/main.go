package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	"github.com/liushuangls/go-anthropic/v2"
)

var regQuestions = []string{
	"Йо! Я Джарвис. Чтобы я стал твоим напарником, давай познакомимся. Как тебя зовут и сколько тебе лет?",
	"На каких языках тебе комфортнее общаться (русский, английский, иврит)?",
	"Чем ты занимаешься по жизни? (Работа, хобби, движ или кодинг?)",
	"Есть ли что-то важное, что мне стоит знать (аллергии, любовь к мемам, особые предпочтения)?",
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

	log.Printf("📥 [DEBUG] Запрос: %s (UID: %s, SID: %s)", msg, uid, sid)

	// Сохраняем сообщение с учетом session_id
	_, err := a.db.Exec("INSERT INTO messages (user_id, session_id, role, content) VALUES ($1, $2, 'user', $3)", uid, sid, msg)
	if err != nil {
		log.Printf("❌ [ERROR] Ошибка записи сообщения: %v", err)
	}

	memCount := a.getMemCount(uid)
	log.Printf("📥 [DEBUG] Текущий memCount: %d", memCount)

	var systemPrompt string
	if memCount < len(regQuestions) {
		systemPrompt = fmt.Sprintf("Ты Джарвис. Регистрация (%d/%d). Вопрос: '%s'. Если ответ получен, ОБЯЗАТЕЛЬНО ответь в формате [JSON]{\"key\":\"...\",\"value\":\"...\",\"cat\":\"profile\"}[/JSON] в конце. Не показывай JSON пользователю.", memCount+1, len(regQuestions), regQuestions[memCount])
	} else {
		systemPrompt = fmt.Sprintf("Ты Джарвис. Профиль: %s. Ответы без Markdown.", a.getMemories(uid))
	}

	resp, err := a.claudeClient.CreateMessages(context.Background(), anthropic.MessagesRequest{
		Model:     anthropic.Model("claude-haiku-4-5"),
		MaxTokens: 2048,
		System:    systemPrompt,
		Messages:  []anthropic.Message{{Role: "user", Content: []anthropic.MessageContent{anthropic.NewTextMessageContent(msg)}}},
	})

	if err != nil {
		log.Printf("❌ [ERROR] Claude API: %v", err)
		http.Error(w, "Claude error", 500)
		return
	}

	fullText := resp.Content[0].GetText()
	log.Printf("📥 [DEBUG] Ответ Claude: %s", fullText)

	// Парсинг JSON
	jsonStr := a.extractTaggedJSON(fullText)
	if jsonStr != "" {
		log.Printf("📥 [DEBUG] JSON найден: %s", jsonStr)
		var m map[string]string
		if err := json.Unmarshal([]byte(jsonStr), &m); err == nil {
			_, err := a.db.Exec("INSERT INTO memories (user_id, fact_key, fact_value, fact_category) VALUES ($1, $2, $3, $4)", uid, m["key"], m["value"], m["cat"])
			if err != nil {
				log.Printf("❌ [ERROR] DB INSERT FAILED: %v", err)
			} else {
				log.Printf("✅ [SUCCESS] Данные записаны в БД!")
			}
		} else {
			log.Printf("❌ [ERROR] JSON UNMARSHAL: %v", err)
		}
	} else {
		log.Printf("⚠️ [WARN] JSON не найден в ответе!")
	}

	cleanText := a.cleanResponse(fullText)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(AgentResponse{Text: cleanText, Model: "claude-haiku-4-5"})
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

func (a *Agent) extractTaggedJSON(text string) string {
	s := strings.Index(text, "[JSON]")
	e := strings.Index(text, "[/JSON]")
	if s != -1 && e != -1 && e > s {
		return text[s+6 : e]
	}
	return ""
}

func (a *Agent) cleanResponse(text string) string {
	s := strings.Index(text, "[JSON]")
	e := strings.Index(text, "[/JSON]")
	if s != -1 && e != -1 {
		text = text[:s] + text[e+7:]
	}
	text = strings.ReplaceAll(text, "**", "")
	text = strings.ReplaceAll(text, "#", "")
	text = strings.ReplaceAll(text, "*", "")
	return strings.TrimSpace(text)
}
