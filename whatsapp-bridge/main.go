package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/mdp/qrterminal/v3"
	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

var (
	msgDB    *sql.DB
	mediaDir string
	client   *whatsmeow.Client
	ctx      = context.Background()
)

// audioMediaInfo armazena metadados necessários para baixar áudio do histórico
type audioMediaInfo struct {
	URL           string `json:"url"`
	DirectPath    string `json:"direct_path"`
	MediaKey      string `json:"media_key"`       // base64
	FileEncSHA256 string `json:"file_enc_sha256"` // base64
	FileSHA256    string `json:"file_sha256"`     // base64
	FileLength    uint64 `json:"file_length"`
	Mimetype      string `json:"mimetype"`
	PTT           bool   `json:"ptt"`
}

func main() {
	_ = godotenv.Load("../.env")

	sessionDB := getEnv("SESSION_DB", "./data/whatsapp-session.db")
	messagesDB := getEnv("MESSAGES_DB", "./data/messages.db")
	mediaDir = getEnv("MEDIA_DIR", "./data/media")
	bridgePort := getEnv("BRIDGE_PORT", "8765")

	for _, dir := range []string{filepath.Dir(sessionDB), filepath.Dir(messagesDB), mediaDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "Erro ao criar diretório %s: %v\n", dir, err)
			os.Exit(1)
		}
	}

	var err error
	msgDB, err = sql.Open("sqlite3", messagesDB+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao abrir banco de dados: %v\n", err)
		os.Exit(1)
	}
	defer msgDB.Close()

	if err := initMessagesDB(); err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao inicializar banco de dados: %v\n", err)
		os.Exit(1)
	}

	dbLog := waLog.Stdout("Database", "ERROR", true)
	container, err := sqlstore.New(ctx, "sqlite3", "file:"+sessionDB+"?_foreign_keys=on", dbLog)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao inicializar store: %v\n", err)
		os.Exit(1)
	}

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao obter dispositivo: %v\n", err)
		os.Exit(1)
	}

	clientLog := waLog.Stdout("Client", "INFO", true)
	client = whatsmeow.NewClient(deviceStore, clientLog)
	client.AddEventHandler(eventHandler)

	// Inicia HTTP server para download sob demanda
	go startHTTPServer(bridgePort)

	if client.Store.ID == nil {
		qrChan, _ := client.GetQRChannel(ctx)
		if err := client.Connect(); err != nil {
			fmt.Fprintf(os.Stderr, "Erro ao conectar: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("\n=================================================")
		fmt.Println("  Escaneie o QR Code abaixo com seu WhatsApp")
		fmt.Println("  WhatsApp > Aparelhos conectados > Conectar aparelho")
		fmt.Println("=================================================\n")
		for evt := range qrChan {
			if evt.Event == "code" {
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
			} else {
				fmt.Printf("Status QR: %s\n", evt.Event)
			}
		}
	} else {
		if err := client.Connect(); err != nil {
			fmt.Fprintf(os.Stderr, "Erro ao reconectar: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Conectado como: %s\n", client.Store.ID.String())
	}

	fmt.Printf("\nWhatsApp Bridge rodando (API em :%s). Aguardando mensagens...\n", bridgePort)
	fmt.Println("Pressione Ctrl+C para encerrar.")

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	fmt.Println("\nEncerrando...")
	client.Disconnect()
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP Server para download sob demanda
// ─────────────────────────────────────────────────────────────────────────────

func startHTTPServer(port string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/download", handleDownload)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"ok"}`)
	})
	fmt.Printf("[Bridge] HTTP API iniciada em :%s\n", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		fmt.Fprintf(os.Stderr, "[Bridge] Erro no HTTP server: %v\n", err)
	}
}

// handleDownload baixa um áudio histórico e retorna o caminho do arquivo
func handleDownload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	msgID := r.URL.Query().Get("id")
	chatJID := r.URL.Query().Get("jid")

	if msgID == "" || chatJID == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"id e jid são obrigatórios"}`)
		return
	}

	// Busca a mensagem no banco
	var mediaInfoJSON, mediaPath sql.NullString
	var msgType string
	err := msgDB.QueryRow(
		`SELECT message_type, media_path, media_info FROM messages WHERE id = ? AND chat_jid = ?`,
		msgID, chatJID,
	).Scan(&msgType, &mediaPath, &mediaInfoJSON)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"error":"mensagem não encontrada: %s"}`, err)
		return
	}

	// Já tem arquivo baixado
	if mediaPath.Valid && mediaPath.String != "" {
		if _, err := os.Stat(mediaPath.String); err == nil {
			resp, _ := json.Marshal(map[string]string{"path": mediaPath.String})
			fmt.Fprint(w, string(resp))
			return
		}
	}

	// Precisa baixar — verifica se temos os metadados
	if !mediaInfoJSON.Valid || mediaInfoJSON.String == "" {
		w.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprint(w, `{"error":"metadados de mídia não disponíveis para esta mensagem histórica"}`)
		return
	}

	var info audioMediaInfo
	if err := json.Unmarshal([]byte(mediaInfoJSON.String), &info); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":"erro ao decodificar metadados: %s"}`, err)
		return
	}

	// Reconstrói o proto AudioMessage para download
	mediaKey, _ := base64.StdEncoding.DecodeString(info.MediaKey)
	fileEncSHA256, _ := base64.StdEncoding.DecodeString(info.FileEncSHA256)
	fileSHA256, _ := base64.StdEncoding.DecodeString(info.FileSHA256)

	audioMsg := &waE2E.AudioMessage{
		URL:           proto.String(info.URL),
		DirectPath:    proto.String(info.DirectPath),
		MediaKey:      mediaKey,
		FileEncSHA256: fileEncSHA256,
		FileSHA256:    fileSHA256,
		FileLength:    proto.Uint64(info.FileLength),
		Mimetype:      proto.String(info.Mimetype),
		PTT:           proto.Bool(info.PTT),
	}

	data, err := client.Download(ctx, audioMsg)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":"erro ao baixar áudio: %s"}`, err)
		return
	}

	// Determina extensão
	fileExt := ".ogg"
	if strings.Contains(info.Mimetype, "mp4") {
		fileExt = ".mp4"
	}

	subdir := "ptt"
	if !info.PTT {
		subdir = "audio"
	}

	safeID := strings.ReplaceAll(msgID, "/", "_")
	safeJID := strings.ReplaceAll(strings.ReplaceAll(chatJID, "@", "_"), ".", "_")
	filename := fmt.Sprintf("%s_%s%s", safeJID, safeID, fileExt)
	path := filepath.Join(mediaDir, subdir, filename)

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":"erro ao criar diretório: %s"}`, err)
		return
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":"erro ao salvar arquivo: %s"}`, err)
		return
	}

	// Atualiza media_path no banco
	_, _ = msgDB.Exec(
		`UPDATE messages SET media_path = ? WHERE id = ? AND chat_jid = ?`,
		path, msgID, chatJID,
	)

	fmt.Printf("[Bridge] Áudio baixado sob demanda: %s\n", path)
	resp, _ := json.Marshal(map[string]string{"path": path})
	fmt.Fprint(w, string(resp))
}

