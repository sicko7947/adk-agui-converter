# agui-go

A Go package for implementing the AG-UI (Agent-User Interaction) protocol for Google's ADK-go, enabling seamless integration between AI agent backends and frontend applications.

## Overview

AG-UI is a lightweight, event-driven protocol designed for streaming communication between AI agent backends and frontend applications. This package provides:

- **Framework-agnostic core**: Event types, encoders, and converters that work with any agent framework
- **ADK adapter**: Ready-to-use integration with Google's Agent Development Kit (ADK)
- **SSE streaming**: Real-time Server-Sent Events support
- **Full protocol compliance**: All AG-UI event types supported
- **Reasoning/Thinking support**: Display agent reasoning process via step events or custom events

## Installation

```bash
go get github.com/sicko7947/agui-go
```

## Quick Start

### With Google ADK

```go
package main

import (
    "log"
    "net/http"

    aguigo "github.com/sicko7947/agui-go"
    "google.golang.org/adk/session"
)

func main() {
    // Create your ADK agent
    myAgent := createYourAgent()

    // Create AG-UI handler for ADK
    handler, err := aguigo.NewADKHandler(
        myAgent,
        session.InMemoryService(),
        "my-app",
    )
    if err != nil {
        log.Fatal(err)
    }

    // Mount the handler
    http.Handle("/api/ag-ui", handler)
    
    log.Println("AG-UI server running on :8081")
    http.ListenAndServe(":8081", nil)
}
```

### With Reasoning/Thinking Display

To enable reasoning/thinking display from your ADK agent, use the converter options:

```go
package main

import (
    "log"
    "net/http"

    aguigo "github.com/sicko7947/agui-go"
    "google.golang.org/adk/session"
)

func main() {
    myAgent := createYourAgent()

    // Create ADK converter with step events enabled for reasoning
    conv := aguigo.NewADKConverter("thread-id", "run-id",
        aguigo.WithStepEvents(true),      // Emit STEP_STARTED/STEP_FINISHED for thinking
        aguigo.WithActivityEvents(true),  // Emit ACTIVITY_DELTA for progress tracking
    )

    // The converter will automatically convert ADK's Thought parts to:
    // - STEP_STARTED/STEP_FINISHED events (when WithStepEvents is true)
    // - CUSTOM "thinking" events (when WithStepEvents is false)
    
    handler, err := aguigo.NewADKHandler(myAgent, session.InMemoryService(), "my-app")
    if err != nil {
        log.Fatal(err)
    }

    http.Handle("/api/ag-ui", handler)
    http.ListenAndServe(":8081", nil)
}
```

### Framework-Agnostic Usage

```go
package main

import (
    "net/http"

    aguigo "github.com/sicko7947/agui-go"
)

// Implement the EventSource interface
type MyEventSource struct{}

func (s *MyEventSource) Run(ctx aguigo.Context, input aguigo.RunAgentInput) <-chan aguigo.Event {
    events := make(chan aguigo.Event)
    
    go func() {
        defer close(events)
        
        conv := aguigo.NewBaseConverter(ctx.ThreadID, ctx.RunID)
        
        // Send run started
        events <- conv.StartRun()
        
        // Optionally show reasoning/thinking step
        stepID := "thinking-step-1"
        events <- conv.CreateStepEvent("reasoning", stepID, true)  // Step started
        events <- conv.CreateActivityEvent(aguigo.Activity{
            ID:          stepID,
            Type:        "thinking",
            Status:      "running",
            Description: "Analyzing the user's request...",
        })
        events <- conv.CreateStepEvent("reasoning", stepID, false) // Step finished
        
        // Send a message
        for _, evt := range conv.StartMessage("assistant") {
            events <- evt
        }
        events <- conv.AddMessageContent("Hello from my agent!")
        events <- conv.EndMessage()
        
        // Send run finished
        for _, evt := range conv.FinishRun() {
          events <- evt
        }
    }()
    
    return events
}

func main() {
    h := aguigo.New(aguigo.Config{
        EventSource: &MyEventSource{},
        AppName:     "my-app",
    })
    
    http.Handle("/api/ag-ui", h)
    http.ListenAndServe(":8081", nil)
}
```

## Package Structure

This is a single-package design where all types are in the `aguigo` package:

```
github.com/sicko7947/agui-go/
├── types.go      # AG-UI event type definitions (Event, Base, RunStarted, etc.)
├── encoder.go    # SSE and JSON encoding utilities
├── converter.go  # BaseConverter for event conversion
├── handler.go    # Generic HTTP handler for AG-UI protocol
├── adapter.go    # Google ADK-specific adapter (ADKConverter, ADKHandler)
└── doc.go        # Package documentation
```

## Event Types

### Lifecycle Events
- `RUN_STARTED` - Agent run initiated
- `RUN_FINISHED` - Agent run completed
- `RUN_ERROR` - Error during execution

### Text Message Events
- `TEXT_MESSAGE_START` - New message begins
- `TEXT_MESSAGE_CONTENT` - Message content chunk
- `TEXT_MESSAGE_END` - Message complete

