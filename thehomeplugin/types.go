package thehomeplugin

import "fmt"

// Error er en fejl til TheHome. Kind afgør, hvad TheHome gør:
// "not_ready" prøver igen senere, "auth_failed" beder brugeren logge ind
// igen, "invalid_argument", "not_supported", "unavailable" og "not_found"
// vises ved tjenestekald, og "failed" er alt andet.
type Error struct {
	Kind    string
	Message string
}

func (e *Error) Error() string { return e.Kind + ": " + e.Message }

func newError(kind string, format string, args ...any) *Error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

func NotReady(format string, args ...any) *Error   { return newError("not_ready", format, args...) }
func AuthFailed(format string, args ...any) *Error { return newError("auth_failed", format, args...) }
func Failed(format string, args ...any) *Error     { return newError("failed", format, args...) }
func Invalid(format string, args ...any) *Error    { return newError("invalid_argument", format, args...) }
func NotSupported(format string, args ...any) *Error {
	return newError("not_supported", format, args...)
}
func Unavailable(format string, args ...any) *Error { return newError("unavailable", format, args...) }

// Entry er en opsætning af integrationen (en "config entry").
type Entry struct {
	EntryID  string         `json:"entry_id"`
	Domain   string         `json:"domain"`
	Title    string         `json:"title"`
	Data     map[string]any `json:"data"`
	Options  map[string]any `json:"options"`
	UniqueID *string        `json:"unique_id"`
}

// Get giver en værdi fra options først, ellers fra data.
func (e Entry) Get(key string) (any, bool) {
	if v, ok := e.Options[key]; ok && v != nil {
		return v, true
	}
	v, ok := e.Data[key]
	return v, ok && v != nil
}

func (e Entry) GetString(key string) string {
	v, _ := e.Get(key)
	s, _ := v.(string)
	return s
}

// GetFloat giver et tal. JSON-tal er altid float64 i Go.
func (e Entry) GetFloat(key string) (float64, bool) {
	v, _ := e.Get(key)
	f, ok := v.(float64)
	return f, ok
}

// Flow er et opsætningsforløb i gang.
type Flow struct {
	FlowID string `json:"flow_id"`
	// "user", "options", "reauth", "zeroconf" ...
	Source    string `json:"source"`
	Discovery any    `json:"discovery"`
	// Data samlet i tidligere trin.
	Data     map[string]any `json:"data"`
	UniqueID *string        `json:"unique_id"`
	// Ved options- og reauth-forløb: opsætningen, der ændres.
	Entry *Entry `json:"entry"`
}

// FlowReply er svaret på et trin i et opsætningsforløb.
type FlowReply struct {
	Result   map[string]any `json:"result"`
	Data     map[string]any `json:"data,omitempty"`
	UniqueID string         `json:"unique_id,omitempty"`
}

// Form viser en formular. schema er et JSON Schema-objekt (type: object).
func Form(stepID string, schema map[string]any) FlowReply {
	return FlowReply{Result: map[string]any{"type": "form", "step_id": stepID, "schema": schema}}
}

// FormWithErrors viser formularen igen med fejl, f.eks. {"base": "cannot_connect"}.
func FormWithErrors(stepID string, schema map[string]any, errors map[string]any) FlowReply {
	return FlowReply{Result: map[string]any{"type": "form", "step_id": stepID, "schema": schema, "errors": errors}}
}

func Menu(stepID string, options ...string) FlowReply {
	return FlowReply{Result: map[string]any{"type": "menu", "step_id": stepID, "options": options}}
}

// Create afslutter og opretter opsætningen. I et options-forløb bliver data de nye options.
func Create(title string, data map[string]any) FlowReply {
	return FlowReply{Result: map[string]any{"type": "create_entry", "title": title, "data": data}}
}

func Abort(reason string) FlowReply {
	return FlowReply{Result: map[string]any{"type": "abort", "reason": reason}}
}

// WithData gemmer data til de næste trin.
func (r FlowReply) WithData(data map[string]any) FlowReply {
	r.Data = data
	return r
}

// WithUniqueID sætter et unikt id. TheHome afbryder med "already_configured",
// hvis en anden opsætning har det.
func (r FlowReply) WithUniqueID(id string) FlowReply {
	r.UniqueID = id
	return r
}

// Device er en enhed. Felterne svarer til TheHomes enhedsregister.
type Device struct {
	// F.eks. [][2]string{{"mit_domæne", "serienummer"}}. Holder enheden stabil.
	Identifiers      [][2]string `json:"identifiers"`
	Name             string      `json:"name"`
	Manufacturer     string      `json:"manufacturer,omitempty"`
	Model            string      `json:"model,omitempty"`
	SwVersion        string      `json:"sw_version,omitempty"`
	SerialNumber     string      `json:"serial_number,omitempty"`
	SuggestedArea    string      `json:"suggested_area,omitempty"`
	ConfigurationURL string      `json:"configuration_url,omitempty"`
}

// Entity er en entity. Felterne svarer til TheHomes entity-register.
type Entity struct {
	// "sensor", "switch", "light", "button", "climate" ...
	Domain string `json:"domain"`
	// Stabilt id, der aldrig må ændre sig.
	UniqueID string `json:"unique_id"`
	// Navn efter enhedens navn. Tomt betyder enhedens hoved-entity.
	Name              string `json:"name,omitempty"`
	DeviceID          string `json:"device_id,omitempty"`
	Icon              string `json:"icon,omitempty"`
	DeviceClass       string `json:"device_class,omitempty"`
	UnitOfMeasurement string `json:"unit_of_measurement,omitempty"`
	StateClass        string `json:"state_class,omitempty"`
	EntityCategory    string `json:"entity_category,omitempty"`
	// Faste attributter, f.eks. supported_features eller options.
	Capabilities map[string]any `json:"capabilities,omitempty"`
}