// ─────────────────────────────────────────────────────────────────────────────
// Inicialização do banco de dados
// ─────────────────────────────────────────────────────────────────────────────

func initMessagesDB() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS chats (
			jid TEXT PRIMARY KEY,
			name TEXT,
			is_group INTEGER NOT NULL DEFAULT 0,
			last_message_time INTEGER,
			unread_count INTEGER DEFAULT 0,
			updated_at INTEGER DEFAULT (strftime('%s', 'now'))
		)`,
		`CREATE TABLE IF NOT EXISTS messages (
			id TEXT NOT NULL,
			chat_jid TEXT NOT NULL,
			sender_jid TEXT NOT NULL,
			sender_name TEXT,
			content TEXT,
			message_type TEXT NOT NULL DEFAULT 'text',
			timestamp INTEGER NOT NULL,
			is_from_me INTEGER NOT NULL DEFAULT 0,
			media_path TEXT,
			media_info TEXT,
			transcription TEXT,
			PRIMARY KEY (id, chat_jid)
		)`,
		`CREATE TABLE IF NOT EXISTS contacts (
			jid TEXT PRIMARY KEY,
			name TEXT,
			push_name TEXT,
			updated_at INTEGER DEFAULT (strftime('%s', 'now'))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_chat_time ON messages(chat_jid, timestamp DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_timestamp ON messages(timestamp DESC)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
			id, chat_jid, content, sender_name,
			content='messages',
			content_rowid='rowid'
		)`,
		`CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
			INSERT OR IGNORE INTO messages_fts(rowid, id, chat_jid, content, sender_name)
			VALUES (new.rowid, new.id, new.chat_jid, new.content, new.sender_name);
		END`,
	}

	for _, q := range queries {
		if _, err := msgDB.Exec(q); err != nil {
			return fmt.Errorf("erro ao executar query: %w\n%s", err, q)
		}
	}

	// Migração: adiciona coluna media_info se não existir (para DBs antigos)
	_, _ = msgDB.Exec(`ALTER TABLE messages ADD COLUMN media_info TEXT`)

	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Event handlers
// ─────────────────────────────────────────────────────────────────────────────

func eventHandler(rawEvt interface{}) {
	switch evt := rawEvt.(type) {
	case *events.Message:
		handleMessage(evt)
	case *events.HistorySync:
		handleHistorySync(evt)
	case *events.GroupInfo:
		handleGroupInfo(evt)
	case *events.PushNameSetting:
		if evt.Action != nil {
			updateContact(client.Store.ID.User, "", evt.Action.GetName())
		}
	case *events.Connected:
		fmt.Println("[Bridge] Conectado ao WhatsApp")
		go func() {
			syncContactsFromStore()
			enrichContactNamesFromHistory()
		}()
	case *events.Disconnected:
		fmt.Println("[Bridge] Desconectado do WhatsApp")
	case *events.LoggedOut:
		fmt.Println("[Bridge] Sessão encerrada. Delete whatsapp-session.db para fazer login novamente.")
		os.Exit(0)
	}
}

func handleMessage(evt *events.Message) {
	msg := evt.Message
	if msg == nil {
		return
	}

	chatJID := evt.Info.Chat.String()
	senderJID := evt.Info.Sender.String()
	senderName := getSenderName(evt)
	isFromMe := evt.Info.IsFromMe
	timestamp := evt.Info.Timestamp.Unix()
	msgID := evt.Info.ID

	msgType, content, mediaPath := extractMessage(evt)
	if msgType == "" {
		return
	}

	_, err := msgDB.Exec(
		`INSERT OR REPLACE INTO messages
		(id, chat_jid, sender_jid, sender_name, content, message_type, timestamp, is_from_me, media_path)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msgID, chatJID, senderJID, senderName, content, msgType, timestamp, boolToInt(isFromMe), mediaPath,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[Bridge] Erro ao salvar mensagem %s: %v\n", msgID, err)
		return
	}

	updateChat(evt, chatJID, timestamp)
	if senderName != "" {
		updateContact(senderJID, "", senderName)
	}

	direction := "<<"
	if isFromMe {
		direction = ">>"
	}
	fmt.Printf("[%s] %s %s (%s): %s\n",
		time.Unix(timestamp, 0).Format("15:04"),
		direction, chatJID, senderName, truncate(content, 80),
	)
}

