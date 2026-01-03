package aguigo

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	"github.com/stretchr/testify/assert"
)

// MockEventSource for testing the handler
type MockEventSource struct {
	RunFunc func(ctx HandlerContext, input RunAgentInput) <-chan events.Event
}

func (m *MockEventSource) Run(ctx HandlerContext, input RunAgentInput) <-chan events.Event {
	if m.RunFunc != nil {
		return m.RunFunc(ctx, input)
	}
	ch := make(chan events.Event)
	close(ch)
	return ch
}

func TestNewHandler(t *testing.T) {
	handler := New(Config{
		EventSource: &MockEventSource{},
	})
	assert.NotNil(t, handler)
}

func TestHandler_ServeHTTP(t *testing.T) {
	mockSource := &MockEventSource{}
	handler := New(Config{EventSource: mockSource})

	t.Run("CORS preflight", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodOptions, "/", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "*", rr.Header().Get("Access-Control-Allow-Origin"))
	})

	t.Run("Method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	})

	t.Run("Invalid JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("invalid"))
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("SSE streaming", func(t *testing.T) {
		mockSource.RunFunc = func(ctx HandlerContext, input RunAgentInput) <-chan events.Event {
			ch := make(chan events.Event, 2)
			ch <- events.NewRunStartedEvent("thread-1", "run-1")
			ch <- events.NewRunFinishedEvent("thread-1", "run-1")
			close(ch)
			return ch
		}

		input := RunAgentInput{ThreadID: "thread-1"}
		body, _ := json.Marshal(input)
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		req.Header.Set("Accept", "text/event-stream")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "text/event-stream", rr.Header().Get("Content-Type"))
		assert.Contains(t, rr.Body.String(), "data:")
		assert.Equal(t, 2, strings.Count(rr.Body.String(), "data:"))
	})

	t.Run("JSON response", func(t *testing.T) {
		mockSource.RunFunc = func(ctx HandlerContext, input RunAgentInput) <-chan events.Event {
			ch := make(chan events.Event, 1)
			ch <- events.NewRunStartedEvent("thread-json", "run-json")
			close(ch)
			return ch
		}

		input := RunAgentInput{}
		body, _ := json.Marshal(input)
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		req.Header.Set("Accept", "application/json")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))
	})

	t.Run("Generate IDs", func(t *testing.T) {
		var capturedCtx HandlerContext
		mockSource.RunFunc = func(ctx HandlerContext, input RunAgentInput) <-chan events.Event {
			capturedCtx = ctx
			ch := make(chan events.Event)
			close(ch)
			return ch
		}

		input := RunAgentInput{Messages: []Message{}}
		body, _ := json.Marshal(input)
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		assert.NotEmpty(t, capturedCtx.ThreadID)
		assert.NotEmpty(t, capturedCtx.RunID)
	})
}

func TestHealthHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	http.HandlerFunc(HealthHandler).ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), `"status":"healthy"`)
}

func TestCORSMiddleware(t *testing.T) {
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	middleware := CORSMiddleware(testHandler)

	t.Run("Normal request", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rr := httptest.NewRecorder()
		middleware.ServeHTTP(rr, req)
		assert.Equal(t, "*", rr.Header().Get("Access-Control-Allow-Origin"))
	})

	t.Run("OPTIONS request", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodOptions, "/", nil)
		rr := httptest.NewRecorder()
		middleware.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "*", rr.Header().Get("Access-Control-Allow-Origin"))
	})
}
