package main

import (
	"context"
	"database/sql"
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
)

func main() {
	// Carrega variáveis de ambiente
	_ = godotenv.Load("../.env")

	sessionDB := getEnv("SESSION_DB", "./data/whatsapp-session.db")
	messagesDB := getEnv("MESSAGES_DB", "./data/messages.db")
	mediaDir = getEnv("MEDIA_DIR", "./data/media")

	// Cria diretórios necessários
	for _, dir := range []string{
		filepath.Dir(sessionDB),
		filepath.Dir(messagesDB),
		mediaDir,
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "Erro ao criar diretório %s: %v\n", dir, err)
			os.Exit(1)
		}
	}

	// Inicializa banco de dados de mensagens
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

	// Inicializa store da sessão WhatsApp
	dbLog := waLog.Stdout("Database", "ERROR", true)
	container, err := sqlstore.New("sqlite3", "file:"+sessionDB+"?_foreign_keys=on", dbLog)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao inicializar store: %v\n", err)
		os.Exit(1)
	}

	deviceStore, err := container.GetFirstDevice()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao obter dispositivo: %v\n", err)
		os.Exit(1)
	}

	clientLog := waLog.Stdout("Client", "INFO", true)
	client = whatsmeow.NewClient(deviceStore, clientLog)
	client.AddEventHandler(eventHandler)

	// Conecta ao WhatsApp
	if client.Store.ID == nil {
		// Primeira vez: necessário escanear QR code
		qrChan, _ := client.GetQRChannel(context.Background())
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
		// Sessão existente: reconecta automaticamente
		if err := client.Connect(); err != nil {
			fmt.Fprintf(os.Stderr, "Erro ao reconectar: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Conectado como: %s\n", client.Store.ID.String())
	}

	fmt.Println("\nWhatsApp Bridge rodando. Aguardando mensagens...")
	fmt.Println("Pressione Ctrl+C para encerrar.")

	// Aguarda sinal de encerramento
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	fmt.Println("\nEncerrando...")
	client.Disconnect()
}

// initMessagesDB cria as tabelas necessárias no banco de dados
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
	return nil
}

// eventHandler processa todos os eventos do WhatsApp
func eventHandler(rawEvt interface{}) {
	switch evt := rawEvt.(type) {
	case *events.Message:
		handleMessage(evt)
	case *events.GroupInfo:
		handleGroupInfo(evt)
	case *events.PushNameSetting:
		if evt.Action != nil {
			updateContact(client.Store.ID.User, "", evt.Action.GetName())
		}
	case *events.Connected:
		fmt.Println("[Bridge] Conectado ao WhatsApp")
	case *events.Disconnected:
		fmt.Println("[Bridge] Desconectado do WhatsApp")
	case *events.LoggedOut:
		fmt.Println("[Bridge] Sessão encerrada. Delete whatsapp-session.db para fazer login novamente.")
		os.Exit(0)
	}
}

// handleMessage processa e salva uma mensagem recebida
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

	// Determina o tipo e conteúdo da mensagem
	msgType, content, mediaPath := extractMessage(evt)
	if msgType == "" {
		return // Ignora tipos não suportados
	}

	// Salva no banco de dados
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

	// Atualiza informações do chat
	updateChat(evt, chatJID, timestamp)

	// Atualiza contato
	if senderName != "" {
		updateContact(senderJID, "", senderName)
	}

	direction := "<<"
	if isFromMe {
		direction = ">>"
	}
	fmt.Printf("[%s] %s %s (%s): %s\n",
		time.Unix(timestamp, 0).Format("15:04"),
		direction,
		chatJID,
		senderName,
		truncate(content, 80),
	)
}

