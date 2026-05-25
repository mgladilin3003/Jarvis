package com.michael.jarvis_bot;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.http.client.SimpleClientHttpRequestFactory;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.stereotype.Component;
import org.springframework.util.LinkedMultiValueMap;
import org.springframework.util.MultiValueMap;
import org.springframework.web.client.RestTemplate;
import org.telegram.telegrambots.bots.TelegramLongPollingBot;
import org.telegram.telegrambots.meta.api.methods.ActionType;
import org.telegram.telegrambots.meta.api.methods.GetFile;
import org.telegram.telegrambots.meta.api.methods.send.SendChatAction;
import org.telegram.telegrambots.meta.api.methods.send.SendMessage;
import org.telegram.telegrambots.meta.api.objects.Message;
import org.telegram.telegrambots.meta.api.objects.Update;
import org.telegram.telegrambots.meta.exceptions.TelegramApiException;

import java.io.File;

@Component
public class JarvisBot extends TelegramLongPollingBot {

    private static final Logger log = LoggerFactory.getLogger(JarvisBot.class);

    private final String botUsername;
    private final String botToken;
    private final RestTemplate restTemplate;
    private final JdbcTemplate jdbcTemplate;

    @Value("${go.agent.url}")
    private String goAgentUrl;

    public record AgentResponse(String text, int tokens_in, int tokens_out, String model) {}

    public JarvisBot(@Value("${bot.name}") String botUsername, 
                     @Value("${bot.token}") String botToken,
                     JdbcTemplate jdbcTemplate) {
        super(botToken);
        this.botUsername = botUsername;
        this.botToken = botToken;
        this.jdbcTemplate = jdbcTemplate;
        
        SimpleClientHttpRequestFactory factory = new SimpleClientHttpRequestFactory();
        factory.setConnectTimeout(5_000);
        factory.setReadTimeout(60_000);
        this.restTemplate = new RestTemplate(factory);
    }

    @Override
    public String getBotUsername() {
        return this.botUsername;
    }

    @Override
    public String getBotToken() {
        return this.botToken;
    }

    @Override
    public void onUpdateReceived(Update update) {
        if (update != null && update.hasMessage()) {
            Message message = update.getMessage();
            long chatId = message.getChatId();

            // 1. ОБРАБОТКА ТЕКСТА
            if (message.hasText()) {
                String messageFromUser = message.getText();
                long telegramId = message.getFrom().getId();
                
                String rawUsername = message.getFrom().getUserName();
                String firstName = message.getFrom().getFirstName();
                String username = (rawUsername != null) ? rawUsername : 
                                 (firstName != null) ? firstName : "User_" + telegramId;

                ensureUserExists(telegramId, username);
                int internalId = jdbcTemplate.queryForObject(
                        "SELECT id FROM users WHERE telegram_id = ?", Integer.class, telegramId);

                if ("/newchat".equals(messageFromUser)) {
                    jdbcTemplate.update("UPDATE chat_sessions SET is_active = FALSE WHERE user_id = ?", internalId);
                    sendText(chatId, "✅ <b>Контекст сброшен.</b>");
                    return;
                }

                log.info("Запрос от {}: {}", username, messageFromUser);
                sendTypingAction(chatId);

                Integer sessionId = getOrCreateActiveSession(internalId);
                
                MultiValueMap<String, String> body = new LinkedMultiValueMap<>();
                body.add("message", messageFromUser);
                body.add("user_id", String.valueOf(internalId));
                body.add("session_id", String.valueOf(sessionId));

                String safeUrl = (goAgentUrl != null) ? goAgentUrl + "/api/v1/chat" : "http://localhost:8081/api/v1/chat";

                try {
                    AgentResponse response = restTemplate.postForObject(safeUrl, body, AgentResponse.class);
                    if (response != null && response.text() != null) {
                        sendText(chatId, response.text());
                        int totalTokens = response.tokens_in() + response.tokens_out();
                        jdbcTemplate.update(
                                "UPDATE users SET tokens_used_today = tokens_used_today + ? WHERE id = ?",
                                totalTokens, internalId
                        );
                    }
                } catch (Exception e) {
                    log.error("Ошибка связи: {}", e.getMessage());
                    sendText(chatId, "Ошибка: Мозг системы недоступен.");
                }
            } 
            // 2. ОБРАБОТКА ФАЙЛОВ
            else if (message.hasDocument()) {
                handleDocument(message);
            }
        }
    }

    private void handleDocument(Message message) {
        long chatId = message.getChatId();
        String fileId = message.getDocument().getFileId();
        String fileName = message.getDocument().getFileName();
        
        log.info("DEBUG: Получен файл: {} (ID: {})", fileName, fileId);
        sendText(chatId, "📄 Файл принят, анализирую...");

        try {
            GetFile getFile = new GetFile();
            getFile.setFileId(fileId);
            org.telegram.telegrambots.meta.api.objects.File file = execute(getFile);

            // Путь для сохранения на Малинке
            String localPath = System.getProperty("user.home") + "/jarvis_files/" + fileName;
            downloadFile(file, new File(localPath));
            log.info("DEBUG: Файл скачан локально: {}", localPath);

            MultiValueMap<String, String> body = new LinkedMultiValueMap<>();
            body.add("file_path", localPath);
            
            String safeUrl = (goAgentUrl != null) ? goAgentUrl + "/api/v1/analyze_file" : "http://localhost:8081/api/v1/analyze_file";
            
            AgentResponse response = restTemplate.postForObject(safeUrl, body, AgentResponse.class);
            
            if (response != null && response.text() != null) {
                sendText(chatId, "✅ <b>Результат анализа:</b>\n" + response.text());
            } else {
                sendText(chatId, "⚠️ Файл обработан, но ответ пуст.");
            }

        } catch (Exception e) {
            log.error("ERROR: Ошибка при обработке файла: ", e);
            sendText(chatId, "❌ Ошибка при обработке файла: " + e.getMessage());
        }
    }

    private Integer getOrCreateActiveSession(int internalId) {
        String selectSql = "SELECT id FROM chat_sessions WHERE user_id = ? AND is_active = TRUE ORDER BY created_at DESC LIMIT 1";
        try {
            return jdbcTemplate.queryForObject(selectSql, Integer.class, internalId);
        } catch (Exception e) {
            jdbcTemplate.update("INSERT INTO chat_sessions (user_id, title) VALUES (?, ?)", 
                               internalId, "Session " + System.currentTimeMillis());
            return jdbcTemplate.queryForObject(selectSql, Integer.class, internalId);
        }
    }

    private void ensureUserExists(long telegramId, String username) {
        jdbcTemplate.update("INSERT INTO users (telegram_id, username) VALUES (?, ?) ON CONFLICT (telegram_id) DO NOTHING", 
                           telegramId, username);
    }

    private void sendTypingAction(long chatId) {
        try {
            execute(SendChatAction.builder()
                    .chatId(String.valueOf(chatId))
                    .action(ActionType.TYPING.toString())
                    .build());
        } catch (TelegramApiException e) {
            log.error("Typing error: {}", e.getMessage());
        }
    }

    private void sendText(long chatId, String text) {
        String safeText = (text != null) ? text : "Empty response";
        SendMessage sm = SendMessage.builder()
                .chatId(String.valueOf(chatId))
                .text(safeText)
                .parseMode("HTML")
                .build();
        try {
            execute(sm);
        } catch (TelegramApiException e) {
            log.error("Send error: {}", e.getMessage());
        }
    }
}