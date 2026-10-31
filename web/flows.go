package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Saved flows: a run_steps batch kept under a name, to run again later.
//
// A batch that worked once — open the settings screen of the app under
// development, log in to the staging site, walk to the page being fixed — is
// usually needed again many times. Replaying it by name costs one tool call
// with no model reasoning between steps, and the host's !flow command runs it
// with no model at all (zero tokens). Flows are plain JSON files under
// <dataDir>/flows/, so they can also be edited by hand.

type savedFlow struct {
	Name     string          `json:"name"`
	Profile  string          `json:"profile,omitempty"` // target it was recorded on (app:…, an account, or "")
	Steps    json.RawMessage `json:"steps"`
	Snapshot string          `json:"snapshot,omitempty"`
	Saved    string          `json:"saved"`
}

var flowNameRE = regexp.MustCompile(`^[\p{L}\p{N}_.-]{1,64}$`)

func flowsDir() (string, error) {
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(dir, "flows")
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	return d, nil
}

func flowPath(name string) (string, error) {
	if !flowNameRE.MatchString(name) {
		return "", fmt.Errorf("flow name %q: use letters, digits, - _ . (max 64)", name)
	}
	d, err := flowsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, name+".json"), nil
}

// rawSteps turns the steps argument back into a JSON array for storage.
func rawSteps(v any) (json.RawMessage, error) {
	if s, ok := v.(string); ok {
		var probe []any
		if err := json.Unmarshal([]byte(s), &probe); err != nil {
			return nil, err
		}
		return json.RawMessage(strings.TrimSpace(s)), nil
	}
	return json.Marshal(v)
}

func saveFlow(name, profile string, steps any, snapshot string) error {
	p, err := flowPath(name)
	if err != nil {
		return err
	}
	raw, err := rawSteps(steps)
	if err != nil {
		return fmt.Errorf("steps: %v", err)
	}
	f := savedFlow{Name: name, Profile: profile, Steps: raw, Snapshot: snapshot, Saved: time.Now().Format(time.RFC3339)}
	b, _ := json.MarshalIndent(f, "", "  ")
	return os.WriteFile(p, b, 0o600)
}

func loadFlow(name string) (*savedFlow, error) {
	p, err := flowPath(name)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no flow named %q — list_flows shows the saved ones", name)
		}
		return nil, err
	}
	var f savedFlow
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("flow %q is not valid JSON: %v", name, err)
	}
	return &f, nil
}

// runFlow replays a saved flow. The caller's profile wins over the recorded
// one, so a flow recorded on one window can be pointed at another.
func (d *Daemon) runFlow(params map[string]any, profile string) CallResult {
	name := strings.TrimSpace(str(params, "name"))
	if name == "" {
		return CallResult{Error: "run_flow requires 'name'"}
	}
	f, err := loadFlow(name)
	if err != nil {
		return CallResult{Error: err.Error()}
	}
	p := map[string]any{"steps": string(f.Steps), "snapshot": f.Snapshot}
	for _, k := range []string{"snapshot", "tabId", "step_delay_ms", "continue_on_error"} {
		if v, ok := params[k]; ok && v != nil && v != "" {
			p[k] = v
		}
	}
	if profile == "" {
		profile = f.Profile
	}
	res := d.runSteps(p, profile)
	prefix := "flow " + name + ": "
	if res.OK {
		res.Text = prefix + res.Text
	} else {
		res.Error = prefix + res.Error
	}
	return res
}

func (d *Daemon) listFlows() CallResult {
	dir, err := flowsDir()
	if err != nil {
		return CallResult{Error: err.Error()}
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	sort.Strings(matches)
	var lines []string
	for _, m := range matches {
		name := strings.TrimSuffix(filepath.Base(m), ".json")
		f, err := loadFlow(name)
		if err != nil {
			lines = append(lines, name+" | (unreadable: "+oneLine(err.Error(), 80)+")")
			continue
		}
		var steps []any
		_ = json.Unmarshal(f.Steps, &steps)
		target := f.Profile
		if target == "" {
			target = "(default Chrome)"
		}
		lines = append(lines, fmt.Sprintf("%s | %d steps | %s | saved %s", name, len(steps), target, f.Saved))
	}
	if len(lines) == 0 {
		return CallResult{OK: true, Text: `(no saved flows — run_steps with save_as="name" saves one)`}
	}
	return CallResult{OK: true, Text: strings.Join(lines, "\n")}
}
