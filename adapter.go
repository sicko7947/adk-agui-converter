// Package aguigo provides an adapter for Google ADK (Agent Development Kit)
// to work with the AG-UI protocol using the official AG-UI Go SDK.
package aguigo

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/encoding/sse"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// Options configures the converter behavior
type Options struct {
	// IncludeRawEvents includes the original event in AG-UI events
	IncludeRawEvents bool
	// EmitStepEvents emits STEP_STARTED/STEP_FINISHED for sub-agent tracking
	EmitStepEvents bool
	// EmitActivityEvents emits ACTIVITY_DELTA for progress tracking
	EmitActivityEvents bool
}

// Option is a functional option for configuring the converter
type Option func(*Options)

// WithRawEvents enables including raw events
func WithRawEvents(include bool) Option {
	return func(o *Options) { o.IncludeRawEvents = include }
}

// WithStepEvents enables step event emission
func WithStepEvents(emit bool) Option {
	return func(o *Options) { o.EmitStepEvents = emit }
}

// WithActivityEvents enables activity event emission
func WithActivityEvents(emit bool) Option {
	return func(o *Options) { o.EmitActivityEvents = emit }
}

// ADKConverter converts ADK session.Event to AG-UI SDK events
type ADKConverter struct {
	mu sync.Mutex

	threadID         string
	runID            string
	currentMessageID string
	messageStarted   bool
	activeToolCalls  map[string]bool
	options          Options
}

// NewADKConverter creates a new ADK-specific converter
func NewADKConverter(threadID, runID string, opts ...Option) *ADKConverter {
	if threadID == "" {
		threadID = events.GenerateThreadID()
	}
	if runID == "" {
		runID = events.GenerateRunID()
	}

	options := Options{}
	for _, opt := range opts {
		opt(&options)
	}

	return &ADKConverter{
		threadID:        threadID,
		runID:           runID,
		activeToolCalls: make(map[string]bool),
		options:         options,
	}
}

// GetThreadID returns the current thread ID
func (c *ADKConverter) GetThreadID() string { return c.threadID }

// GetRunID returns the current run ID
func (c *ADKConverter) GetRunID() string { return c.runID }

// GetOptions returns the converter options
func (c *ADKConverter) GetOptions() Options { return c.options }

// StartRun generates the RUN_STARTED event
func (c *ADKConverter) StartRun() events.Event {
	return events.NewRunStartedEvent(c.threadID, c.runID)
}

// FinishRun generates the RUN_FINISHED event(s)
func (c *ADKConverter) FinishRun() []events.Event {
	c.mu.Lock()
	defer c.mu.Unlock()

	var result []events.Event

	// Close any open message
	if c.messageStarted {
		result = append(result, events.NewTextMessageEndEvent(c.currentMessageID))
		c.messageStarted = false
	}

	result = append(result, events.NewRunFinishedEvent(c.threadID, c.runID))
	return result
}

// ErrorRun generates the RUN_ERROR event(s)
func (c *ADKConverter) ErrorRun(err error) []events.Event {
	c.mu.Lock()
	defer c.mu.Unlock()

	var result []events.Event

	// Close any open message
	if c.messageStarted {
		result = append(result, events.NewTextMessageEndEvent(c.currentMessageID))
		c.messageStarted = false
	}

	result = append(result, events.NewRunErrorEvent(err.Error(), events.WithRunID(c.runID)))
	return result
}

// ConvertEvent converts an ADK session.Event to AG-UI SDK events
func (c *ADKConverter) ConvertEvent(adkEvent *session.Event) []events.Event {
	var result []events.Event

	if adkEvent.Content != nil && len(adkEvent.Content.Parts) > 0 {
		for _, part := range adkEvent.Content.Parts {
			// Handle thinking/reasoning (Thought flag on text parts)
			if part.Thought && part.Text != "" {
				result = append(result, c.handleThought(adkEvent, part.Text)...)
				continue
			}

			// Handle text content
			if part.Text != "" {
				result = append(result, c.handleTextPart(adkEvent, part.Text)...)
			}

			// Handle function calls (tool invocations)
			if part.FunctionCall != nil {
				result = append(result, c.handleFunctionCall(part.FunctionCall)...)
			}

			// Handle function responses (tool results)
			if part.FunctionResponse != nil {
				result = append(result, c.handleFunctionResponse(part.FunctionResponse)...)
			}
		}
	}

	// Handle state changes via actions
	result = append(result, c.handleActions(&adkEvent.Actions)...)

	return result
}

// handleThought processes thinking/reasoning content from ADK events
func (c *ADKConverter) handleThought(adkEvent *session.Event, thought string) []events.Event {
	var result []events.Event

	if !c.options.EmitStepEvents {
		// Emit as custom event if step events disabled
		result = append(result, events.NewCustomEvent(
			"thinking",
			events.WithValue(map[string]any{
				"content": thought,
				"author":  adkEvent.Author,
			}),
		))
		return result
	}

	// Create a step for the thinking process
	stepID := events.GenerateStepID()

	result = append(result, events.NewStepStartedEvent(stepID))
	result = append(result, events.NewStepFinishedEvent(stepID))

	return result
}

