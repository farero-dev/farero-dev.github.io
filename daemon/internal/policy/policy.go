// Package policy decides what happens to a tool call (기능 명세서 6장).
//
// Plugin tools are classified by an app-managed table (policy/default.json,
// embedded here) plus the user's overrides. The engine also keeps the
// in-memory "allow for this session" grants; taint is stored on the session
// and passed in by the caller.
package policy

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/farero-dev/farero/daemon/internal/model"
)

//go:generate cp ../../../policy/default.json default.json

//go:embed default.json
var defaultTable []byte

// Levels (6-1).
const (
	LevelAuto  = "auto"
	LevelAsk   = "ask"
	LevelBlock = "block"
)

// Rule is one tool's classification.
type Rule struct {
	Level       string `json:"level"`
	NoSession   bool   `json:"no_session,omitempty"`
	Taint       bool   `json:"taint,omitempty"`
	ReadOnly    *bool  `json:"read_only,omitempty"`
	Destructive bool   `json:"destructive,omitempty"`
}

// IsReadOnly reports whether the tool only reads. It defaults to the
// table's own level being auto, so a user override does not change what the
// tool actually does.
func (r Rule) IsReadOnly(defaultLevel string) bool {
	if r.ReadOnly != nil {
		return *r.ReadOnly
	}
	return defaultLevel == LevelAuto
}

// Table is the parsed classification table.
type Table struct {
	Version int                    `json:"version"`
	Plugins map[string]PluginRules `json:"plugins"`
}

// PluginRules is one plugin's section of the table.
type PluginRules struct {
	Tools map[string]Rule `json:"tools"`
}

// ParseTable parses and validates a classification table.
func ParseTable(b []byte) (*Table, error) {
	var t Table
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, err
	}
	for p, pt := range t.Plugins {
		for name, r := range pt.Tools {
			switch r.Level {
			case LevelAuto, LevelAsk, LevelBlock:
			default:
				return nil, fmt.Errorf("%s/%s: bad level %q", p, name, r.Level)
			}
			if r.NoSession && r.Level == LevelAuto {
				return nil, fmt.Errorf("%s/%s: no_session tool cannot be auto", p, name)
			}
		}
	}
	return &t, nil
}

// AddPlugin adds (or replaces) a plugin's rules. farerod uses it for the
// development plugin, which is not part of the shipped table.
func (t *Table) AddPlugin(name string, rules map[string]Rule) {
	if t.Plugins == nil {
		t.Plugins = map[string]PluginRules{}
	}
	t.Plugins[name] = PluginRules{Tools: rules}
}

// DefaultTable returns the embedded default table.
func DefaultTable() *Table {
	t, err := ParseTable(defaultTable)
	if err != nil {
		panic("embedded policy table: " + err.Error())
	}
	return t
}

// ToolKey is the exposed gateway tool name and the override key.
func ToolKey(plugin, tool string) string { return plugin + "_" + tool }

// Engine evaluates calls against the table, overrides and session grants.
type Engine struct {
	mu        sync.Mutex
	table     *Table
	overrides map[string]string          // ToolKey -> level
	grants    map[string]map[string]bool // session id -> grant key
}

// NewEngine builds an engine. overrides may be nil.
func NewEngine(t *Table, overrides map[string]string) *Engine {
	if overrides == nil {
		overrides = map[string]string{}
	}
	return &Engine{table: t, overrides: overrides, grants: map[string]map[string]bool{}}
}

// Effective is a tool's rule after applying the user's override.
type Effective struct {
	Rule
	DefaultLevel string `json:"default_level"`
	Overridden   bool   `json:"overridden"`
}

// Lookup returns the effective rule for a plugin tool.
func (e *Engine) Lookup(plugin, tool string) (Effective, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lookupLocked(plugin, tool)
}

