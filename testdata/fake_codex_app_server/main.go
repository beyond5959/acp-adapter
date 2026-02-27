package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"codex-acp/internal/appserver"
)

type turnControl struct {
	cancel chan struct{}
	once   sync.Once
}

type fakeServer struct {
	codec *appserver.JSONLCodec

	mu         sync.Mutex
	nextThread int
	nextTurn   int
	turns      map[string]*turnControl

	receivedInitialize  bool
	receivedInitialized bool
	crashOnThreadStart  bool
}

func main() {
	server := &fakeServer{
		codec:              appserver.NewJSONLCodec(os.Stdin, os.Stdout),
		turns:              make(map[string]*turnControl),
		crashOnThreadStart: os.Getenv("FAKE_APP_SERVER_CRASH_ON_THREAD_START") == "1",
	}

	if err := server.serve(); err != nil && err != io.EOF {
		_, _ = fmt.Fprintf(os.Stderr, "fake app-server error: %v\n", err)
		os.Exit(1)
	}
}

func (s *fakeServer) serve() error {
	for {
		msg, err := s.codec.ReadMessage()
		if err != nil {
			return err
		}
		if msg.Method == "" {
			continue
		}
		s.handle(msg)
	}
}

func (s *fakeServer) handle(msg appserver.RPCMessage) {
	switch msg.Method {
	case "initialize":
		s.receivedInitialize = true
		s.writeResult(msg.ID, map[string]any{
			"serverInfo": map[string]any{
				"name":    "fake-codex-app-server",
				"version": "0.1.0",
			},
			"capabilities": map[string]any{},
		})
	case "initialized":
		if !s.receivedInitialize {
			s.writeError(msg.ID, -32000, "initialize must be called before initialized")
			return
		}
		s.receivedInitialized = true
	case "thread/start":
		if !s.receivedInitialize || !s.receivedInitialized {
			s.writeError(msg.ID, -32000, "initialize/initialized handshake required")
			return
		}
		if s.crashOnThreadStart {
			os.Exit(42)
		}
		threadID := s.newThreadID()
		s.writeResult(msg.ID, appserver.ThreadStartResult{
			ThreadID: threadID,
		})
	case "turn/start":
		var params appserver.TurnStartParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			s.writeError(msg.ID, -32602, "invalid params")
			return
		}
		if params.ThreadID == "" {
			s.writeError(msg.ID, -32602, "threadId required")
			return
		}

		turnID, control := s.newTurn()
		s.writeResult(msg.ID, appserver.TurnStartResult{
			TurnID: turnID,
		})

		go s.runTurn(params.ThreadID, turnID, params.Input, control)
	case "turn/interrupt":
		var params appserver.TurnInterruptParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			s.writeError(msg.ID, -32602, "invalid params")
			return
		}
		s.cancelTurn(params.TurnID)
		s.writeResult(msg.ID, map[string]any{"interrupted": true})
	default:
		s.writeError(msg.ID, -32601, "method not found")
	}
}

func (s *fakeServer) newThreadID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextThread++
	return fmt.Sprintf("thread-%d", s.nextThread)
}

func (s *fakeServer) newTurn() (string, *turnControl) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextTurn++
	turnID := fmt.Sprintf("turn-%d", s.nextTurn)
	control := &turnControl{cancel: make(chan struct{})}
	s.turns[turnID] = control
	return turnID, control
}

func (s *fakeServer) cancelTurn(turnID string) {
	s.mu.Lock()
	control, ok := s.turns[turnID]
	s.mu.Unlock()
	if !ok {
		return
	}
	control.once.Do(func() { close(control.cancel) })
}

func (s *fakeServer) removeTurn(turnID string) {
	s.mu.Lock()
	delete(s.turns, turnID)
	s.mu.Unlock()
}

func (s *fakeServer) runTurn(threadID, turnID, input string, control *turnControl) {
	defer s.removeTurn(turnID)

	select {
	case <-control.cancel:
		s.writeTurnCompleted(threadID, turnID, "cancelled")
		return
	case <-time.After(40 * time.Millisecond):
		s.writeTurnUpdate(threadID, turnID, "working")
	}

	duration := 120 * time.Millisecond
	if strings.Contains(strings.ToLower(input), "slow") {
		duration = 2 * time.Second
	}

	select {
	case <-control.cancel:
		s.writeTurnCompleted(threadID, turnID, "cancelled")
	case <-time.After(duration):
		s.writeTurnUpdate(threadID, turnID, "done")
		s.writeTurnCompleted(threadID, turnID, "end_turn")
	}
}

func (s *fakeServer) writeTurnUpdate(threadID, turnID, delta string) {
	s.writeNotification("turn/update", appserver.TurnUpdateNotification{
		ThreadID: threadID,
		TurnID:   turnID,
		Delta:    delta,
	})
}

func (s *fakeServer) writeTurnCompleted(threadID, turnID, stopReason string) {
	s.writeNotification("turn/completed", appserver.TurnCompletedNotification{
		ThreadID:   threadID,
		TurnID:     turnID,
		StopReason: stopReason,
	})
}

func (s *fakeServer) writeResult(id *json.RawMessage, result any) {
	var resultRaw json.RawMessage
	if result != nil {
		raw, err := json.Marshal(result)
		if err == nil {
			resultRaw = raw
		}
	}
	_ = s.codec.WriteMessage(appserver.RPCMessage{
		JSONRPC: "2.0",
		ID:      cloneID(id),
		Result:  resultRaw,
	})
}

func (s *fakeServer) writeError(id *json.RawMessage, code int, message string) {
	_ = s.codec.WriteMessage(appserver.RPCMessage{
		JSONRPC: "2.0",
		ID:      cloneID(id),
		Error: &appserver.RPCError{
			Code:    code,
			Message: message,
		},
	})
}

func (s *fakeServer) writeNotification(method string, params any) {
	var paramsRaw json.RawMessage
	if params != nil {
		raw, err := json.Marshal(params)
		if err == nil {
			paramsRaw = raw
		}
	}
	_ = s.codec.WriteMessage(appserver.RPCMessage{
		JSONRPC: "2.0",
		Method:  method,
		Params:  paramsRaw,
	})
}

func cloneID(id *json.RawMessage) *json.RawMessage {
	if id == nil {
		return nil
	}
	cp := append(json.RawMessage(nil), (*id)...)
	return &cp
}