// handleTextPart processes text content from ADK events
func (c *ADKConverter) handleTextPart(adkEvent *session.Event, text string) []events.Event {
	c.mu.Lock()
	defer c.mu.Unlock()

	var result []events.Event

	// Start a new message if needed
	if !c.messageStarted {
		role := "assistant"
		if adkEvent.Author == "user" {
			role = "user"
		}
		c.currentMessageID = events.GenerateMessageID()
		c.messageStarted = true
		result = append(result, events.NewTextMessageStartEvent(c.currentMessageID, events.WithRole(role)))
	}

	// Add content chunk
	result = append(result, events.NewTextMessageContentEvent(c.currentMessageID, text))

	return result
}

// handleFunctionCall processes function call requests from ADK
func (c *ADKConverter) handleFunctionCall(fc *genai.FunctionCall) []events.Event {
	c.mu.Lock()
	defer c.mu.Unlock()

	var result []events.Event

	toolCallID := fc.ID
	if toolCallID == "" {
		toolCallID = events.GenerateToolCallID()
	}

	// Close any open text message before tool call
	if c.messageStarted {
		result = append(result, events.NewTextMessageEndEvent(c.currentMessageID))
		c.messageStarted = false
	}

	// Track this tool call
	c.activeToolCalls[toolCallID] = true

	// Start tool call
	result = append(result, events.NewToolCallStartEvent(toolCallID, fc.Name))

	// Emit TOOL_CALL_ARGS with the arguments
	if fc.Args != nil {
		argsJSON, err := json.Marshal(fc.Args)
		if err == nil && len(argsJSON) > 0 {
			result = append(result, events.NewToolCallArgsEvent(toolCallID, string(argsJSON)))
		}
	}

	// Emit TOOL_CALL_END
	result = append(result, events.NewToolCallEndEvent(toolCallID))

	return result
}

// handleFunctionResponse processes function/tool responses from ADK
func (c *ADKConverter) handleFunctionResponse(fr *genai.FunctionResponse) []events.Event {
	c.mu.Lock()
	defer c.mu.Unlock()

	toolCallID := fr.ID
	if toolCallID == "" {
		toolCallID = events.GenerateToolCallID()
	}

	// Serialize the response
	var content string
	if fr.Response != nil {
		responseJSON, err := json.Marshal(fr.Response)
		if err == nil {
			content = string(responseJSON)
		} else {
			content = fmt.Sprintf("%v", fr.Response)
		}
	}

	// Clean up tracked tool call
	delete(c.activeToolCalls, toolCallID)

	messageID := events.GenerateMessageID()
	return []events.Event{events.NewToolCallResultEvent(messageID, toolCallID, content)}
}

// handleActions processes ADK action signals (state changes, transfers, etc.)
func (c *ADKConverter) handleActions(actions *session.EventActions) []events.Event {
	var result []events.Event

	// Handle state delta - convert map to JSON Patch operations
	if len(actions.StateDelta) > 0 {
		var ops []events.JSONPatchOperation
		for key, value := range actions.StateDelta {
			ops = append(ops, events.JSONPatchOperation{
				Op:    "replace",
				Path:  "/" + key,
				Value: value,
			})
		}
		result = append(result, events.NewStateDeltaEvent(ops))
	}

	// Handle artifact delta as a custom event
	if len(actions.ArtifactDelta) > 0 {
		result = append(result, events.NewCustomEvent(
			"artifact_delta",
			events.WithValue(actions.ArtifactDelta),
		))
	}

	// Handle agent transfer as a custom event
	if actions.TransferToAgent != "" {
		result = append(result, events.NewCustomEvent(
			"agent_transfer",
			events.WithValue(map[string]string{
				"targetAgent": actions.TransferToAgent,
			}),
		))
	}

	// Handle escalation as a custom event
	if actions.Escalate {
		result = append(result, events.NewCustomEvent(
			"escalation",
			events.WithValue(map[string]any{
				"escalate": true,
			}),
		))
	}

	return result
}

// IsMessageStarted returns whether a message is currently being streamed
func (c *ADKConverter) IsMessageStarted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.messageStarted
}

// ADKHandler handles AG-UI protocol requests for ADK agents
type ADKHandler struct {
	runner         *runner.Runner
	sessionService session.Service
	appName        string
	converterOpts  []Option
}

// NewADKHandler creates a new AG-UI handler for an ADK agent.
func NewADKHandler(ag agent.Agent, sessionService session.Service, appName string, opts ...Option) (*ADKHandler, error) {
	if appName == "" {
		appName = "adk-agent"
	}

	r, err := runner.New(runner.Config{
		AppName:        appName,
		Agent:          ag,
		SessionService: sessionService,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create runner: %w", err)
	}

	return &ADKHandler{
		runner:         r,
		sessionService: sessionService,
		appName:        appName,
		converterOpts:  opts,
	}, nil
}

// ensureSession creates a session if it doesn't exist
func (h *ADKHandler) ensureSession(ctx context.Context, userID, sessionID string) error {
	_, err := h.sessionService.Get(ctx, &session.GetRequest{
		AppName:   h.appName,
		UserID:    userID,
		SessionID: sessionID,
	})
	if err == nil {
		return nil
	}

	_, err = h.sessionService.Create(ctx, &session.CreateRequest{
		AppName:   h.appName,
		UserID:    userID,
		SessionID: sessionID,
	})
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}

	return nil
}

