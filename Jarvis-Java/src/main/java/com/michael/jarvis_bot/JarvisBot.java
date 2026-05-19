package com.michael.jarvis_bot;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.http.client.SimpleClientHttpRequestFactory;
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

    @Value("${go.agent.url}")
    private String goAgentUrl;

    // ВНИМАНИЕ: Здесь больше нет @Bean, мы создаем объект вручную в конструкторе
    public JarvisBot(@Value("${bot.name}") String botUsername, 
                     @Value("${bot.token}") String botToken) {
        super(botToken);
        this.botUsername = botUsername;
        
        // Создаем RestTemplate здесь, без участия Spring-контейнера
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
    public void onUpdateReceived(Update update) {
        if (update.hasMessage() && update.getMessage().hasText()) {
            String messageFromUser = update.getMessage().getText();
            long chatId = update.getMessage().getChatId();

            log.info("Получено сообщение от {}: {}", chatId, messageFromUser);
            
            // UX: Индикатор печати
            try {
                SendChatAction typingAction = SendChatAction.builder()
                        .chatId(chatId)
                        .action(ActionType.TYPING.toString())
                        .build();
                execute(typingAction);
            } catch (TelegramApiException e) {
                log.error("Не удалось отправить статус TYPING: {}", e.getMessage());
            }
            
            MultiValueMap<String, String> body = new LinkedMultiValueMap<>();
            body.add("message", messageFromUser);

            try {
                String responseFromClaude = restTemplate.postForObject(goAgentUrl, body, String.class);
                sendText(chatId, responseFromClaude);
            } catch (Exception e) {
                log.error("Ошибка при запросе к Go-агенту ({}): {}", goAgentUrl, e.getMessage());
                sendText(chatId, "Ошибка: Повар на кухне (Go) не отвечает!");
            }
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