// extractMessage extrai tipo, conteúdo e caminho de mídia de uma mensagem
func extractMessage(evt *events.Message) (msgType, content, mediaPath string) {
	msg := evt.Message

	// Texto simples
	if text := msg.GetConversation(); text != "" {
		return "text", text, ""
	}

	// Texto estendido (com preview de link, etc.)
	if ext := msg.GetExtendedTextMessage(); ext != nil {
		return "text", ext.GetText(), ""
	}

	// Áudio / PTT (Push-to-Talk)
	if audio := msg.GetAudioMessage(); audio != nil {
		msgType = "audio"
		if audio.GetPtt() {
			msgType = "ptt"
		}
		autoDownload := getEnv("AUTO_DOWNLOAD_AUDIO", "true") == "true"
		if autoDownload {
			path := downloadMedia(evt, audio, msgType)
			return msgType, "[Mensagem de áudio]", path
		}
		return msgType, "[Mensagem de áudio]", ""
	}

	// Imagem
	if img := msg.GetImageMessage(); img != nil {
		caption := img.GetCaption()
		if caption == "" {
			caption = "[Imagem]"
		}
		autoDownload := getEnv("AUTO_DOWNLOAD_IMAGES", "false") == "true"
		if autoDownload {
			path := downloadMedia(evt, img, "image")
			return "image", caption, path
		}
		return "image", caption, ""
	}

	// Vídeo
	if vid := msg.GetVideoMessage(); vid != nil {
		caption := vid.GetCaption()
		if caption == "" {
			caption = "[Vídeo]"
		}
		return "video", caption, ""
	}

	// Documento
	if doc := msg.GetDocumentMessage(); doc != nil {
		return "document", fmt.Sprintf("[Documento: %s]", doc.GetFileName()), ""
	}

	// Sticker
	if msg.GetStickerMessage() != nil {
		return "sticker", "[Sticker]", ""
	}

	// Localização
	if loc := msg.GetLocationMessage(); loc != nil {
		return "location", fmt.Sprintf("[Localização: %.6f, %.6f]", loc.GetDegreesLatitude(), loc.GetDegreesLongitude()), ""
	}

	// Contato
	if contact := msg.GetContactMessage(); contact != nil {
		return "contact", fmt.Sprintf("[Contato: %s]", contact.GetDisplayName()), ""
	}

	// Reação
	if reaction := msg.GetReactionMessage(); reaction != nil {
		return "reaction", fmt.Sprintf("[Reação: %s]", reaction.GetText()), ""
	}

	return "", "", ""
}

// mediaDownloader interface para polimorfismo na função downloadMedia
type mediaMessage interface {
	proto.Message
}

// downloadMedia baixa um arquivo de mídia e salva localmente
func downloadMedia(evt *events.Message, mediaMsg interface{}, mediaType string) string {
	var url, mimetype, fileExt string
	var mediaKey []byte

	switch m := mediaMsg.(type) {
	case *waE2E.AudioMessage:
		url = m.GetUrl()
		mediaKey = m.GetMediaKey()
		mimetype = m.GetMimetype()
		fileExt = ".ogg"
		if strings.Contains(mimetype, "mp4") {
			fileExt = ".mp4"
		}
	case *waE2E.ImageMessage:
		url = m.GetUrl()
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

	// Usa o cliente whatsmeow para baixar corretamente (descriptografa)
	var data []byte
	var err error

	switch m := mediaMsg.(type) {
	case *waE2E.AudioMessage:
		data, err = client.Download(m)
	case *waE2E.ImageMessage:
		data, err = client.Download(m)
	}

	if err != nil {
		// Fallback: tenta baixar direto via HTTP (sem descriptografia - pode falhar)
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

	// Salva o arquivo
	filename := fmt.Sprintf("%s_%s%s", evt.Info.Chat.String(), evt.Info.ID, fileExt)
	filename = strings.ReplaceAll(filename, "@", "_")
	filename = strings.ReplaceAll(filename, ".", "_") + fileExt
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

// updateChat atualiza as informações de um chat no banco
func updateChat(evt *events.Message, chatJID string, timestamp int64) {
	isGroup := evt.Info.Chat.Server == types.GroupServer

	var name string
	if isGroup {
		// Tenta obter nome do grupo via API
		groupInfo, err := client.GetGroupInfo(evt.Info.Chat)
		if err == nil && groupInfo != nil {
			name = groupInfo.Name
		}
	} else {
		// Para contatos individuais, usa o push name ou JID
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

// handleGroupInfo processa eventos de informações de grupo
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

// updateContact atualiza informações de um contato
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

// getSenderName retorna o nome de exibição do remetente
func getSenderName(evt *events.Message) string {
	if evt.Info.PushName != "" {
		return evt.Info.PushName
	}
	return evt.Info.Sender.User
}

// getEnv retorna o valor de uma variável de ambiente ou o padrão
func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

// boolToInt converte bool para int (para SQLite)
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// truncate limita uma string a n caracteres
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
