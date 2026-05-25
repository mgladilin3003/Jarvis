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
	"Йо! Я Джарвис, твой новый цифровой бро. Рад, что ты меня подрубил. Чтобы я не был просто железкой, давай познакомимся по-человечески. Как тебя величать и сколько тебе лет (хотя в душе мы все дети, но мне для понимания)?",
	"Живем в мире, где всё перемешано. На каких языках тебе комфортнее штурмовать этот мир? Русский, английский, иврит — пиши, на чем удобно, я пойму всё.",
	"Чем дышишь? Расскажи в паре слов, чем занимаешься по жизни. Ты программист, который сутками фиксит баги, или человек, который предпочитает движ и реальный мир? Это поможет мне понимать, в каком ключе лучше «раскидывать» информацию.",
	"Есть ли что-то, о чем я обязан знать, чтобы не накосячить? Ну, там, аллергия на плохие новости по утрам или особая любовь к мемам?",
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

	chatHistory := a.getChatHistory(uid, sid)
	memories := a.getMemories(uid)
	memCount := a.getMemCount(uid)

	var sessionMsgCount int
	a.db.QueryRow("SELECT COUNT(*) FROM messages WHERE session_id = $1 AND role = 'user'", sid).Scan(&sessionMsgCount)

	var userMessageID int64
	a.db.QueryRow("INSERT INTO messages (user_id, session_id, role, content) VALUES ($1, $2, 'user', $3) RETURNING id",
		uid, sid, msg).Scan(&userMessageID)

	var systemPrompt string
	if memCount < len(regQuestions) {
		// РЕЖИМ 1: РЕГИСТРАЦИЯ
		systemPrompt = fmt.Sprintf(`Ты Джарвис. Идет регистрация. 
		Вопросы: %s
		Известно: [%s]
		ПРАВИЛА:
		1. ИСПОЛЬЗУЙ HTML: <b>жирный</b>, <u>подчеркнутый</u>. НИКАКИХ ЗВЕЗДОЧЕК И РЕШЕТОК.
		2. Если пользователь ответил на ПОСЛЕДНИЙ вопрос регистрации (%d-й), то:
		   - Подтверди, что данные сохранены.
		   - СРАЗУ (в этом же сообщении) спроси: "Как сегодня будем общаться: по-дружески или серьезно?" и "Какая цель нашего первого чата?".
		3. Иначе — просто задай СЛЕДУЮЩИЙ ВОПРОС из списка слово в слово.
		4. В конце: ---JSON--- {"key":"...","value":"...","cat":"profile","title":"Знакомство"}`,
			strings.Join(regQuestions, "\n"), memories, len(regQuestions))
	} else if sessionMsgCount <= 1 {
		// РЕЖИМ 2: СТАРТ НОВОЙ СЕССИИ (после регистрации или /newchat)
		systemPrompt = fmt.Sprintf(`Ты Джарвис. Пользователь: [%s]. Начало нового чата.
		ПРАВИЛА:
		1. Поприветствуй по имени (используй <b></b>). 
		2. Спроси про тон (дружеский/серьезный) и цель чата.
		3. Используй ЭМОДЗИ и HTML (<b>, <u>). БЕЗ ЗВЕЗДОЧЕК.
		4. В конце: ---JSON--- {"title": "Тема чата"}`, memories)
	} else {
		// РЕЖИМ 3: РАБОТА
		systemPrompt = fmt.Sprintf(`Ты Джарвис. Память: [%s]. Контекст: %s.
		ПРАВИЛА:
		1. Общайся по делу, используй HTML. БЕЗ ЗВЕЗДОЧЕК И РЕШЕТОК.
		2. Если сменили тему, обнови title: ---JSON--- {"title": "Новое название чата"}`, memories, chatHistory)
	}

	resp, _ := a.claudeClient.CreateMessages(context.Background(), anthropic.MessagesRequest{
		Model:     anthropic.Model("claude-haiku-4-5"),
		MaxTokens: 1024,
		System:    systemPrompt,
		Messages:  []anthropic.Message{{Role: "user", Content: []anthropic.MessageContent{anthropic.NewTextMessageContent(msg)}}},
	})

	if userMessageID != 0 {
		a.db.Exec("UPDATE messages SET tokens_used = $1 WHERE id = $2", resp.Usage.InputTokens, userMessageID)
	}

	fullText := resp.Content[0].GetText()
	cleanText := fullText

	if strings.Contains(fullText, "---JSON---") {
		parts := strings.Split(fullText, "---JSON---")
		cleanText = parts[0]
		var m map[string]string
		re := regexp.MustCompile(`\{.*\}`)
		if err := json.Unmarshal([]byte(re.FindString(parts[1])), &m); err == nil {
			if key, ok := m["key"]; ok && key != "" {
				a.db.Exec("INSERT INTO memories (user_id, fact_key, fact_value, fact_category) VALUES ($1, $2, $3, $4)", uid, key, m["value"], m["cat"])
			}
			if title, ok := m["title"]; ok && title != "" {
				a.db.Exec("UPDATE sessions SET title = $1 WHERE id = $2", title, sid)
				log.Printf("📝 Title updated to: %s", title)
			}
		}
	}

	finalText := a.cleanTrash(cleanText)
	a.db.Exec(`INSERT INTO messages (user_id, session_id, role, content, tokens_used) VALUES ($1, $2, 'assistant', $3, $4)`,
		uid, sid, finalText, resp.Usage.OutputTokens)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(AgentResponse{Text: finalText, Model: "claude-haiku-4-5"})
}

func (a *Agent) cleanTrash(text string) string {
	text = strings.ReplaceAll(text, "---JSON---", "")
	text = strings.ReplaceAll(text, "**", "")
	text = strings.ReplaceAll(text, "#", "")
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
	for rows != nil && rows.Next() {
		var k, v string
		rows.Scan(&k, &v)
		sb.WriteString(k + ": " + v + "; ")
	}
	return sb.String()
}

func (a *Agent) getChatHistory(uid, sid string) string {
	rows, _ := a.db.Query(`SELECT role, content FROM (SELECT id, role, content FROM messages WHERE user_id = $1 AND session_id = $2 ORDER BY id DESC LIMIT 6) sub ORDER BY id ASC`, uid, sid)
	var sb strings.Builder
	for rows != nil && rows.Next() {
		var r, c string
		rows.Scan(&r, &c)
		sb.WriteString(r + ": " + c + "\n")
	}
	return sb.String()
}
