package com.michael.jarvis_bot;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.http.client.SimpleClientHttpRequestFactory;
import org.springframework.jdbc.core.JdbcTemplate; // Добавлено для работы с БД
import org.springframework.stereotype.Component;
import org.springframework.util.LinkedMultiValueMap;
import org.springframework.util.MultiValueMap;
import org.springframework.web.client.RestTemplate;
import org.telegram.telegrambots.bots.TelegramLongPollingBot;
import org.telegram.telegrambots.meta.api.methods.ActionType;
import org.telegram.telegrambots.meta.api.methods.send.SendChatAction;
import org.telegram.telegrambots.meta.api.methods.send.SendMessage;
import org.telegram.telegrambots.meta.api.objects.Update;
import org.telegram.telegrambots.meta.exceptions.TelegramApiException;

@Component
public class JarvisBot extends TelegramLongPollingBot {

    private static final Logger log = LoggerFactory.getLogger(JarvisBot.class);

    private final String botUsername;
    private final RestTemplate restTemplate;
    private final JdbcTemplate jdbcTemplate; // Поле для работы с базой

    @Value("${go.agent.url}")
    private String goAgentUrl;

    public JarvisBot(@Value("${bot.name}") String botUsername, 
                     @Value("${bot.token}") String botToken,
                     JdbcTemplate jdbcTemplate) { // Spring сам внедрит JdbcTemplate
        super(botToken);
        this.botUsername = botUsername;
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

    @SuppressWarnings("null")
    @Override
    public void onUpdateReceived(Update update) {
        if (update.hasMessage() && update.getMessage().hasText()) {
            String messageFromUser = update.getMessage().getText();
            long chatId = update.getMessage().getChatId();
            
            // Исправление Null type safety для username
            String username = update.getMessage().getFrom().getUserName();
            if (username == null) {
                username = update.getMessage().getFrom().getFirstName();
                if (username == null) username = "User_" + chatId;
            }

            log.info("Получено сообщение от {}: {}", chatId, messageFromUser);
            
            // ШАГ 1: Регистрация пользователя в БД (чтобы Go-агент его нашел)
            ensureUserExists(chatId, username);
            
            // ШАГ 2: Индикатор печати
            sendTypingAction(chatId);
            
            // ШАГ 3: Запрос к Go-агенту
            MultiValueMap<String, String> body = new LinkedMultiValueMap<>();
            body.add("message", messageFromUser);
            body.add("user_id", String.valueOf(chatId));

            try {
                     String responseFromClaude = restTemplate.postForObject(goAgentUrl, body, String.class);

                    if (responseFromClaude != null) {
                        sendText(chatId, responseFromClaude);
                    } else {
                        log.warn("Go-агент вернул пустой ответ для пользователя {}", chatId);
                        sendText(chatId, "<i>Джарвис задумался и не смог ответить. Попробуйте еще раз.</i>");
}
            } catch (Exception e) {
                log.error("Ошибка при запросе к Go-агенту ({}): {}", goAgentUrl, e.getMessage());
                sendText(chatId, "<b>Ошибка:</b> Повар на кухне (Go) не отвечает!");
            }
        }
    }

    private void ensureUserExists(long telegramId, String username) {
        String sql = "INSERT INTO users (telegram_id, username) VALUES (?, ?) ON CONFLICT (telegram_id) DO NOTHING";
        try {
            jdbcTemplate.update(sql, telegramId, username);
        } catch (Exception e) {
            log.error("Ошибка при проверке пользователя в БД: {}", e.getMessage());
        }
    }

    private void sendTypingAction(long chatId) {
        try {
            SendChatAction typingAction = SendChatAction.builder()
                    .chatId(chatId)
                    .action(ActionType.TYPING.toString())
                    .build();
            execute(typingAction);
        } catch (TelegramApiException e) {
            log.error("Не удалось отправить статус TYPING: {}", e.getMessage());
        }
    }

    private void sendText(long chatId, String text) {
        SendMessage sm = SendMessage.builder()
                .chatId(chatId)
                .text(text)
                .parseMode("HTML")
                .build();
        try {
            execute(sm);
        } catch (TelegramApiException e) {
            log.warn("Ошибка отправки HTML сообщения, пробуем обычный текст: {}", e.getMessage());
            try {
                execute(SendMessage.builder().chatId(chatId).text(text).build());
            } catch (TelegramApiException ex) {
                log.error("Критическая ошибка отправки: {}", ex.getMessage());
            }
        }
    }
}