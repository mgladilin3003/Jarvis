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

	// 0. ПОЛУЧАЕМ ИСТОРИЮ ДИАЛОГА (Контекст)
	chatHistory := a.getChatHistory(uid, sid)

	// 1. Запись текущего сообщения пользователя
	var userMessageID int64
	err := a.db.QueryRow("INSERT INTO messages (user_id, session_id, role, content) VALUES ($1, $2, 'user', $3) RETURNING id",
		uid, sid, msg).Scan(&userMessageID)
	if err != nil {
		log.Printf("❌ Ошибка записи сообщения: %v", err)
	}

	memCount := a.getMemCount(uid)
	memories := a.getMemories(uid)

	var systemPrompt string
	if memCount < len(regQuestions) {
		var questionsStr string
		for i, q := range regQuestions {
			questionsStr += fmt.Sprintf("%d. %s\n", i+1, q)
		}

		systemPrompt = fmt.Sprintf(`Ты Джарвис, цифровой бро. Идет регистрация.
        
История текущего диалога (для понимания контекста):
%s

Список вопросов для регистрации:
%s

База известных фактов: [%s]

ЖЕСТКИЕ ПРАВИЛА:
1. КАТЕГОРИЧЕСКИ ЗАПРЕЩЕНО использовать Markdown (**звездочки**). Это ломает бота! Для выделения эмоций используй ЭМОДЗИ (🚀😎🔥) или ЗАГЛАВНЫЕ БУКВЫ.
2. НЕ ЗДОРОВАЙСЯ в каждом сообщении. Поздоровался один раз — и хватит.
3. НЕ ПОВТОРЯЙ факты о пользователе (имя, хобби), если это не нужно прямо сейчас. Общайся естественно.
4. Отреагируй на ответ пользователя и СРАЗУ (в этом же сообщении) задай следующий вопрос из списка. Вопрос копируй СЛОВО В СЛОВО без отсебятины.
5. Если получена новая инфа о пользователе, в самом конце добавь разделитель ---JSON--- и JSON: {"key":"...","value":"...","cat":"profile"}.`, chatHistory, questionsStr, memories)

	} else {
		systemPrompt = fmt.Sprintf(`Ты Джарвис, цифровой напарник. 

История текущего диалога (память):
%s

База известных фактов: [%s]

ЖЕСТКИЕ ПРАВИЛА:
1. НИКАКИХ ЗВЕЗДОЧЕК (**) и Markdown-разметки! Используй только текст, ЗАГЛАВНЫЕ БУКВЫ и ЭМОДЗИ (😎🤖💡).
2. Не здоровайся в каждом сообщении. Мы уже общаемся.
3. Упоминай факты о пользователе только если это уместно в контексте беседы. Не веди себя как робот-автоответчик.`, chatHistory, memories)
	}

	resp, err := a.claudeClient.CreateMessages(context.Background(), anthropic.MessagesRequest{
		Model:     anthropic.Model("claude-haiku-4-5"),
		MaxTokens: 2048,
		System:    systemPrompt,
		Messages:  []anthropic.Message{{Role: "user", Content: []anthropic.MessageContent{anthropic.NewTextMessageContent(msg)}}},
	})

	if err != nil {
		log.Printf("❌ API Error: %v", err)
		http.Error(w, "Claude error", 500)
		return
	}

	if userMessageID != 0 {
		a.db.Exec("UPDATE messages SET tokens_used = $1 WHERE id = $2", resp.Usage.InputTokens, userMessageID)
	}

	fullText := resp.Content[0].GetText()

	var cleanText string
	if strings.Contains(fullText, "---JSON---") {
		parts := strings.Split(fullText, "---JSON---")
		cleanText = parts[0]

		jsonPart := parts[1]
		s := strings.Index(jsonPart, "{")
		e := strings.LastIndex(jsonPart, "}")
		if s != -1 && e != -1 && e >= s {
			match := jsonPart[s : e+1]
			var m map[string]string
			if err := json.Unmarshal([]byte(match), &m); err == nil {
				a.db.Exec("INSERT INTO memories (user_id, fact_key, fact_value, fact_category) VALUES ($1, $2, $3, $4)", uid, m["key"], m["value"], m["cat"])
			}
		}
	} else {
		cleanText = fullText
	}

	finalText := a.cleanTrash(cleanText)
	a.db.Exec(`INSERT INTO messages (user_id, session_id, role, content, tokens_used) VALUES ($1, $2, 'assistant', $3, $4)`,
		uid, sid, finalText, resp.Usage.OutputTokens)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(AgentResponse{Text: finalText, Model: "claude-haiku-4-5"})
}

// Достаем последние 10 сообщений, чтобы Джарвис понимал контекст
func (a *Agent) getChatHistory(uid, sid string) string {
	rows, err := a.db.Query(`
		SELECT role, content FROM (
			SELECT id, role, content FROM messages 
			WHERE user_id = $1 AND session_id = $2 
			ORDER BY id DESC LIMIT 10
		) sub ORDER BY id ASC
	`, uid, sid)
	if err != nil {
		return ""
	}
	defer rows.Close()

	var sb strings.Builder
	for rows.Next() {
		var role, content string
		rows.Scan(&role, &content)
		if role == "user" {
			sb.WriteString("User: " + content + "\n")
		} else {
			sb.WriteString("Jarvis: " + content + "\n")
		}
	}
	return sb.String()
}

func (a *Agent) cleanTrash(text string) string {
	reTags := regexp.MustCompile(`(?i)\[JSON\].*?\[/JSON\]`)
	text = reTags.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, "---JSON---", "")

	// Вырезаем маркдаун
	text = strings.ReplaceAll(text, "**", "")
	text = strings.ReplaceAll(text, "*", "")
	text = strings.ReplaceAll(text, "###", "")
	text = strings.ReplaceAll(text, "##", "")
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
	for rows.Next() {
		var k, v string
		rows.Scan(&k, &v)
		sb.WriteString(k + ": " + v + "; ")
	}
	return sb.String()
}
