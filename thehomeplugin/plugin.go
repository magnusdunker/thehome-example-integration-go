// Package thehomeplugin er SDK'en til eksterne TheHome-integrationer i Go.
//
// En ekstern integration er et selvstændigt program. TheHome starter det og
// taler med det over JSON-RPC 2.0 på stdin og stdout, én besked pr. linje.
// stderr er integrationens log. Programmet stopper, når stdin lukkes.
//
// Implementér Integration (indlejr Base for standardmetoderne) og kald Run
// fra main:
//
//	type mine struct{ thehomeplugin.Base }
//
//	func (mine) SetupEntry(h *thehomeplugin.Host, e thehomeplugin.Entry) error {
//		id, err := h.AddEntity(e.EntryID, thehomeplugin.Entity{Domain: "sensor", UniqueID: "mine-1"}, false)
//		if err != nil || id == "" {
//			return err
//		}
//		return h.SetState(id, "42", nil)
//	}
//
//	func main() { thehomeplugin.Run(mine{}) }
package thehomeplugin

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// APIVersion er versionen af protokollen, som denne SDK taler.
const APIVersion = 1

const hostTimeout = 30 * time.Second

// Integration er det, en integration implementerer. Indlejr Base for at
// få standardsvar på alt andet end SetupEntry.
type Integration interface {
	// ConfigFlow er opsætningsforløbet. Første kald har step = kilden
	// ("user" ...) og input = nil. De næste har den sidste formulars step_id.
	ConfigFlow(h *Host, flow Flow, step string, input map[string]any) (FlowReply, error)
	// OptionsFlow er indstillinger for en eksisterende opsætning (flow.Entry er sat).
	OptionsFlow(h *Host, flow Flow, step string, input map[string]any) (FlowReply, error)
	// SetupEntry sætter en opsætning op: registrér enheder og entities, start arbejde.
	SetupEntry(h *Host, entry Entry) error
	// UnloadEntry stopper arbejdet for en opsætning. TheHome fjerner selv dens tilstande.
	UnloadEntry(h *Host, entryID string) error
	// EntityService er et tjenestekald på en entity tilføjet med handler = true,
	// f.eks. switch.turn_on.
	EntityService(h *Host, entryID, entityID, service string, data map[string]any) error
	// DomainService er et kald af en tjeneste registreret med Host.RegisterService.
	DomainService(h *Host, entryID, service string, data map[string]any, entityIDs []string) (any, error)
	// Diagnostics er fejlsøgningsdata. Skjul hemmeligheder.
	Diagnostics(h *Host, entry Entry) any
	// RemoveDevice kaldes, når brugeren sletter en enhed.
	RemoveDevice(h *Host, entry Entry, device map[string]any) error
}

// Base giver standardsvar. Indlejr den og implementér SetupEntry.
type Base struct{}

func (Base) ConfigFlow(*Host, Flow, string, map[string]any) (FlowReply, error) {
	return Abort("not_supported"), nil
}
func (Base) OptionsFlow(*Host, Flow, string, map[string]any) (FlowReply, error) {
	return Abort("not_supported"), nil
}
func (Base) UnloadEntry(*Host, string) error { return nil }
func (Base) EntityService(_ *Host, _, entityID, service string, _ map[string]any) error {
	return NotSupported("%s på %s", service, entityID)
}
func (Base) DomainService(_ *Host, _, service string, _ map[string]any, _ []string) (any, error) {
	return nil, NotSupported("%s", service)
}
func (Base) Diagnostics(*Host, Entry) any                    { return nil }
func (Base) RemoveDevice(*Host, Entry, map[string]any) error { return nil }

// ---------------------------------------------------------------------------
// forbindelsen til TheHome

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *wireError      `json:"error,omitempty"`
}

type wireError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    *struct {
		Kind string `json:"kind"`
	} `json:"data,omitempty"`
}

type reply struct {
	result json.RawMessage
	err    error
}

// Host er forbindelsen til TheHome. Den er sikker at bruge fra flere goroutines.
type Host struct {
	out     io.Writer
	writeMu sync.Mutex
	nextID  atomic.Uint64

	mu      sync.Mutex
	pending map[uint64]chan reply
	closed  bool
	dataDir string
}

func newHost(w io.Writer) *Host {
	dir := os.Getenv("THEHOME_DATA_DIR")
	if dir == "" {
		dir = "data"
	}
	return &Host{out: w, pending: map[uint64]chan reply{}, dataDir: dir}
}