func handleHistorySync(evt *events.HistorySync) {
	data := evt.Data
	if data == nil {
		return
	}

	total := 0
	for _, conv := range data.GetConversations() {
		chatJID := conv.GetID()
		if chatJID == "" {
			continue
		}

		chatName := conv.GetName()
		isGroup := strings.Contains(chatJID, "@g.us")

		_, _ = msgDB.Exec(
			`INSERT INTO chats (jid, name, is_group, updated_at)
			VALUES (?, ?, ?, strftime('%s', 'now'))
			ON CONFLICT(jid) DO UPDATE SET
				name = COALESCE(NULLIF(excluded.name, ''), name),
				updated_at = excluded.updated_at`,
			chatJID, chatName, boolToInt(isGroup),
		)

		// HistorySync como primeira fonte: salva o nome na tabela contacts também
		// (para contatos individuais). syncContactsFromStore sobrescreve depois
		// com o FullName da agenda se disponível.
		if !isGroup && chatName != "" {
			updateContact(chatJID, chatName, "")
		}

		for _, histMsg := range conv.GetMessages() {
			webMsg := histMsg.GetMessage()
			if webMsg == nil {
				continue
			}

			msgInfo := webMsg.GetMessage()
			if msgInfo == nil {
				continue
			}

			msgID := webMsg.GetKey().GetID()
			senderJID := chatJID
			isFromMe := webMsg.GetKey().GetFromMe()

			if !isFromMe && webMsg.GetKey().GetParticipant() != "" {
				senderJID = webMsg.GetKey().GetParticipant()
			} else if !isFromMe && isGroup {
				continue
			}

			senderName := webMsg.GetPushName()
			timestamp := int64(webMsg.GetMessageTimestamp())

			// Salva push_name do histórico na tabela contacts
			if senderName != "" {
				updateContact(senderJID, "", senderName)
			}

			msgType, content, mediaInfoJSON := extractFromProto(msgInfo)
			if msgType == "" {
				continue
			}

			var mediaInfoArg interface{} = nil
			if mediaInfoJSON != "" {
				mediaInfoArg = mediaInfoJSON
			}

			_, err := msgDB.Exec(
				`INSERT OR IGNORE INTO messages
				(id, chat_jid, sender_jid, sender_name, content, message_type, timestamp, is_from_me, media_info)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				msgID, chatJID, senderJID, senderName, content, msgType, timestamp, boolToInt(isFromMe), mediaInfoArg,
			)
			if err == nil {
				total++
				_, _ = msgDB.Exec(
					`UPDATE chats SET last_message_time = MAX(COALESCE(last_message_time, 0), ?) WHERE jid = ?`,
					timestamp, chatJID,
				)
			}
		}
	}

	syncType := data.GetSyncType().String()
	fmt.Printf("[Bridge] HistorySync (%s): %d mensagens salvas\n", syncType, total)
	_ = waHistorySync.HistorySync_RECENT
}

// ─────────────────────────────────────────────────────────────────────────────
// Extração de mensagens
// ─────────────────────────────────────────────────────────────────────────────

func extractMessage(evt *events.Message) (msgType, content, mediaPath string) {
	msg := evt.Message

	if text := msg.GetConversation(); text != "" {
		return "text", text, ""
	}
	if ext := msg.GetExtendedTextMessage(); ext != nil {
		return "text", ext.GetText(), ""
	}
	if audio := msg.GetAudioMessage(); audio != nil {
		msgType = "audio"
		if audio.GetPTT() {
			msgType = "ptt"
		}
		if getEnv("AUTO_DOWNLOAD_AUDIO", "true") == "true" {
			path := downloadMediaMsg(evt, audio, msgType)
			return msgType, "[Mensagem de áudio]", path
		}
		return msgType, "[Mensagem de áudio]", ""
	}
	if img := msg.GetImageMessage(); img != nil {
		caption := img.GetCaption()
		if caption == "" {
			caption = "[Imagem]"
		}
		if getEnv("AUTO_DOWNLOAD_IMAGES", "false") == "true" {
			path := downloadMediaMsg(evt, img, "image")
			return "image", caption, path
		}
		return "image", caption, ""
	}
	if vid := msg.GetVideoMessage(); vid != nil {
		caption := vid.GetCaption()
		if caption == "" {
			caption = "[Vídeo]"
		}
		return "video", caption, ""
	}
	if doc := msg.GetDocumentMessage(); doc != nil {
		return "document", fmt.Sprintf("[Documento: %s]", doc.GetFileName()), ""
	}
	if msg.GetStickerMessage() != nil {
		return "sticker", "[Sticker]", ""
	}
	if loc := msg.GetLocationMessage(); loc != nil {
		return "location", fmt.Sprintf("[Localização: %.6f, %.6f]", loc.GetDegreesLatitude(), loc.GetDegreesLongitude()), ""
	}
	if contact := msg.GetContactMessage(); contact != nil {
		return "contact", fmt.Sprintf("[Contato: %s]", contact.GetDisplayName()), ""
	}
	if reaction := msg.GetReactionMessage(); reaction != nil {
		return "reaction", fmt.Sprintf("[Reação: %s]", reaction.GetText()), ""
	}
	return "", "", ""
}

// extractFromProto extrai tipo, conteúdo e media_info JSON de mensagens do history sync
func extractFromProto(msg *waE2E.Message) (msgType, content, mediaInfoJSON string) {
	if msg == nil {
		return "", "", ""
	}
	if text := msg.GetConversation(); text != "" {
		return "text", text, ""
	}
	if ext := msg.GetExtendedTextMessage(); ext != nil && ext.GetText() != "" {
		return "text", ext.GetText(), ""
	}
	if audio := msg.GetAudioMessage(); audio != nil {
		isPTT := audio.GetPTT()
		t := "audio"
		if isPTT {
			t = "ptt"
		}
		info := audioMediaInfo{
			URL:           audio.GetURL(),
			DirectPath:    audio.GetDirectPath(),
			MediaKey:      base64.StdEncoding.EncodeToString(audio.GetMediaKey()),
			FileEncSHA256: base64.StdEncoding.EncodeToString(audio.GetFileEncSHA256()),
			FileSHA256:    base64.StdEncoding.EncodeToString(audio.GetFileSHA256()),
			FileLength:    audio.GetFileLength(),
			Mimetype:      audio.GetMimetype(),
			PTT:           isPTT,
		}
		// Só salva media_info se tiver URL (mensagens recentes têm, antigas não)
		jsonStr := ""
		if info.URL != "" && info.MediaKey != "" {
			b, _ := json.Marshal(info)
			jsonStr = string(b)
		}
		label := "[Mensagem de voz]"
		if !isPTT {
			label = "[Áudio]"
		}
		return t, label, jsonStr
	}
	if img := msg.GetImageMessage(); img != nil {
		caption := img.GetCaption()
		if caption == "" {
			caption = "[Imagem]"
		}
		return "image", caption, ""
	}
	if vid := msg.GetVideoMessage(); vid != nil {
		caption := vid.GetCaption()
		if caption == "" {
			caption = "[Vídeo]"
		}
		return "video", caption, ""
	}
	if doc := msg.GetDocumentMessage(); doc != nil {
		return "document", fmt.Sprintf("[Documento: %s]", doc.GetFileName()), ""
	}
	if msg.GetStickerMessage() != nil {
		return "sticker", "[Sticker]", ""
	}
	if loc := msg.GetLocationMessage(); loc != nil {
		return "location", fmt.Sprintf("[Localização: %.6f, %.6f]", loc.GetDegreesLatitude(), loc.GetDegreesLongitude()), ""
	}
	if contact := msg.GetContactMessage(); contact != nil {
		return "contact", fmt.Sprintf("[Contato: %s]", contact.GetDisplayName()), ""
	}
	if reaction := msg.GetReactionMessage(); reaction != nil {
		return "reaction", fmt.Sprintf("[Reação: %s]", reaction.GetText()), ""
	}
	return "", "", ""
}

// downloadMediaMsg baixa mídia de mensagens em tempo real
func downloadMediaMsg(evt *events.Message, mediaMsg interface{}, mediaType string) string {
	var url, mimetype, fileExt string
	var mediaKey []byte

	switch m := mediaMsg.(type) {
	case *waE2E.AudioMessage:
		url = m.GetURL()
		mediaKey = m.GetMediaKey()
		mimetype = m.GetMimetype()
		fileExt = ".ogg"
		if strings.Contains(mimetype, "mp4") {
			fileExt = ".mp4"
		}
	case *waE2E.ImageMessage:
		url = m.GetURL()
		mediaKey = m.GetMediaKey()
		mimetype = m.GetMimetype()
		fileExt = ".jpg"
		if strings.Contains(mimetype, "png") {
			fileExt = ".png"
		}
	default:
		return ""
	}

	if url == "" || len(mediaKey) == 0 {
		return ""
	}

	var data []byte
	var err error

	switch m := mediaMsg.(type) {
	case *waE2E.AudioMessage:
		data, err = client.Download(ctx, m)
	case *waE2E.ImageMessage:
		data, err = client.Download(ctx, m)
	}

	if err != nil {
		resp, httpErr := http.Get(url)
		if httpErr != nil {
			fmt.Fprintf(os.Stderr, "[Bridge] Erro ao baixar mídia: %v\n", err)
			return ""
		}
		defer resp.Body.Close()
		data, err = io.ReadAll(resp.Body)
		if err != nil {
			return ""
		}
	}

	safeChat := strings.ReplaceAll(strings.ReplaceAll(evt.Info.Chat.String(), "@", "_"), ".", "_")
	filename := fmt.Sprintf("%s_%s%s", safeChat, evt.Info.ID, fileExt)
	path := filepath.Join(mediaDir, mediaType, filename)

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return ""
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "[Bridge] Erro ao salvar mídia: %v\n", err)
		return ""
	}
	return path
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers de banco de dados
// ─────────────────────────────────────────────────────────────────────────────

func updateChat(evt *events.Message, chatJID string, timestamp int64) {
	isGroup := evt.Info.Chat.Server == types.GroupServer
	var name string
	if isGroup {
		groupInfo, err := client.GetGroupInfo(ctx, evt.Info.Chat)
		if err == nil && groupInfo != nil {
			name = groupInfo.Name
		}
	} else {
		name = evt.Info.PushName
		if name == "" {
			name = evt.Info.Sender.User
		}
	}
	_, err := msgDB.Exec(
		`INSERT INTO chats (jid, name, is_group, last_message_time, updated_at)
		VALUES (?, ?, ?, ?, strftime('%s', 'now'))
		ON CONFLICT(jid) DO UPDATE SET
			name = COALESCE(NULLIF(excluded.name, ''), name),
			last_message_time = excluded.last_message_time,
			updated_at = excluded.updated_at`,
		chatJID, name, boolToInt(isGroup), timestamp,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[Bridge] Erro ao atualizar chat: %v\n", err)
	}
}

func handleGroupInfo(evt *events.GroupInfo) {
	if evt.Name != nil {
		_, err := msgDB.Exec(
			`INSERT INTO chats (jid, name, is_group, updated_at)
			VALUES (?, ?, 1, strftime('%s', 'now'))
			ON CONFLICT(jid) DO UPDATE SET name = excluded.name, updated_at = excluded.updated_at`,
			evt.JID.String(), evt.Name.Name,
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[Bridge] Erro ao atualizar nome do grupo: %v\n", err)
		}
	}
}

// enrichContactNamesFromHistory preenche nomes de chats ainda sem nome.
// Ordem de prioridade:
//  1. contacts.name  (HistorySync conv.GetName + agenda via syncContactsFromStore)
//  2. contacts.push_name (PushName dos remetentes do histórico)
//  3. messages.sender_name (fallback direto das mensagens)
//  4. Número de telefone extraído do JID (@s.whatsapp.net)
func enrichContactNamesFromHistory() {
	// 1. Usa push_name da tabela contacts para chats ainda sem nome
	res1, _ := msgDB.Exec(`
		UPDATE chats
		SET name = (
			SELECT COALESCE(NULLIF(ct.name,''), ct.push_name)
			FROM contacts ct
			WHERE ct.jid = chats.jid
			  AND (ct.name != '' OR ct.push_name != '')
			LIMIT 1
		)
		WHERE (name IS NULL OR name = '') AND is_group = 0
		  AND EXISTS (
			SELECT 1 FROM contacts ct WHERE ct.jid = chats.jid
			  AND (ct.name IS NOT NULL AND ct.name != ''
			       OR ct.push_name IS NOT NULL AND ct.push_name != '')
		  )`)
	n1, _ := res1.RowsAffected()

	// 2. Usa sender_name do histórico de mensagens para os que restam
	res2, _ := msgDB.Exec(`
		UPDATE chats
		SET name = (
			SELECT sender_name FROM messages
			WHERE messages.chat_jid = chats.jid
			  AND messages.is_from_me = 0
			  AND messages.sender_name IS NOT NULL
			  AND messages.sender_name != ''
			ORDER BY messages.timestamp DESC LIMIT 1
		)
		WHERE (name IS NULL OR name = '') AND is_group = 0
		  AND EXISTS (
			SELECT 1 FROM messages
			WHERE messages.chat_jid = chats.jid
			  AND messages.is_from_me = 0
			  AND messages.sender_name IS NOT NULL AND messages.sender_name != ''
		  )`)
	n2, _ := res2.RowsAffected()

	// 3. Fallback: usa número de telefone extraído do JID (@s.whatsapp.net)
	res3, _ := msgDB.Exec(`
		UPDATE chats
		SET name = '+' || REPLACE(jid, '@s.whatsapp.net', '')
		WHERE (name IS NULL OR name = '') AND is_group = 0
		  AND jid LIKE '%@s.whatsapp.net'`)
	n3, _ := res3.RowsAffected()

	fmt.Printf("[Bridge] Enriquecimento de nomes: %d de contacts, %d do histórico, %d de número de telefone\n", n1, n2, n3)
}

// syncContactsFromStore lê todos os contatos do store do whatsmeow (agenda do celular
// sincronizada pelo WhatsApp) e atualiza o banco com o FullName de cada um.
func syncContactsFromStore() {
	contacts, err := client.Store.Contacts.GetAllContacts(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[Bridge] Erro ao buscar contatos da agenda: %v\n", err)
		return
	}

	updated := 0
	for jid, info := range contacts {
		// Usa FullName (agenda) com fallback para PushName (nome no WhatsApp)
		name := info.FullName
		if name == "" {
			name = info.PushName
		}
		if name == "" {
			continue
		}

		jidStr := jid.String()

		// Atualiza tabela contacts — FullName tem prioridade sobre push_name existente
		_, _ = msgDB.Exec(
			`INSERT INTO contacts (jid, name, push_name, updated_at)
			VALUES (?, ?, ?, strftime('%s', 'now'))
			ON CONFLICT(jid) DO UPDATE SET
				name = COALESCE(NULLIF(excluded.name, ''), name),
				push_name = COALESCE(NULLIF(excluded.push_name, ''), push_name),
				updated_at = excluded.updated_at`,
			jidStr, info.FullName, info.PushName,
		)

		// Se tiver FullName da agenda, ele é autoritativo — sempre atualiza o chat
		if info.FullName != "" {
			_, _ = msgDB.Exec(
				`UPDATE chats SET name = ? WHERE jid = ?`,
				info.FullName, jidStr,
			)
		}

		updated++
	}

	fmt.Printf("[Bridge] Agenda sincronizada: %d contatos atualizados\n", updated)
}

func updateContact(jid, name, pushName string) {
	_, _ = msgDB.Exec(
		`INSERT INTO contacts (jid, name, push_name, updated_at)
		VALUES (?, ?, ?, strftime('%s', 'now'))
		ON CONFLICT(jid) DO UPDATE SET
			name = COALESCE(NULLIF(excluded.name, ''), name),
			push_name = COALESCE(NULLIF(excluded.push_name, ''), push_name),
			updated_at = excluded.updated_at`,
		jid, name, pushName,
	)
}

// ─────────────────────────────────────────────────────────────────────────────
// Utilitários
// ─────────────────────────────────────────────────────────────────────────────

func getSenderName(evt *events.Message) string {
	if evt.Info.PushName != "" {
		return evt.Info.PushName
	}
	return evt.Info.Sender.User
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
