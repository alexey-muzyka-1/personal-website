package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/channel"
	"github.com/alexey-muzyka-1/personal-website/bot/internal/funnel"
)

const (
	defaultAPIBase  = "https://api.telegram.org"
	defaultTimeout  = 10 * time.Second
	maxResponseSize = 1 << 20
)

var _ Sender = (*Client)(nil)

// Client — исходящие вызовы Bot API.
//
// Токен хранится только здесь и никогда не попадает в логи и в ошибки:
// он подставляется в путь запроса, а адреса мы не логируем.
type Client struct {
	token   string
	apiBase string
	http    *http.Client
}

type ClientOption func(*Client)

// WithAPIBase подменяет адрес API — нужен тестам с httptest.
func WithAPIBase(base string) ClientOption {
	return func(c *Client) { c.apiBase = base }
}

func WithHTTPClient(hc *http.Client) ClientOption {
	return func(c *Client) { c.http = hc }
}

func NewClient(token string, opts ...ClientOption) (*Client, error) {
	if token == "" {
		return nil, errors.New("bot token is required")
	}

	c := &Client{
		token:   token,
		apiBase: defaultAPIBase,
		http:    &http.Client{Timeout: defaultTimeout},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

type inlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

type inlineKeyboard struct {
	InlineKeyboard [][]inlineButton `json:"inline_keyboard"`
}

type sendMessageRequest struct {
	ChatID      int64           `json:"chat_id"`
	Text        string          `json:"text"`
	ParseMode   string          `json:"parse_mode"`
	ReplyMarkup *inlineKeyboard `json:"reply_markup,omitempty"`
	// Ссылки на статьи разворачиваются в громоздкое превью и отодвигают
	// кнопку вниз — в этом сценарии превью только мешает.
	DisablePreview bool `json:"disable_web_page_preview"`
}

type editMessageRequest struct {
	ChatID         int64           `json:"chat_id"`
	MessageID      int64           `json:"message_id"`
	Text           string          `json:"text"`
	ParseMode      string          `json:"parse_mode"`
	ReplyMarkup    *inlineKeyboard `json:"reply_markup,omitempty"`
	DisablePreview bool            `json:"disable_web_page_preview"`
}

// parseMode — реплики размечены узким HTML: <b>, <i>, <blockquote>.
const parseMode = "HTML"

// EditMessage заменяет текст и кнопки уже отправленного сообщения.
func (c *Client) EditMessage(ctx context.Context, chatID, messageID int64, reply funnel.Reply) error {
	markup, err := keyboard(reply.Buttons)
	if err != nil {
		return fmt.Errorf("building keyboard: %w", err)
	}

	req := editMessageRequest{
		ChatID:         chatID,
		MessageID:      messageID,
		Text:           reply.Text,
		ParseMode:      parseMode,
		ReplyMarkup:    markup,
		DisablePreview: true,
	}
	if err := c.call(ctx, "editMessageText", req); err != nil {
		return fmt.Errorf("editMessageText: %w", err)
	}
	return nil
}

func (c *Client) SendMessage(ctx context.Context, chatID int64, reply funnel.Reply) error {
	markup, err := keyboard(reply.Buttons)
	if err != nil {
		return fmt.Errorf("building keyboard: %w", err)
	}

	req := sendMessageRequest{
		ChatID:         chatID,
		Text:           reply.Text,
		ParseMode:      parseMode,
		ReplyMarkup:    markup,
		DisablePreview: true,
	}
	if err := c.call(ctx, "sendMessage", req); err != nil {
		return fmt.Errorf("sendMessage: %w", err)
	}
	return nil
}

func (c *Client) AnswerCallback(ctx context.Context, callbackID string) error {
	req := struct {
		CallbackQueryID string `json:"callback_query_id"`
	}{CallbackQueryID: callbackID}

	if err := c.call(ctx, "answerCallbackQuery", req); err != nil {
		return fmt.Errorf("answerCallbackQuery: %w", err)
	}
	return nil
}

// keyboard — по одной кнопке в ряд: в сценарии их максимум две, и
// вертикально они читаются лучше.
func keyboard(buttons []funnel.Button) (*inlineKeyboard, error) {
	if len(buttons) == 0 {
		return nil, nil
	}

	rows := make([][]inlineButton, 0, len(buttons))
	for _, b := range buttons {
		if b.URL != "" {
			rows = append(rows, []inlineButton{{Text: b.Label, URL: b.URL}})
			continue
		}
		data, err := encodeAction(b.Action)
		if err != nil {
			return nil, err
		}
		rows = append(rows, []inlineButton{{Text: b.Label, CallbackData: data}})
	}
	return &inlineKeyboard{InlineKeyboard: rows}, nil
}

// apiResponse — общая обёртка Bot API. Result разбирается только там, где
// ответ действительно нужен: в переписке боту хватает факта успеха, а вот
// про канал Telegram отвечает цифрами.
type apiResponse struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
	Result      json.RawMessage `json:"result"`
}

// ChatMemberCount — сколько человек в канале прямо сейчас.
func (c *Client) ChatMemberCount(ctx context.Context, chat string) (int, error) {
	req := struct {
		ChatID string `json:"chat_id"`
	}{ChatID: chat}

	var count int
	if err := c.callInto(ctx, "getChatMemberCount", req, &count); err != nil {
		return 0, fmt.Errorf("getChatMemberCount: %w", err)
	}
	return count, nil
}

// ChatMember — статус одного человека в канале.
//
// Это единственный способ узнать про подписку того, кто подписался до
// того, как бот стал админом: списка участников Bot API не отдаёт, и
// спрашивать приходится поимённо, про каждого, кого мы и так знаем.
func (c *Client) ChatMember(ctx context.Context, chat string, userID int64) (channel.Member, string, error) {
	req := struct {
		ChatID string `json:"chat_id"`
		UserID int64  `json:"user_id"`
	}{ChatID: chat, UserID: userID}

	var member ChatMember
	if err := c.callInto(ctx, "getChatMember", req, &member); err != nil {
		return channel.Member{}, "", fmt.Errorf("getChatMember: %w", err)
	}

	out := channel.Member{TelegramID: userID}
	if member.User != nil {
		out.Username, out.FirstName = member.User.Username, member.User.FirstName
	}
	return out, member.Status, nil
}

func (c *Client) call(ctx context.Context, method string, payload any) error {
	return c.callInto(ctx, method, payload, nil)
}

func (c *Client) callInto(ctx context.Context, method string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encoding request: %w", err)
	}

	url := fmt.Sprintf("%s/bot%s/%s", c.apiBase, c.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		// Ошибка транспорта содержит URL вместе с токеном — наружу
		// отдаём только факт сбоя.
		return fmt.Errorf("calling bot api: %w", scrubToken(err, c.token))
	}
	defer resp.Body.Close()

	var parsed apiResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseSize)).Decode(&parsed); err != nil {
		return fmt.Errorf("decoding response (http %d): %w", resp.StatusCode, err)
	}
	if !parsed.OK {
		return fmt.Errorf("bot api error %d: %s", parsed.ErrorCode, parsed.Description)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(parsed.Result, out); err != nil {
		return fmt.Errorf("decoding result of %s: %w", method, err)
	}
	return nil
}