// ServeHTTP handles AG-UI protocol requests
func (h *ADKHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Printf("[AG-UI] Received %s request from %s", r.Method, r.RemoteAddr)

	if r.Method == http.MethodOptions {
		h.handleCORS(w)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var input RunAgentInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// Ensure IDs exist
	if input.ThreadID == "" {
		input.ThreadID = events.GenerateThreadID()
	}
	if input.RunID == "" {
		input.RunID = events.GenerateRunID()
	}

	// Determine encoding based on Accept header
	accept := r.Header.Get("Accept")
	if accept == "" || accept == "text/event-stream" || accept == "*/*" {
		h.handleSSE(w, r.Context(), input)
	} else {
		h.handleJSON(w, r.Context(), input)
	}
}

func (h *ADKHandler) handleCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Authorization")
	w.WriteHeader(http.StatusOK)
}

// handleSSE handles Server-Sent Events streaming
func (h *ADKHandler) handleSSE(w http.ResponseWriter, ctx context.Context, input RunAgentInput) {
	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("X-Accel-Buffering", "no")

	conv := NewADKConverter(input.ThreadID, input.RunID, h.converterOpts...)
	writer := sse.NewSSEWriter()

	// Send RUN_STARTED
	if err := writer.WriteEvent(ctx, w, conv.StartRun()); err != nil {
		return
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	// Convert AG-UI messages to ADK content
	adkContent := convertMessagesToADKContent(input.Messages)

	userID := "default-user"
	sessionID := input.ThreadID

	if err := h.ensureSession(ctx, userID, sessionID); err != nil {
		writer.WriteErrorEvent(ctx, w, err, input.RunID)
		return
	}

	errorOccurred := false

	for adkEvent, err := range h.runner.Run(ctx, userID, sessionID, adkContent, agent.RunConfig{}) {
		if err != nil {
			writer.WriteErrorEvent(ctx, w, err, input.RunID)
			errorOccurred = true
			break
		}

		aguiEvents := conv.ConvertEvent(adkEvent)
		for _, evt := range aguiEvents {
			if err := writer.WriteEvent(ctx, w, evt); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}

	if !errorOccurred {
		for _, evt := range conv.FinishRun() {
			if err := writer.WriteEvent(ctx, w, evt); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}
}

// handleJSON handles non-streaming JSON responses
func (h *ADKHandler) handleJSON(w http.ResponseWriter, ctx context.Context, input RunAgentInput) {
	conv := NewADKConverter(input.ThreadID, input.RunID, h.converterOpts...)
	var allEvents []events.Event

	allEvents = append(allEvents, conv.StartRun())

	adkContent := convertMessagesToADKContent(input.Messages)

	userID := "default-user"
	sessionID := input.ThreadID

	if err := h.ensureSession(ctx, userID, sessionID); err != nil {
		allEvents = append(allEvents, events.NewRunErrorEvent(err.Error(), events.WithRunID(input.RunID)))
		h.writeJSONEvents(w, allEvents)
		return
	}

	errorOccurred := false

	for adkEvent, err := range h.runner.Run(ctx, userID, sessionID, adkContent, agent.RunConfig{}) {
		if err != nil {
			allEvents = append(allEvents, events.NewRunErrorEvent(err.Error(), events.WithRunID(input.RunID)))
			errorOccurred = true
			break
		}

		allEvents = append(allEvents, conv.ConvertEvent(adkEvent)...)
	}

	if !errorOccurred {
		allEvents = append(allEvents, conv.FinishRun()...)
	}

	h.writeJSONEvents(w, allEvents)
}

func (h *ADKHandler) writeJSONEvents(w http.ResponseWriter, evts []events.Event) {
	var jsonEvents []json.RawMessage
	for _, evt := range evts {
		data, err := evt.ToJSON()
		if err != nil {
			continue
		}
		jsonEvents = append(jsonEvents, data)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(jsonEvents)
}

// convertMessagesToADKContent converts AG-UI messages to ADK content format
func convertMessagesToADKContent(messages []Message) *genai.Content {
	if len(messages) == 0 {
		return nil
	}

	var lastUserMessage *Message
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			lastUserMessage = &messages[i]
			break
		}
	}

	if lastUserMessage == nil {
		return nil
	}

	var parts []*genai.Part
	for _, content := range lastUserMessage.Content {
		if content.Type == "text" && content.Text != "" {
			parts = append(parts, &genai.Part{Text: content.Text})
		}
	}

	if len(parts) == 0 {
		return nil
	}

	return &genai.Content{
		Role:  genai.RoleUser,
		Parts: parts,
	}
}