func (e *Engine) lookupLocked(plugin, tool string) (Effective, bool) {
	pt, ok := e.table.Plugins[plugin]
	if !ok {
		return Effective{}, false
	}
	r, ok := pt.Tools[tool]
	if !ok {
		return Effective{}, false
	}
	eff := Effective{Rule: r, DefaultLevel: r.Level}
	if lv, ok := e.overrides[ToolKey(plugin, tool)]; ok && lv != r.Level {
		eff.Level = lv
		eff.Overridden = true
	}
	return eff, true
}

// Exposed reports whether the tool is listed to agents: classified and not
// blocked (6-1, 6-2 step 1).
func (e *Engine) Exposed(plugin, tool string) bool {
	eff, ok := e.Lookup(plugin, tool)
	return ok && eff.Level != LevelBlock
}

// SetOverride changes a tool's level. A no_session tool may only be ask or
// block: the user cannot lift "approve every time" (Q33). An empty level
// resets to the table default.
func (e *Engine) SetOverride(plugin, tool, level string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	eff, ok := e.lookupLocked(plugin, tool)
	if !ok {
		return fmt.Errorf("unknown tool %s", ToolKey(plugin, tool))
	}
	key := ToolKey(plugin, tool)
	switch level {
	case "":
		delete(e.overrides, key)
		return nil
	case LevelAuto, LevelAsk, LevelBlock:
	default:
		return fmt.Errorf("bad level %q", level)
	}
	if eff.NoSession && level == LevelAuto {
		return fmt.Errorf("%s requires approval every time and cannot be auto-allowed", key)
	}
	if level == eff.DefaultLevel {
		delete(e.overrides, key)
	} else {
		e.overrides[key] = level
	}
	return nil
}

// Overrides returns a copy of the current overrides.
func (e *Engine) Overrides() map[string]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]string, len(e.overrides))
	for k, v := range e.overrides {
		out[k] = v
	}
	return out
}

// ToolInfo describes one classified tool for the settings screen.
type ToolInfo struct {
	Plugin string `json:"plugin"`
	Tool   string `json:"tool"`
	Effective
}

// Tools lists every classified tool, sorted by plugin then tool.
func (e *Engine) Tools() []ToolInfo {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []ToolInfo
	for p, pt := range e.table.Plugins {
		for name := range pt.Tools {
			eff, _ := e.lookupLocked(p, name)
			out = append(out, ToolInfo{Plugin: p, Tool: name, Effective: eff})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Plugin != out[j].Plugin {
			return out[i].Plugin < out[j].Plugin
		}
		return out[i].Tool < out[j].Tool
	})
	return out
}

// Outcome is what the caller must do.
type Outcome int

const (
	Run         Outcome = iota // execute now
	Ask                        // show an approval card
	Deny                       // refuse without asking (app not running)
	Block                      // refuse: the tool is not exposed
	Passthrough                // agent tool only: leave it to the agent's own prompt
)

func (o Outcome) String() string {
	return [...]string{"run", "ask", "deny", "block", "passthrough"}[o]
}

// Decision is the engine's verdict.
type Decision struct {
	Outcome Outcome
	// Decision is the audit log value for Run, Deny, Block and Passthrough.
	Decision string
	// Reasons explain an Ask (shown on the card) or the other outcomes.
	Reasons []string
	// AllowSession is whether the card offers "allow for this session".
	AllowSession bool
}

// PluginCall is a gateway tool call to decide.
type PluginCall struct {
	Plugin      string
	Tool        string
	SessionID   string // "" when the call could not be tied to a session
	Tainted     bool
	UIConnected bool
}

