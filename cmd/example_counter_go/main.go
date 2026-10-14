// Eksempel-tæller (Go): en ekstern integration til TheHome.
//
// Hver opsætning får en enhed med
//   - en sensor, der tæller én op med et fast interval,
//   - en kontakt, der sætter tælleren på pause,
//   - en knap, der nulstiller den.
//
// Den viser også et opsætningsforløb med validering, et options-forløb,
// en tjeneste (example_counter_go.set), en hændelse og en fil i
// integrationens datamappe, så tallet overlever en genstart.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	p "github.com/magnusdunker/thehome-example-integration-go/thehomeplugin"
)

const domain = "example_counter_go"

// version sættes ved build med -ldflags "-X main.version=v1.2.3".
var version = "dev"

type counter struct {
	count     uint64
	running   bool
	countID   string
	runningID string
	resetID   string
	stop      chan struct{}
}

type example struct {
	p.Base
	mu       sync.Mutex
	counters map[string]*counter
}

func userSchema(name string, interval int) map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"name", "interval"},
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "title": "Navn", "default": name},
			"interval": map[string]any{"type": "integer", "title": "Interval (sekunder)", "minimum": 1, "maximum": 3600, "default": interval,
				"description": "Hvor tit tælleren tæller én op"},
		},
	}
}

func optionsSchema(interval int) map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"interval"},
		"properties": map[string]any{
			"interval": map[string]any{"type": "integer", "title": "Interval (sekunder)", "minimum": 1, "maximum": 3600, "default": interval},
		},
	}
}

func intervalOf(e p.Entry) int {
	f, ok := e.GetFloat("interval")
	if !ok {
		return 10
	}
	return min(max(int(f), 1), 3600)
}

func intOf(v any, def int) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return def
}

// registerSetService registrerer example_counter_go.set. Tjenesten gælder
// alle opsætninger; målet afgør hvilken. TheHome knytter den til den
// opsætning, der registrerede den.
func registerSetService(h *p.Host, entryID string) error {
	return h.RegisterService(entryID, "set", map[string]any{
		"name":        "Sæt tæller",
		"description": "Sætter en tæller til en bestemt værdi",
		"target":      map[string]any{"entity": map[string]any{"integration": domain, "domain": "sensor"}},
		"fields": map[string]any{
			"value": map[string]any{"name": "Værdi", "required": true, "example": 100,
				"selector": map[string]any{"number": map[string]any{"min": 0, "max": 1000000}}},
		},
	})
}

func file(h *p.Host, entryID string) string {
	return filepath.Join(h.DataDir(), entryID+".json")
}

func load(h *p.Host, entryID string) uint64 {
	b, err := os.ReadFile(file(h, entryID))
	if err != nil {
		return 0
	}
	var v struct {
		Count uint64 `json:"count"`
	}
	_ = json.Unmarshal(b, &v)
	return v.Count
}

func save(h *p.Host, entryID string, count uint64) {
	_ = os.MkdirAll(h.DataDir(), 0o755)
	if err := os.WriteFile(file(h, entryID), []byte(fmt.Sprintf(`{"count":%d}`, count)), 0o644); err != nil {
		p.Log("WARN", "kunne ikke gemme tælleren: %v", err)
	}
}

// publish sender tællerens og kontaktens tilstand til TheHome.
func (x *example) publish(h *p.Host, entryID string) error {
	x.mu.Lock()
	c, ok := x.counters[entryID]
	if !ok {
		x.mu.Unlock()
		return nil
	}
	count, running, countID, runningID := c.count, c.running, c.countID, c.runningID
	x.mu.Unlock()
	if countID != "" {
		if err := h.SetState(countID, strconv.FormatUint(count, 10), nil); err != nil {
			return err
		}
	}
	if runningID != "" {
		state := "off"
		if running {
			state = "on"
		}
		return h.SetState(runningID, state, nil)
	}
	return nil
}

func (x *example) setCount(h *p.Host, entryID string, value uint64) error {
	x.mu.Lock()
	c, ok := x.counters[entryID]
	if ok {
		c.count = value
	}
	x.mu.Unlock()
	if !ok {
		return p.NotReady("opsætningen kører ikke")
	}
	save(h, entryID, value)
	return x.publish(h, entryID)
}

func (x *example) reset(h *p.Host, entryID string) error {
	if err := x.setCount(h, entryID, 0); err != nil {
		return err
	}
	resetID := ""
	x.mu.Lock()
	if c, ok := x.counters[entryID]; ok {
		resetID = c.resetID
	}
	x.mu.Unlock()
	if resetID != "" {
		// en knaps tilstand er tidspunktet for sidste tryk
		if err := h.SetState(resetID, time.Now().UTC().Format(time.RFC3339Nano), nil); err != nil {
			return err
		}
	}
	return h.FireEvent(domain+"_reset", map[string]any{"entry_id": entryID})
}

func (x *example) ConfigFlow(_ *p.Host, _ p.Flow, step string, input map[string]any) (p.FlowReply, error) {
	if step != "user" {
		return p.FlowReply{}, p.Invalid("ukendt trin %s", step)
	}
	if input == nil {
		return p.Form("user", userSchema("Tæller", 10)), nil
	}
	name, _ := input["name"].(string)
	name = strings.TrimSpace(name)
	interval := intOf(input["interval"], 10)
	if name == "" {
		return p.FormWithErrors("user", userSchema("", interval), map[string]any{"name": "required"}), nil
	}
	return p.Create(name, map[string]any{"name": name, "interval": interval}).WithUniqueID(strings.ToLower(name)), nil
}

