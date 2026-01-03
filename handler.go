// Package aguigo provides HTTP handlers for the AG-UI protocol.
package aguigo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/encoding/sse"
)

// RunAgentInput represents the AG-UI protocol input format
type RunAgentInput struct {
	ThreadID string    `json:"threadId"`
	RunID    string    `json:"runId,omitempty"`
	Messages []Message `json:"messages"`
	Tools    []Tool    `json:"tools,omitempty"`
	Context  any       `json:"context,omitempty"`
	State    any       `json:"state,omitempty"`
}

// Tool represents a tool definition in AG-UI format
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters,omitempty"`
}

// Message represents a chat message in the history
type Message struct {
	ID        string        `json:"id"`
	Role      string        `json:"role"`
	Content   []ContentPart `json:"content"`
	Name      string        `json:"name,omitempty"`
	CreatedAt int64         `json:"createdAt,omitempty"`
}

// ContentPart represents a part of message content
type ContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"`
	URL      string `json:"url,omitempty"`
}

// UnmarshalJSON implements custom JSON unmarshaling for Message
// to support both string content (simple) and array content (rich)
func (m *Message) UnmarshalJSON(data []byte) error {
	// Use an alias to avoid infinite recursion
	type MessageAlias Message
	type messageRaw struct {
		MessageAlias
		Content json.RawMessage `json:"content"`
	}

	var raw messageRaw
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	// Copy the non-content fields
	*m = Message(raw.MessageAlias)

	// Handle empty content
	if len(raw.Content) == 0 || string(raw.Content) == "null" {
		m.Content = nil
		return nil
	}

	// Try to unmarshal as string first
	var contentStr string
	if err := json.Unmarshal(raw.Content, &contentStr); err == nil {
		// Content is a string, convert to ContentPart array
		m.Content = []ContentPart{{
			Type: "text",
			Text: contentStr,
		}}
		return nil
	}

	// Try to unmarshal as array of ContentPart
	var contentParts []ContentPart
	if err := json.Unmarshal(raw.Content, &contentParts); err != nil {
		return fmt.Errorf("content must be either a string or an array of ContentPart: %w", err)
	}

	m.Content = contentParts
	return nil
}

// EventSource is the interface that agent implementations must satisfy
type EventSource interface {
	Run(ctx HandlerContext, input RunAgentInput) <-chan events.Event
}

// HandlerContext provides context for the agent run
type HandlerContext struct {
	ThreadID string
	RunID    string
	UserID   string
	Request  *http.Request
}

// Config configures the handler
type Config struct {
	EventSource EventSource
	AppName     string
	Logger      Logger
}

// Logger interface for logging
type Logger interface {
	Printf(format string, v ...any)
}

type defaultLogger struct{}

func (defaultLogger) Printf(format string, v ...any) {}

// Handler handles AG-UI protocol requests
type Handler struct {
	eventSource EventSource
	appName     string
	logger      Logger
}

// New creates a new AG-UI handler
func New(config Config) *Handler {
	logger := config.Logger
	if logger == nil {
		logger = defaultLogger{}
	}

	return &Handler{
		eventSource: config.EventSource,
		appName:     config.AppName,
		logger:      logger,
	}
}

// ServeHTTP handles AG-UI protocol requests
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.logger.Printf("[AG-UI] Received %s request from %s", r.Method, r.RemoteAddr)

	if r.Method == http.MethodOptions {
		h.handleCORS(w)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var input RunAgentInput
	if err := json.Unmarshal(body, &input); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if input.ThreadID == "" {
		input.ThreadID = events.GenerateThreadID()
	}
	if input.RunID == "" {
		input.RunID = events.GenerateRunID()
	}

	ctx := HandlerContext{
		ThreadID: input.ThreadID,
		RunID:    input.RunID,
		UserID:   r.Header.Get("X-User-ID"),
		Request:  r,
	}

	accept := r.Header.Get("Accept")
	if accept == "" || accept == "text/event-stream" || accept == "*/*" {
		h.handleSSE(w, r.Context(), ctx, input)
	} else {
		h.handleJSON(w, r.Context(), ctx, input)
	}
}

func (h *Handler) handleCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Authorization, X-User-ID")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) handleSSE(w http.ResponseWriter, ctx context.Context, hctx HandlerContext, input RunAgentInput) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("X-Accel-Buffering", "no")

	writer := sse.NewSSEWriter()
	eventsChan := h.eventSource.Run(hctx, input)

	for evt := range eventsChan {
		if err := writer.WriteEvent(ctx, w, evt); err != nil {
			h.logger.Printf("[AG-UI] Failed to send event: %v", err)
			return
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

func (h *Handler) handleJSON(w http.ResponseWriter, ctx context.Context, hctx HandlerContext, input RunAgentInput) {
	var allEvents []events.Event

	eventsChan := h.eventSource.Run(hctx, input)
	for evt := range eventsChan {
		allEvents = append(allEvents, evt)
	}

	var jsonEvents []json.RawMessage
	for _, evt := range allEvents {
		data, err := evt.ToJSON()
		if err != nil {
			continue
		}
		jsonEvents = append(jsonEvents, data)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(jsonEvents)
}

// HealthHandler returns a simple health check endpoint
func HealthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status":   "healthy",
		"protocol": "ag-ui",
		"version":  "1.0.0",
	})
}

// CORSMiddleware adds CORS headers to responses
func CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Authorization, X-User-ID")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// StdLogger wraps the standard log package
type StdLogger struct{}

func (StdLogger) Printf(format string, v ...any) {
	log.Printf(format, v...)
}