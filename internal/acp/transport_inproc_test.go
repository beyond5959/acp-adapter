package acp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

func TestInProcTransportBidirectionalRoundTrip(t *testing.T) {
	t.Parallel()

	a, b := NewInProcTransportPair(16)
	defer func() {
		_ = a.Close()
		_ = b.Close()
	}()

	if err := a.WriteNotification("ping", map[string]any{"x": 1}); err != nil {
		t.Fatalf("a write notification: %v", err)
	}
	msg := mustReadInProc(t, b, 2*time.Second)
	if msg.Method != "ping" {
		t.Fatalf("read method=%q, want ping", msg.Method)
	}

	resultID := json.RawMessage(`"res-1"`)
	if err := b.WriteResult(resultID, map[string]any{"ok": true}); err != nil {
		t.Fatalf("b write result: %v", err)
	}
	msg = mustReadInProc(t, a, 2*time.Second)
	if got := messageIDString(msg.ID); got != "res-1" {
		t.Fatalf("result id=%q, want res-1", got)
	}
	if len(msg.Result) == 0 {
		t.Fatalf("result payload must not be empty")
	}

	errID := json.RawMessage(`"err-1"`)
	if err := a.WriteError(errID, -32000, "boom", map[string]any{"k": "v"}); err != nil {
		t.Fatalf("a write error: %v", err)
	}
	msg = mustReadInProc(t, b, 2*time.Second)
	if msg.Error == nil || msg.Error.Message != "boom" {
		t.Fatalf("unexpected error payload: %+v", msg.Error)
	}
}

func TestInProcTransportConcurrentWrites(t *testing.T) {
	t.Parallel()

	a, b := NewInProcTransportPair(64)
	defer func() {
		_ = a.Close()
		_ = b.Close()
	}()

	const (
		writers   = 8
		perWriter = 25
		total     = writers * perWriter
	)

	readDone := make(chan error, 1)
	go func() {
		for i := 0; i < total; i++ {
			msg, err := b.ReadMessage()
			if err != nil {
				readDone <- fmt.Errorf("read[%d]: %w", i, err)
				return
			}
			if msg.Method != "concurrent" {
				readDone <- fmt.Errorf("read[%d]: method=%q", i, msg.Method)
				return
			}
		}
		readDone <- nil
	}()

	var wg sync.WaitGroup
	errCh := make(chan error, total)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(writer int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				if err := a.WriteNotification("concurrent", map[string]any{
					"writer": writer,
					"i":      i,
				}); err != nil {
					errCh <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent write error: %v", err)
		}
	}

	select {
	case err := <-readDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for concurrent reads")
	}
}

func TestInProcTransportCloseSemantics(t *testing.T) {
	t.Parallel()

	a, b := NewInProcTransportPair(4)
	if err := a.Close(); err != nil {
		t.Fatalf("close a: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("close a second time: %v", err)
	}

	if err := a.WriteNotification("after-close", nil); !errors.Is(err, io.EOF) {
		t.Fatalf("a write after close err=%v, want io.EOF", err)
	}

	_, err := readInProcWithTimeout(b, time.Second)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("b read after peer close err=%v, want io.EOF", err)
	}

	if err := b.WriteNotification("to-closed-peer", nil); !errors.Is(err, io.EOF) {
		t.Fatalf("b write to closed peer err=%v, want io.EOF", err)
	}

	if err := b.Close(); err != nil {
		t.Fatalf("close b: %v", err)
	}
	_, err = readInProcWithTimeout(a, time.Second)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("a read after local close err=%v, want io.EOF", err)
	}
}

func mustReadInProc(t *testing.T, transport *InProcTransport, timeout time.Duration) RPCMessage {
	t.Helper()
	msg, err := readInProcWithTimeout(transport, timeout)
	if err != nil {
		t.Fatalf("read message: %v", err)
	}
	return msg
}

func readInProcWithTimeout(transport *InProcTransport, timeout time.Duration) (RPCMessage, error) {
	type result struct {
		msg RPCMessage
		err error
	}
	done := make(chan result, 1)
	go func() {
		msg, err := transport.ReadMessage()
		done <- result{msg: msg, err: err}
	}()

	select {
	case r := <-done:
		return r.msg, r.err
	case <-time.After(timeout):
		return RPCMessage{}, fmt.Errorf("read timeout")
	}
}

func messageIDString(raw *json.RawMessage) string {
	if raw == nil {
		return ""
	}
	var s string
	if err := json.Unmarshal(*raw, &s); err == nil {
		return s
	}

	var number float64
	if err := json.Unmarshal(*raw, &number); err == nil {
		return fmt.Sprintf("%.0f", number)
	}
	return string(*raw)
}