### Tool Call Events
- `TOOL_CALL_START` - Tool invocation begins
- `TOOL_CALL_ARGS` - Tool arguments chunk
- `TOOL_CALL_END` - Tool call complete
- `TOOL_CALL_RESULT` - Tool execution result

### State Events
- `STATE_SNAPSHOT` - Full state snapshot
- `STATE_DELTA` - Incremental state update
- `MESSAGES_SNAPSHOT` - Message history

### Activity Events
- `ACTIVITY_SNAPSHOT` - All activities
- `ACTIVITY_DELTA` - Activity update

### Step Events (for Reasoning/Thinking)
- `STEP_STARTED` - Processing step begins (can be used for thinking/reasoning)
- `STEP_FINISHED` - Processing step ends

### Special Events
- `RAW` - Untyped data passthrough
- `CUSTOM` - Vendor-specific events (e.g., "thinking" for reasoning display)

## Reasoning/Thinking Support

The package supports displaying agent reasoning in two ways:

### 1. Step Events (Recommended)

When `WithStepEvents(true)` is enabled, thinking content from ADK agents is converted to `STEP_STARTED`/`STEP_FINISHED` events:

```json
{"type": "STEP_STARTED", "stepName": "agent_thinking", "stepId": "uuid"}
{"type": "STEP_FINISHED", "stepId": "uuid"}
```

### 2. Custom Events

When step  are disabled, thinking content is emitted as custom events:

```json
{"type": "CUSTOM", "name": "thinking", "data": {"content": "...", "author": "agent"}}
```

### Frontend Handling

```typescript
function handleEvent(event: AgUiEvent) {
  switch (event.type) {
    case 'STEP_STARTED':
      if (event.stepName.includes('thinking')) {
        showThinkingIndicator(event.stepId);
      }
      break;
    case 'STEP_FINISHED':
      hideThinkingIndicator(event.stepId);
      break;
    case 'CUSTOM':
      if .name === 'thinking') {
        displayThinkingContent(event.data.content);
      }
      break;
    // ... handle other events
  }
}
```

## Frontend Integration

### React with @assistant-ui/react-ag-ui

```tsx
import { useAgUiRuntime } from "@assistant-ui/react-ag-ui";
import { HttpAgent } from "@ag-ui/client";
import { AssistantRuntimeProvider, Thread } from "@assistant-ui/react";

function App() {
  const agent = new HttpAgent({
    url: "http://localhost:8081/api/ag-ui",
    headers: { Accept: "text/event-stream" },
  });

  const runtime = use({ agent });

  return (
    <AssistantRuntimeProvider runtime={runtime}>
      <Thread />
    </AssistantRuntimeProvider>
  );
}
```

### Manual SSE Connection

```typescript
async function streamChat(messages: Message[]) {
  const response = await fetch("http://localhost:8081/api/ag-ui", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "Accept": "text/event-stream",
    },
    body: JSON.stringify({
      threadId: "thread-123",
      messages: messages,
    }),
  });

  co response.body?.getReader();
  const decoder = new TextDecoder();

  while (true) {
    const { done, value } = await reader!.read();
    if (done) break;

    const chunk = decoder.decode(value);
    const lines = chunk.split("\n");

    for (const line of lines) {
      if (line.startsWith("data: ")) {
        const event = JSON.parse(line.slice(6));
        handleEvent(event);
      }
    }
  }
}
```

## API Reference

### Converter Options

```go
conv := aguigo.NewBaseConverter(threadID, runID,
    aguigo.WithRawEvents(true),      // Include original events
    aguigo.WithStepEvents(true),     // Emit STEP_* events for thinking/reasoning
    aguigo.WithActivityEvents(true), // Emit ACTIVITY_DELTA events
)
```

### ADK Handler

```go
handler, err := aguigo.NewADKHandler(agent, sessionService, appName)
```

### ADK Converter

```go
conv := aguigo.NewADKConverter(threadID, runID,
    aguigo.WithStepEvents(true),
    aguigo.WithActivityEvents(true),
)

// Convert ADK events to AG-UI events
aguiEvents := conv.ConvertEvent(adkEvent)
```

### Generic Handler

```go
h := aguigo.New(aguigo.Config{
    EventSource: myEventSource,
    AppName:     "my-app",
    Logger:      aguigo.StdLogger{}, // Optional
})
```

### Key Types

```go
// Event interface - all AG-UI events implement this
type Event interface {
    GetType() EventType
    GetTimestamp() int64
    ToJSON() ([]byte, error)
}

// EventSource interface - implement this for custom agents
type EventSource interface {
    Run(ctx Context, input RunAgentInput) <-chan Event
}

// Activity for progress tracking
type Activity struct {
    ID          string `json:"id"`
    Type        string `json:"type"`
    Status      string `json:"status"` // "pending", "running", "completed", "failed"
    Description string `json:"description,omitempty"`
}
```

## License

MIT