// scrubToken прячет токен, если он оказался внутри текста ошибки:
// ошибки транспорта содержат полный URL, а он уедет в логи целиком.
func scrubToken(err error, token string) error {
	msg := err.Error()
	if !strings.Contains(msg, token) {
		return err
	}
	return errors.New(strings.ReplaceAll(msg, token, "<token>"))
}

type setWebhookRequest struct {
	URL string `json:"url"`
	// SecretToken Telegram будет присылать в заголовке
	// X-Telegram-Bot-Api-Secret-Token — так webhook отличает свои запросы
	// от чужих.
	SecretToken    string   `json:"secret_token"`
	AllowedUpdates []string `json:"allowed_updates"`
}

// AllowedUpdates — что бот просит присылать.
//
// Список обязателен целиком при каждом вызове: Telegram не дополняет
// прежний набор, а заменяет его. И chat_member в него входить обязан —
// без явного упоминания подписки на канал не приходят вовсе, по
// умолчанию этот тип выключен.
var AllowedUpdates = []string{"message", "callback_query", "chat_member", "my_chat_member"}

// SetWebhook регистрирует адрес webhook в Telegram.
//
// Вызывается на каждом старте, а не по флагу. Флаг был грабли: набор
// нужных типов update меняется вместе с кодом, и «выкатить и не забыть
// один раз включить переменную» означает молча остаться без половины
// событий. Вызов идемпотентный — тот же адрес и тот же секрет.
//
// Накопившиеся update не сбрасываются: рестарт это не переезд, а очередь
// за время простоя — это люди, которые нажали кнопку, пока бот лежал.
func (c *Client) SetWebhook(ctx context.Context, url, secret string) error {
	req := setWebhookRequest{
		URL:            url,
		SecretToken:    secret,
		AllowedUpdates: AllowedUpdates,
	}
	if err := c.call(ctx, "setWebhook", req); err != nil {
		return fmt.Errorf("setWebhook: %w", err)
	}
	return nil
}