// DecidePlugin applies the decision order of 기능 명세서 6-2.
func (e *Engine) DecidePlugin(c PluginCall) Decision {
	e.mu.Lock()
	defer e.mu.Unlock()
	eff, ok := e.lookupLocked(c.Plugin, c.Tool)
	if !ok {
		return Decision{Outcome: Block, Decision: model.DecisionBlocked, Reasons: []string{model.ReasonUnclassified}}
	}
	switch eff.Level {
	case LevelBlock:
		return Decision{Outcome: Block, Decision: model.DecisionBlocked, Reasons: []string{model.ReasonPolicy}}
	case LevelAuto:
		return Decision{Outcome: Run, Decision: model.DecisionAutoAllowed, Reasons: []string{model.ReasonPolicy}}
	}
	if !c.UIConnected {
		return Decision{Outcome: Deny, Decision: model.DecisionAutoDenied, Reasons: []string{model.ReasonAppNotRunning}}
	}
	known := c.SessionID != ""
	grantable := known && !c.Tainted && !eff.NoSession
	if grantable && e.grants[c.SessionID][grantKeyPlugin(c.Plugin, c.Tool)] {
		return Decision{Outcome: Run, Decision: model.DecisionSessionAllowed, Reasons: []string{model.ReasonPolicy}}
	}
	reasons := []string{model.ReasonPolicy}
	if c.Tainted {
		reasons = append(reasons, model.ReasonTainted)
	}
	if !known {
		reasons = append(reasons, model.ReasonUnknownSession)
	}
	return Decision{Outcome: Ask, Reasons: reasons, AllowSession: grantable}
}

// AgentCall is an agent's own permission request (F-04).
type AgentCall struct {
	SessionID   string
	Tool        string
	Input       json.RawMessage
	Tainted     bool
	UIConnected bool
}

// DecideAgent decides a PermissionRequest. Grants are per tool and the same
// input (Q38) and are void while the session is tainted (Q39).
func (e *Engine) DecideAgent(c AgentCall) Decision {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !c.UIConnected {
		return Decision{Outcome: Passthrough, Decision: model.DecisionPassthrough, Reasons: []string{model.ReasonAppNotRunning}}
	}
	grantable := c.SessionID != "" && !c.Tainted
	if grantable && e.grants[c.SessionID][GrantKeyAgent(c.Tool, c.Input)] {
		return Decision{Outcome: Run, Decision: model.DecisionSessionAllowed, Reasons: []string{model.ReasonAgentRequest}}
	}
	reasons := []string{model.ReasonAgentRequest}
	if c.Tainted {
		reasons = append(reasons, model.ReasonTainted)
	}
	return Decision{Outcome: Ask, Reasons: reasons, AllowSession: grantable}
}

// GrantPlugin records "allow for this session" for a plugin tool.
func (e *Engine) GrantPlugin(sessionID, plugin, tool string) {
	e.grant(sessionID, grantKeyPlugin(plugin, tool))
}

// GrantAgent records "allow for this session" for an agent tool and input.
func (e *Engine) GrantAgent(sessionID, tool string, input json.RawMessage) {
	e.grant(sessionID, GrantKeyAgent(tool, input))
}

func (e *Engine) grant(sessionID, key string) {
	if sessionID == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.grants[sessionID] == nil {
		e.grants[sessionID] = map[string]bool{}
	}
	e.grants[sessionID][key] = true
}

// EndSession forgets a session's grants (they last until the session ends).
func (e *Engine) EndSession(sessionID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.grants, sessionID)
}

func grantKeyPlugin(plugin, tool string) string { return "plugin\x00" + ToolKey(plugin, tool) }

// GrantKeyAgent scopes an agent-tool grant: the same shell command, the same
// file for edits, otherwise the identical input.
func GrantKeyAgent(tool string, input json.RawMessage) string {
	var m map[string]any
	_ = json.Unmarshal(input, &m)
	str := func(k string) string {
		s, _ := m[k].(string)
		return s
	}
	switch tool {
	case "Bash":
		return "agent\x00Bash\x00" + str("command")
	case "Edit", "Write", "MultiEdit":
		return "agent\x00" + tool + "\x00" + str("file_path")
	case "NotebookEdit":
		return "agent\x00NotebookEdit\x00" + str("notebook_path")
	}
	return "agent\x00" + tool + "\x00" + CanonicalHash(input)
}

// CanonicalHash hashes JSON with object keys sorted, so the same value hashes
// the same regardless of key order (Claude Code's hook input keeps the
// model's key order while the MCP request has sorted keys; M0 experiment 1).
func CanonicalHash(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		v = string(raw)
	}
	b, _ := json.Marshal(v) // encoding/json sorts map keys
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