func (h *Host) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	_, err = h.out.Write(append(b, '\n'))
	return err
}

// DataDir er integrationens egen mappe til filer. Den bevares ved opdateringer.
func (h *Host) DataDir() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.dataDir
}

// Request kalder en metode i TheHome og venter på svaret. result må være nil.
func (h *Host) Request(method string, params any, result any) error {
	id := h.nextID.Add(1)
	ch := make(chan reply, 1)
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return NotReady("forbindelsen til TheHome er lukket")
	}
	h.pending[id] = ch
	h.mu.Unlock()
	if err := h.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		h.forget(id)
		return NotReady("forbindelsen til TheHome er lukket")
	}
	select {
	case r := <-ch:
		if r.err != nil {
			return r.err
		}
		if result != nil && len(r.result) > 0 {
			return json.Unmarshal(r.result, result)
		}
		return nil
	case <-time.After(hostTimeout):
		h.forget(id)
		return Failed("%s: TheHome svarede ikke", method)
	}
}

func (h *Host) forget(id uint64) {
	h.mu.Lock()
	delete(h.pending, id)
	h.mu.Unlock()
}

func (h *Host) resolve(id uint64, r reply) {
	h.mu.Lock()
	ch, ok := h.pending[id]
	delete(h.pending, id)
	h.mu.Unlock()
	if ok {
		ch <- r
	}
}

func (h *Host) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for id, ch := range h.pending {
		ch <- reply{err: NotReady("forbindelsen til TheHome er lukket")}
		delete(h.pending, id)
	}
}

// RegisterDevice registrerer en enhed og returnerer dens id.
func (h *Host) RegisterDevice(entryID string, d Device) (string, error) {
	var res struct {
		ID string `json:"id"`
	}
	if err := h.Request("device/register", map[string]any{"entry_id": entryID, "device": d}, &res); err != nil {
		return "", err
	}
	if res.ID == "" {
		return "", Failed("intet enheds-id")
	}
	return res.ID, nil
}

// AddEntity tilføjer en entity. Med handler sender TheHome dens tjenestekald
// til Integration.EntityService. Returnerer entity_id, eller "" hvis
// brugeren har slået den fra.
func (h *Host) AddEntity(entryID string, e Entity, handler bool) (string, error) {
	var res struct {
		EntityID *string `json:"entity_id"`
	}
	if err := h.Request("entity/add", map[string]any{"entry_id": entryID, "entity": e, "handler": handler}, &res); err != nil {
		return "", err
	}
	if res.EntityID == nil {
		return "", nil
	}
	return *res.EntityID, nil
}

func (h *Host) SetState(entityID, state string, attributes map[string]any) error {
	if attributes == nil {
		attributes = map[string]any{}
	}
	return h.Request("state/set", map[string]any{"entity_id": entityID, "state": state, "attributes": attributes}, nil)
}

func (h *Host) SetUnavailable(entityID string) error {
	return h.Request("state/unavailable", map[string]any{"entity_id": entityID}, nil)
}

// GetState giver tilstanden af en vilkårlig entity, eller nil.
func (h *Host) GetState(entityID string) (map[string]any, error) {
	var res map[string]any
	err := h.Request("state/get", map[string]any{"entity_id": entityID}, &res)
	return res, err
}

// RegisterService registrerer tjenesten <domæne>.<service>. Kald ender i
// Integration.DomainService.
func (h *Host) RegisterService(entryID, service string, description map[string]any) error {
	return h.Request("service/register", map[string]any{"entry_id": entryID, "service": service, "description": description}, nil)
}

// CallService kalder en tjeneste i TheHome, f.eks. light.turn_on.
func (h *Host) CallService(domain, service string, data map[string]any) (any, error) {
	var res any
	err := h.Request("hub/call_service", map[string]any{"domain": domain, "service": service, "data": data}, &res)
	return res, err
}

func (h *Host) FireEvent(eventType string, data map[string]any) error {
	if data == nil {
		data = map[string]any{}
	}
	return h.Request("event/fire", map[string]any{"event_type": eventType, "data": data}, nil)
}

// CreateIssue beder brugeren rette noget. severity: "warning" eller "error".
func (h *Host) CreateIssue(entryID, issueID, severity, title, description string) error {
	return h.Request("issue/create", map[string]any{"entry_id": entryID, "issue_id": issueID, "severity": severity, "title": title, "description": description}, nil)
}

