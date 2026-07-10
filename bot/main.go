// Command bot is the Telegram front-end and job queue owner. It never runs
// claude itself — it enqueues prompts and waits for the agent (running on
// whichever machine has the projects) to poll for work and post results back.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/joho/godotenv"

	"github.com/dustin/go-humanize"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/Fosterist/claude-anywhere/internal/api"
	"github.com/Fosterist/claude-anywhere/internal/config"
	"github.com/Fosterist/claude-anywhere/internal/store"
)

func main() {
	godotenv.Load()

	token := mustEnv("TELEGRAM_TOKEN")
	agentToken := mustEnv("AGENT_TOKEN")
	adminChatID, err := strconv.ParseInt(mustEnv("ADMIN_CHAT_ID"), 10, 64)
	if err != nil {
		log.Fatalf("ADMIN_CHAT_ID must be a number: %v", err)
	}
	httpAddr := envOr("HTTP_ADDR", ":8090")
	dbPath := envOr("DB_PATH", "claude-anywhere.db")
	configPath := envOr("CONFIG_PATH", "projects.json")

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatalf("telegram: %v", err)
	}
	log.Printf("authorized as @%s", bot.Self.UserName)

	commands := tgbotapi.NewSetMyCommands(
		tgbotapi.BotCommand{Command: "projects", Description: "Выбрать проект"},
		tgbotapi.BotCommand{Command: "mode", Description: "Режим очереди: автономно / пошагово"},
		tgbotapi.BotCommand{Command: "offline", Description: "Поведение при офлайне ПК"},
		tgbotapi.BotCommand{Command: "status", Description: "Текущие настройки и расход за 5 часов"},
	)
	if _, err := bot.Request(commands); err != nil {
		log.Printf("set commands: %v", err)
	}

	live := &liveness{}
	srv := &server{st: st, agentToken: agentToken, bot: bot, live: live}
	go srv.listenHTTP(httpAddr)

	tg := &tgHandler{bot: bot, st: st, cfg: cfg, adminChatID: adminChatID, live: live}
	tg.run()
}

// liveness tracks the last time the agent polled for work, so the bot can
// tell whether it's likely online without a dedicated heartbeat endpoint —
// every /jobs/next hit already proves the agent is alive.
type liveness struct {
	mu       sync.Mutex
	lastPoll time.Time
}

func (l *liveness) touch() {
	l.mu.Lock()
	l.lastPoll = time.Now()
	l.mu.Unlock()
}

func (l *liveness) online(threshold time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return !l.lastPoll.IsZero() && time.Since(l.lastPoll) <= threshold
}

// agentOfflineThreshold is a few multiples of the agent's poll interval
// (3s), so normal jitter doesn't false-positive as "offline".
const agentOfflineThreshold = 15 * time.Second

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("missing required env var %s", key)
	}
	return v
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// --- HTTP side: talks to the agent ---

type server struct {
	st         *store.Store
	agentToken string
	bot        *tgbotapi.BotAPI
	live       *liveness
}

func (s *server) listenHTTP(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/jobs/next", s.authed(s.handleNext))
	mux.HandleFunc("/jobs/result", s.authed(s.handleResult))
	log.Printf("agent API listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("http server: %v", err)
	}
}

func (s *server) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+s.agentToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *server) handleNext(w http.ResponseWriter, r *http.Request) {
	s.live.touch()
	job, err := s.st.ClaimNext()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if job == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	json.NewEncoder(w).Encode(job)
}

func (s *server) handleResult(w http.ResponseWriter, r *http.Request) {
	var res api.Result
	if err := json.NewDecoder(r.Body).Decode(&res); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	chatID, err := s.st.Complete(res)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	text := res.Result
	if res.IsError {
		text = "⚠️ Ошибка выполнения: " + res.ErrorText
	}
	totalTokens := res.InputTokens + res.OutputTokens + res.CacheCreationTokens
	msg := tgbotapi.NewMessage(chatID, fmt.Sprintf(
		"%s\n\n💵 $%.4f · 🔤 %s токенов (вход %s, выход %s, кэш-чтение %s)",
		text, res.CostUSD,
		humanCount(totalTokens), humanCount(res.InputTokens), humanCount(res.OutputTokens), humanCount(res.CacheReadTokens),
	))
	if _, err := s.bot.Send(msg); err != nil {
		log.Printf("send result to chat %d: %v", chatID, err)
	}

	if state, err := s.st.GetChatState(chatID); err == nil && state.Mode == "confirm" {
		if heldID, _ := s.st.NextHeld(chatID); heldID != 0 {
			count, _ := s.st.CountHeld(chatID)
			cm := tgbotapi.NewMessage(chatID, "Готово. В очереди ещё есть промт — продолжить?")
			cm.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData(
						fmt.Sprintf("▶️ Продолжить (ещё %d)", count), fmt.Sprintf("continue:%d", heldID)),
				),
			)
			s.bot.Send(cm)
		}
	}
	w.WriteHeader(http.StatusOK)
}

// --- Telegram side: talks to the user ---

type tgHandler struct {
	bot         *tgbotapi.BotAPI
	st          *store.Store
	cfg         *config.Config
	adminChatID int64
	live        *liveness
}

func (h *tgHandler) run() {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := h.bot.GetUpdatesChan(u)

	for update := range updates {
		if update.CallbackQuery != nil {
			h.handleCallback(update.CallbackQuery)
			continue
		}
		if update.Message == nil {
			continue
		}
		h.handleMessage(update.Message)
	}
}

