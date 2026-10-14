package thehomeplugin

import (
	"bufio"
	"encoding/json"
	"io"
	"testing"
	"time"
)

// fakeHost plays TheHome on the other end of two pipes.
type fakeHost struct {
	t    *testing.T
	in   *io.PipeWriter // to the plugin's stdin
	out  *bufio.Scanner // from the plugin's stdout
	done chan error
}

func start(t *testing.T, i Integration) *fakeHost {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	f := &fakeHost{t: t, in: inW, out: bufio.NewScanner(outR), done: make(chan error, 1)}
	go func() {
		f.done <- Serve(inR, outW, i)
		outW.Close()
	}()
	t.Cleanup(func() { inW.Close() })
	return f
}

func (f *fakeHost) send(v any) {
	f.t.Helper()
	b, _ := json.Marshal(v)
	if _, err := f.in.Write(append(b, '\n')); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeHost) read() map[string]any {
	f.t.Helper()
	ch := make(chan map[string]any, 1)
	go func() {
		if !f.out.Scan() {
			ch <- nil
			return
		}
		var m map[string]any
		_ = json.Unmarshal(f.out.Bytes(), &m)
		ch <- m
	}()
	select {
	case m := <-ch:
		if m == nil {
			f.t.Fatal("the plugin closed its output")
		}
		return m
	case <-time.After(5 * time.Second):
		f.t.Fatal("no message from the plugin")
	}
	return nil
}

type sample struct {
	Base
	device string
}

func (s *sample) SetupEntry(h *Host, e Entry) error {
	id, err := h.RegisterDevice(e.EntryID, Device{Name: e.Title})
	if err != nil {
		return err
	}
	s.device = id
	if e.Title == "locked" {
		return AuthFailed("forkert adgangskode")
	}
	return nil
}

func (s *sample) ConfigFlow(_ *Host, _ Flow, step string, input map[string]any) (FlowReply, error) {
	if input == nil {
		return Form(step, map[string]any{"type": "object"}), nil
	}
	return Create("x", input).WithUniqueID("x"), nil
}

func TestInitializeAndFlow(t *testing.T) {
	f := start(t, &sample{})
	f.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"api_version": 1, "data_dir": "/tmp/x"}})
	m := f.read()
	if m["id"].(float64) != 1 || m["result"].(map[string]any)["api_version"].(float64) != APIVersion {
		t.Fatalf("initialize: %v", m)
	}

	f.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "config_flow", "params": map[string]any{"flow": map[string]any{"flow_id": "f"}, "step": "user", "input": nil}})
	r := f.read()["result"].(map[string]any)["result"].(map[string]any)
	if r["type"] != "form" || r["step_id"] != "user" {
		t.Fatalf("first step: %v", r)
	}
	f.send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "config_flow", "params": map[string]any{"flow": map[string]any{}, "step": "user", "input": map[string]any{"a": 1}}})
	res := f.read()["result"].(map[string]any)
	if res["result"].(map[string]any)["type"] != "create_entry" || res["unique_id"] != "x" {
		t.Fatalf("second step: %v", res)
	}
}

func TestSetupCallsBackAndErrorsCarryKind(t *testing.T) {
	s := &sample{}
	f := start(t, s)

	// setup_entry: the plugin asks TheHome for a device id while TheHome waits
	f.send(map[string]any{"jsonrpc": "2.0", "id": 7, "method": "setup_entry", "params": map[string]any{"entry": map[string]any{"entry_id": "e1", "title": "Stue"}}})
	req := f.read()
	if req["method"] != "device/register" || req["params"].(map[string]any)["entry_id"] != "e1" {
		t.Fatalf("expected device/register, got %v", req)
	}
	f.send(map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": map[string]any{"id": "dev-1"}})
	done := f.read()
	if done["id"].(float64) != 7 || done["error"] != nil {
		t.Fatalf("setup reply: %v", done)
	}
	if s.device != "dev-1" {
		t.Fatalf("device id %q", s.device)
	}

	// an error from the integration keeps its kind
	f.send(map[string]any{"jsonrpc": "2.0", "id": 8, "method": "setup_entry", "params": map[string]any{"entry": map[string]any{"entry_id": "e2", "title": "locked"}}})
	req = f.read()
	f.send(map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": map[string]any{"id": "dev-2"}})
	e := f.read()["error"].(map[string]any)
	if e["data"].(map[string]any)["kind"] != "auth_failed" || e["message"] != "forkert adgangskode" {
		t.Fatalf("error: %v", e)
	}

	// an error from TheHome reaches the integration with its kind
	f.send(map[string]any{"jsonrpc": "2.0", "id": 9, "method": "setup_entry", "params": map[string]any{"entry": map[string]any{"entry_id": "e3"}}})
	req = f.read()
	f.send(map[string]any{"jsonrpc": "2.0", "id": req["id"], "error": map[string]any{"code": -32000, "message": "nej", "data": map[string]any{"kind": "not_found"}}})
	e = f.read()["error"].(map[string]any)
	if e["data"].(map[string]any)["kind"] != "not_found" {
		t.Fatalf("host error: %v", e)
	}

	// defaults from Base, and unknown methods
	f.send(map[string]any{"jsonrpc": "2.0", "id": 10, "method": "entity/call_service", "params": map[string]any{"entity_id": "switch.x", "service": "turn_on"}})
	e = f.read()["error"].(map[string]any)
	if e["data"].(map[string]any)["kind"] != "not_supported" {
		t.Fatalf("default entity service: %v", e)
	}
	f.send(map[string]any{"jsonrpc": "2.0", "id": 11, "method": "nope"})
	e = f.read()["error"].(map[string]any)
	if e["data"].(map[string]any)["kind"] != "not_supported" {
		t.Fatalf("unknown method: %v", e)
	}
}

func TestStopsOnShutdownAndEOF(t *testing.T) {
	f := start(t, &sample{})
	f.send(map[string]any{"jsonrpc": "2.0", "method": "shutdown"})
	select {
	case err := <-f.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop on shutdown")
	}

	g := start(t, &sample{})
	g.in.Close()
	select {
	case <-g.done:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop when stdin closed")
	}
}