func (x *example) OptionsFlow(_ *p.Host, flow p.Flow, _ string, input map[string]any) (p.FlowReply, error) {
	current := 10
	if flow.Entry != nil {
		current = intervalOf(*flow.Entry)
	}
	if input == nil {
		return p.Form("init", optionsSchema(current)), nil
	}
	return p.Create("", map[string]any{"interval": intOf(input["interval"], current)}), nil
}

func (x *example) SetupEntry(h *p.Host, entry p.Entry) error {
	id := entry.EntryID
	name := entry.GetString("name")
	if name == "" {
		name = entry.Title
	}
	interval := intervalOf(entry)

	device, err := h.RegisterDevice(id, p.Device{
		Identifiers:  [][2]string{{domain, id}},
		Name:         name,
		Manufacturer: "TheHome",
		Model:        "Eksempel-tæller (Go)",
		SwVersion:    version,
	})
	if err != nil {
		return err
	}
	countID, err := h.AddEntity(id, p.Entity{Domain: "sensor", UniqueID: id + "-count", Name: "Antal", DeviceID: device, Icon: "mdi:counter", StateClass: "total_increasing"}, false)
	if err != nil {
		return err
	}
	runningID, err := h.AddEntity(id, p.Entity{Domain: "switch", UniqueID: id + "-running", Name: "Tæller", DeviceID: device, Icon: "mdi:play-pause"}, true)
	if err != nil {
		return err
	}
	resetID, err := h.AddEntity(id, p.Entity{Domain: "button", UniqueID: id + "-reset", Name: "Nulstil", DeviceID: device, Icon: "mdi:restart"}, true)
	if err != nil {
		return err
	}
	if err := registerSetService(h, id); err != nil {
		return err
	}

	count := load(h, id)
	c := &counter{count: count, running: true, countID: countID, runningID: runningID, resetID: resetID, stop: make(chan struct{})}
	x.mu.Lock()
	x.counters[id] = c
	x.mu.Unlock()
	if resetID != "" {
		if err := h.SetState(resetID, "unknown", nil); err != nil {
			return err
		}
	}
	if err := x.publish(h, id); err != nil {
		return err
	}

	go x.tick(h, id, c, time.Duration(interval)*time.Second)
	p.Log("INFO", "%s tæller hvert %d. sekund fra %d", name, interval, count)
	return nil
}

func (x *example) tick(h *p.Host, entryID string, c *counter, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-t.C:
		}
		x.mu.Lock()
		if !c.running {
			x.mu.Unlock()
			continue
		}
		c.count++
		n, sensor := c.count, c.countID
		x.mu.Unlock()
		save(h, entryID, n)
		if sensor != "" {
			if err := h.SetState(sensor, strconv.FormatUint(n, 10), nil); err != nil {
				p.Log("WARN", "kunne ikke opdatere %s: %v", sensor, err)
			}
		}
	}
}

func (x *example) UnloadEntry(h *p.Host, entryID string) error {
	x.mu.Lock()
	if c, ok := x.counters[entryID]; ok {
		close(c.stop)
		delete(x.counters, entryID)
	}
	other := ""
	for id := range x.counters {
		other = id
		break
	}
	x.mu.Unlock()
	// TheHome fjerner tjenesten med opsætningen; de andre skal stadig have den
	if other != "" {
		return registerSetService(h, other)
	}
	return nil
}

func (x *example) EntityService(h *p.Host, entryID, entityID, service string, _ map[string]any) error {
	x.mu.Lock()
	c, ok := x.counters[entryID]
	if !ok {
		x.mu.Unlock()
		return p.Unavailable("%s", entityID)
	}
	isSwitch, isButton := c.runningID == entityID, c.resetID == entityID
	if isSwitch {
		switch service {
		case "turn_on":
			c.running = true
		case "turn_off":
			c.running = false
		case "toggle":
			c.running = !c.running
		default:
			isSwitch = false
		}
	}
	x.mu.Unlock()
	switch {
	case isSwitch:
		return x.publish(h, entryID)
	case isButton && service == "press":
		return x.reset(h, entryID)
	}
	return p.NotSupported("%s på %s", service, entityID)
}

func (x *example) DomainService(h *p.Host, _ string, service string, data map[string]any, entityIDs []string) (any, error) {
	if service != "set" {
		return nil, p.NotSupported("%s", service)
	}
	value, ok := data["value"].(float64)
	if !ok {
		return nil, p.Invalid("value mangler")
	}
	if value < 0 {
		return nil, p.Invalid("value skal være 0 eller mere")
	}
	var targets []string
	x.mu.Lock()
	for id, c := range x.counters {
		if slices.Contains(entityIDs, c.countID) || slices.Contains(entityIDs, c.runningID) {
			targets = append(targets, id)
		}
	}
	x.mu.Unlock()
	if len(targets) == 0 {
		return nil, p.Invalid("vælg en tæller som mål")
	}
	for _, id := range targets {
		if err := x.setCount(h, id, uint64(value)); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func (x *example) Diagnostics(_ *p.Host, entry p.Entry) any {
	x.mu.Lock()
	defer x.mu.Unlock()
	c, ok := x.counters[entry.EntryID]
	if !ok {
		return map[string]any{"loaded": false}
	}
	return map[string]any{"count": c.count, "running": c.running, "interval": intervalOf(entry)}
}

func main() {
	p.Run(&example{counters: map[string]*counter{}})
}