func (h *tgHandler) allowed(chatID int64) bool { return chatID == h.adminChatID }

func (h *tgHandler) handleMessage(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	if !h.allowed(chatID) {
		return
	}

	switch {
	case msg.Command() == "start":
		h.send(chatID, "👋 Готов.\n\n"+
			"/projects — выбрать проект\n"+
			"/mode — автономно / пошагово\n"+
			"/offline — поведение при офлайне ПК\n"+
			"/status — текущие настройки и расход\n\n"+
			"После выбора проекта — просто пишите промт обычным сообщением.")
		return
	case msg.Command() == "projects":
		h.sendProjectPicker(chatID)
		return
	case msg.Command() == "mode":
		h.sendModePicker(chatID)
		return
	case msg.Command() == "offline":
		h.sendOfflinePicker(chatID)
		return
	case msg.Command() == "status":
		h.sendStatus(chatID)
		return
	}

	state, err := h.st.GetChatState(chatID)
	if err != nil || state.CurrentProject == "" {
		h.send(chatID, "Сначала выберите проект: /projects")
		return
	}

	status := "pending"
	if state.Mode == "confirm" {
		if active, _ := h.st.HasActiveJob(chatID); active {
			status = "held"
		}
	}

	jobID, err := h.st.Enqueue(chatID, state.CurrentProject, msg.Text, "acceptEdits", 0, status)
	if err != nil {
		h.send(chatID, "Не получилось поставить в очередь: "+err.Error())
		return
	}

	label := "📥 В очереди"
	if status == "held" {
		label = "⏸ Ждёт своей очереди (пошаговый режим — сначала завершится текущее)"
	}
	text := fmt.Sprintf("%s (задание #%d, проект: %s)", label, jobID, state.CurrentProject)
	if state.OfflineBehavior == "notify" && !h.live.online(agentOfflineThreshold) {
		text += "\n⚠️ Агент на ПК сейчас не отвечает — выполнится, как только ПК будет на связи."
	}
	h.send(chatID, text)
}

func (h *tgHandler) handleCallback(cb *tgbotapi.CallbackQuery) {
	chatID := cb.Message.Chat.ID
	if !h.allowed(chatID) {
		return
	}
	h.bot.Request(tgbotapi.NewCallback(cb.ID, ""))

	switch {
	case len(cb.Data) > 8 && cb.Data[:8] == "project:":
		project := cb.Data[8:]
		h.st.SetProject(chatID, project)
		h.send(chatID, "✅ Текущий проект: "+project)
	case len(cb.Data) > 5 && cb.Data[:5] == "mode:":
		mode := cb.Data[5:]
		h.st.SetMode(chatID, mode)
		h.send(chatID, "✅ Режим очереди: "+mode)
	case len(cb.Data) > 8 && cb.Data[:8] == "offline:":
		behavior := cb.Data[8:]
		h.st.SetOfflineBehavior(chatID, behavior)
		h.send(chatID, "✅ При офлайне ПК: "+behavior)
	case len(cb.Data) > 9 && cb.Data[:9] == "continue:":
		id, err := strconv.ParseInt(cb.Data[9:], 10, 64)
		if err != nil {
			return
		}
		if err := h.st.Release(chatID, id); err != nil {
			h.send(chatID, "Не получилось продолжить: "+err.Error())
			return
		}
		h.send(chatID, "▶️ Запускаю следующее")
	}
}

func (h *tgHandler) sendProjectPicker(chatID int64) {
	var rows [][]tgbotapi.InlineKeyboardButton
	for name := range h.cfg.Projects {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(name, "project:"+name),
		))
	}
	msg := tgbotapi.NewMessage(chatID, "Выберите проект:")
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(rows...)
	h.bot.Send(msg)
}

func (h *tgHandler) sendModePicker(chatID int64) {
	msg := tgbotapi.NewMessage(chatID, "Режим выполнения серии промтов:")
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Автономно", "mode:auto"),
			tgbotapi.NewInlineKeyboardButtonData("Пошагово", "mode:confirm"),
		),
	)
	h.bot.Send(msg)
}

func (h *tgHandler) sendOfflinePicker(chatID int64) {
	msg := tgbotapi.NewMessage(chatID, "Если ПК офлайн, когда пришёл промт:")
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Ждать в очереди", "offline:queue"),
			tgbotapi.NewInlineKeyboardButtonData("Сообщить сразу", "offline:notify"),
		),
	)
	h.bot.Send(msg)
}

func (h *tgHandler) sendStatus(chatID int64) {
	state, _ := h.st.GetChatState(chatID)
	cost, count, tokens, _ := h.st.RecentCost(chatID, 5*time.Hour)
	text := fmt.Sprintf(
		"Проект: %s\nРежим: %s\nПри офлайне: %s\n\nЗа последние 5 часов: %d запросов, $%.4f, %s токенов",
		orDash(state.CurrentProject), orDash(state.Mode), orDash(state.OfflineBehavior), count, cost, humanCount(tokens),
	)
	h.send(chatID, text)
}

func humanCount(n int64) string { return humanize.Comma(n) }

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func (h *tgHandler) send(chatID int64, text string) {
	if _, err := h.bot.Send(tgbotapi.NewMessage(chatID, text)); err != nil {
		log.Printf("send to %d: %v", chatID, err)
	}
}