func (h *Host) DeleteIssue(entryID, issueID string) error {
	return h.Request("issue/delete", map[string]any{"entry_id": entryID, "issue_id": issueID}, nil)
}

// UpdateEntryData gemmer ny data (f.eks. et fornyet token) på opsætningen.
func (h *Host) UpdateEntryData(entryID string, data map[string]any) error {
	return h.Request("entry/update", map[string]any{"entry_id": entryID, "data": data}, nil)
}

// Log skriver til TheHomes log. level: "ERROR", "WARN", "INFO" eller "DEBUG".
func Log(level, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%s %s\n", level, fmt.Sprintf(format, args...))
}

// ---------------------------------------------------------------------------
// løkken

// Run kører integrationen på stdin og stdout, indtil TheHome lukker stdin
// eller beder om at stoppe. Derefter afslutter programmet.
func Run(i Integration) {
	if err := Serve(os.Stdin, os.Stdout, i); err != nil {
		Log("ERROR", "%v", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// Serve læser beskeder fra r og skriver svar til w, indtil r slutter eller
// TheHome sender shutdown. Run bruger den med stdin og stdout; tests kan
// bruge den med pipes.
func Serve(r io.Reader, w io.Writer, i Integration) error {
	h := newHost(w)
	defer h.close()
	var wg sync.WaitGroup
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var m message
		if err := json.Unmarshal(line, &m); err != nil {
			Log("WARN", "ugyldig besked fra TheHome: %s", line)
			continue
		}
		if m.Method == "" {
			// svar på et af vores egne kald
			var id uint64
			if json.Unmarshal(m.ID, &id) == nil {
				if m.Error != nil {
					kind := "failed"
					if m.Error.Data != nil && m.Error.Data.Kind != "" {
						kind = m.Error.Data.Kind
					}
					h.resolve(id, reply{err: &Error{Kind: kind, Message: m.Error.Message}})
				} else {
					h.resolve(id, reply{result: m.Result})
				}
			}
			continue
		}
		if m.Method == "shutdown" {
			break
		}
		wg.Add(1)
		go func(m message) {
			defer wg.Done()
			res, err := dispatch(i, h, m.Method, m.Params)
			if len(m.ID) == 0 || string(m.ID) == "null" {
				return
			}
			if err != nil {
				var e *Error
				if !errors.As(err, &e) {
					e = &Error{Kind: "failed", Message: err.Error()}
				}
				_ = h.send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{
					"code": -32000, "message": e.Message, "data": map[string]any{"kind": e.Kind}}})
				return
			}
			_ = h.send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": res})
		}(m)
	}
	h.close()
	wg.Wait()
	return sc.Err()
}

type params struct {
	DataDir   string         `json:"data_dir"`
	Flow      Flow           `json:"flow"`
	Step      string         `json:"step"`
	Input     map[string]any `json:"input"`
	Entry     Entry          `json:"entry"`
	EntryID   string         `json:"entry_id"`
	EntityID  string         `json:"entity_id"`
	Service   string         `json:"service"`
	Data      map[string]any `json:"data"`
	EntityIDs []string       `json:"entity_ids"`
	Device    map[string]any `json:"device"`
}

func dispatch(i Integration, h *Host, method string, raw json.RawMessage) (any, error) {
	var p params
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, Invalid("%s: %v", method, err)
		}
	}
	if p.Data == nil {
		p.Data = map[string]any{}
	}
	switch method {
	case "initialize":
		if p.DataDir != "" {
			h.mu.Lock()
			h.dataDir = p.DataDir
			h.mu.Unlock()
		}
		return map[string]any{"api_version": APIVersion}, nil
	case "config_flow", "options_flow":
		run := i.ConfigFlow
		if method == "options_flow" {
			run = i.OptionsFlow
		}
		r, err := run(h, p.Flow, p.Step, p.Input)
		if err != nil {
			return nil, err
		}
		return r, nil
	case "setup_entry":
		return nil, i.SetupEntry(h, p.Entry)
	case "unload_entry":
		return nil, i.UnloadEntry(h, p.EntryID)
	case "entity/call_service":
		return nil, i.EntityService(h, p.EntryID, p.EntityID, p.Service, p.Data)
	case "service/call":
		return i.DomainService(h, p.EntryID, p.Service, p.Data, p.EntityIDs)
	case "diagnostics":
		return i.Diagnostics(h, p.Entry), nil
	case "remove_device":
		return nil, i.RemoveDevice(h, p.Entry, p.Device)
	}
	return nil, NotSupported("ukendt metode %s", method)
}
