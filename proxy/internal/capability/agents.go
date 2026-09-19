// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package capability

// Agents: a folder per agent in the holder folder, a workspace per agent in
// dsh (plan §3.6, decided 2026-09-14; local since §5.9, 2026-09-19).
//
// An agent is `<workspace root>/<name>/`: `agent.md` (persona and standing
// instructions), `agent.yaml` (trigger, budget, declared resources),
// `.agents/skills/` (its own skills, where dsh's skill provider looks), and
// two folders the runs write, `state/` and `runs/`. The definition is owned
// by root and read-only for the worker, so a run cannot rewrite what it is;
// the chat writes it through the `agents` MCP server (WriteAgent), and the
// holder reads it in dsh's files panel. The marker file says "this
// directory is an agent", so the routine engine finds it after a restart and
// a run in it is filed as an agent run rather than a conversation.

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
)

const (
	agentsFolder = "agents"
	// agentSkillsLocal is where dsh's skill provider reads a workspace's own
	// skills (`<project root>/.agents/skills`). Drive shows the plain name.
	agentSkillsFolder = "skills"
	agentSkillsLocal  = ".agents/skills"
	agentSpecFile     = "agent.yaml"
	agentPersonaFile  = "agent.md"
	// agentMarkerFile says "the mirror made this directory": the claim
	// survives a restart (agentDirs is memory), so an agent folder deleted
	// in Drive while the harness was down is still pruned, and an agent
	// written again under a name whose directory lingers is still mirrored
	// (2026-09-16: "exists as a workspace that is not an agent; not
	// mirroring over it", and the new agent never ran).
	agentMarkerFile = ".privasys-agent"
	// maxAgentName bounds a folder name that becomes a workspace title.
	maxAgentName = 64
)

// agentOutputDirs are the subfolders a RUN writes: pushed to Drive, never
// pulled, owned by the worker.
var agentOutputDirs = map[string]bool{"state": true, "runs": true}

// AgentSpec is the parsed `agent.yaml`, plus where the agent lives.
type AgentSpec struct {
	Name string `yaml:"-"`
	// Path is the local workspace directory dsh runs the agent in.
	Path string `yaml:"-"`

	// Prompt is what a scheduled run is asked to do. Empty means the agent
	// runs only when a person asks in its workspace.
	Prompt  string       `yaml:"prompt"`
	Trigger AgentTrigger `yaml:"trigger"`
	// Debounce collects a burst of events into one run; MinInterval is the
	// shortest gap between two runs whatever arrives. Go durations.
	Debounce    string `yaml:"debounce"`
	MinInterval string `yaml:"min_interval"`
	Paused      bool   `yaml:"paused"`
}

// AgentTrigger is exactly one of: every (a Go duration), at (a cron
// schedule, read in UTC since the harness does not know the holder's
// timezone), or on (an event source named "<tool>.<call>": a mounted tool's
// change-feed call, held by the proxy as the holder; the contract is in
// proxy routines.go).
type AgentTrigger struct {
	Every string `yaml:"every"`
	At    string `yaml:"at"`
	On    string `yaml:"on"`
}

// eventSourcePattern is "<tool>.<call>", each a plain lowercase identifier.
var eventSourcePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)

// cronParser reads `at`: the five standard fields (minute, hour, day of
// month, month, day of week) and the descriptors (@daily, @hourly, ...), so
// "0 17 * * FRI" and "@daily" both work. No seconds field: a run is not a
// thing that happens twice a minute.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// Schedule is the parsed `at`, or nil when the trigger is not a schedule
// (or not a valid one; parseAgentSpec refused that already).
func (a AgentSpec) Schedule() cron.Schedule {
	if a.Trigger.At == "" {
		return nil
	}
	s, err := cronParser.Parse(a.Trigger.At)
	if err != nil {
		return nil
	}
	return s
}

// Scheduled reports whether the agent has any unattended trigger.
func (a AgentSpec) Scheduled() bool {
	return !a.Paused && (a.Trigger.Every != "" || a.Trigger.At != "" || a.Trigger.On != "")
}

// Durations returns the debounce and minimum interval with their defaults
// (2 minutes and 10 minutes, plan §3.3).
func (a AgentSpec) Durations() (debounce, minInterval time.Duration) {
	debounce, minInterval = 2*time.Minute, 10*time.Minute
	if d, err := time.ParseDuration(a.Debounce); err == nil && d >= 0 {
		debounce = d
	}
	if d, err := time.ParseDuration(a.MinInterval); err == nil && d >= 0 {
		minInterval = d
	}
	return debounce, minInterval
}

// parseAgentSpec reads an agent.yaml. An absent or empty file is a valid
// agent with no trigger.
func parseAgentSpec(data []byte) (AgentSpec, error) {
	var spec AgentSpec
	if len(strings.TrimSpace(string(data))) == 0 {
		return spec, nil
	}
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return AgentSpec{}, err
	}
	set := 0
	for _, v := range []string{spec.Trigger.Every, spec.Trigger.At, spec.Trigger.On} {
		if v != "" {
			set++
		}
	}
	if set > 1 {
		// Two triggers are two opinions about when to run; the definition
		// says one, and a second one is a mistake to state, not to guess at.
		return AgentSpec{}, errors.New("trigger names more than one of every, at and on; an agent has exactly one trigger")
	}
	if spec.Trigger.Every != "" {
		if _, err := time.ParseDuration(spec.Trigger.Every); err != nil {
			return AgentSpec{}, fmt.Errorf("trigger.every %q is not a duration", spec.Trigger.Every)
		}
	}
	if spec.Trigger.At != "" {
		if _, err := cronParser.Parse(spec.Trigger.At); err != nil {
			return AgentSpec{}, fmt.Errorf("trigger.at %q is not a cron schedule: %v", spec.Trigger.At, err)
		}
	}
	if spec.Trigger.On != "" && !eventSourcePattern.MatchString(spec.Trigger.On) {
		return AgentSpec{}, fmt.Errorf("trigger.on %q is not an event source (<tool>.<call>)", spec.Trigger.On)
	}
	return spec, nil
}

// validAgentName accepts a Drive folder name as an agent: one path segment,
// not hidden, short enough to be a workspace title.
func validAgentName(name string) bool {
	if name == "" || len(name) > maxAgentName || strings.HasPrefix(name, ".") {
		return false
	}
	if strings.ContainsAny(name, `/\`) || name != strings.TrimSpace(name) {
		return false
	}
	return name != "." && name != ".."
}

// SetAgentsRoot wires the holder's agents to a local root (the workspace
// root: each agent becomes a sibling of the other workspaces) and names the
// worker uid that owns the folders a run writes. Empty leaves agents off.
func (s *Syncer) SetAgentsRoot(local string, uid int) {
	s.mu.Lock()
	s.agentsRoot, s.agentUID = local, uid
	s.mu.Unlock()
}

// markedAgentDirs lists the workspace root's directories that carry the
// mirror's marker.
func markedAgentDirs(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, e.Name(), agentMarkerFile)); err == nil {
			out = append(out, e.Name())
		}
	}
	return out
}

// ValidAgentName reports whether a name can be an agent folder, and so a
// workspace title. Exported for the harness's own agents tools.
func ValidAgentName(name string) bool { return validAgentName(name) }

// ParseAgentSpec parses an agent.yaml, for a caller that refuses a bad one
// before it is written.
func ParseAgentSpec(data []byte) (AgentSpec, error) { return parseAgentSpec(data) }

// WriteAgent writes an agent's definition files into `<name>/` under the
// workspace root, and returns that path relative to the root.
//
// This is how the CHAT makes an agent (the agent-builder skill): the proxy
// writes, as root, so the definition is read-only for the worker; the output
// folders are the worker's. Written files that already hold the same bytes
// are left alone, so a re-run of the builder changes nothing on disk.
func (s *Syncer) WriteAgent(name string, files map[string][]byte) (string, error) {
	if !validAgentName(name) {
		return "", fmt.Errorf("%q is not a valid agent name", name)
	}
	s.mu.Lock()
	root, uid := s.agentsRoot, s.agentUID
	s.mu.Unlock()
	if root == "" {
		return "", errors.New("this worker has no workspace root, so there is nowhere to write the agent")
	}
	local := filepath.Join(root, name)
	if err := os.MkdirAll(local, 0o755); err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(local, agentMarkerFile)); err != nil {
		if err := os.WriteFile(filepath.Join(local, agentMarkerFile), []byte("an agent of the holder's, written by the harness\n"), 0o644); err != nil {
			return "", err
		}
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, fname := range names {
		if !validAgentName(fname) {
			return "", fmt.Errorf("%q is not a valid agent file name", fname)
		}
		target := filepath.Join(local, fname)
		if have, err := os.ReadFile(target); err == nil && string(have) == string(files[fname]) {
			continue
		}
		if err := os.WriteFile(target, files[fname], 0o644); err != nil {
			return "", fmt.Errorf("write %s/%s: %w", name, fname, err)
		}
	}
	ownDefinition(local, uid)
	for dir := range agentOutputDirs {
		out := filepath.Join(local, dir)
		_ = os.MkdirAll(out, 0o755)
		chownAll(out, uid)
	}
	return name, nil
}

// Agents lists the holder's agents as the workspace root holds them: every
// marked directory, its `agent.yaml` parsed (an unparsable one runs only
// when asked, and says so once per listing).
func (s *Syncer) Agents() []AgentSpec {
	s.mu.Lock()
	root := s.agentsRoot
	s.mu.Unlock()
	if root == "" {
		return nil
	}
	names := markedAgentDirs(root)
	sort.Strings(names)
	out := make([]AgentSpec, 0, len(names))
	for _, name := range names {
		local := filepath.Join(root, name)
		spec := AgentSpec{Name: name, Path: local}
		if data, err := os.ReadFile(filepath.Join(local, agentSpecFile)); err == nil {
			parsed, perr := parseAgentSpec(data)
			if perr != nil {
				log.Printf("[agents] %s/%s: %v (the agent runs only when asked)", name, agentSpecFile, perr)
			} else {
				spec = parsed
				spec.Name, spec.Path = name, local
			}
		}
		out = append(out, spec)
	}
	return out
}

// isAgentDir reports whether a top-level workspace entry is an agent's
// directory (it carries the marker).
func (s *Syncer) isAgentDir(name string) bool {
	s.mu.Lock()
	root := s.agentsRoot
	s.mu.Unlock()
	if root == "" || !validAgentName(name) {
		return false
	}
	_, err := os.Stat(filepath.Join(root, name, agentMarkerFile))
	return err == nil
}

// isAgentCwd reports whether a session's working directory is inside one of
// the holder's agent workspaces: such a session is an agent run, kept in
// the holder folder with the agent rather than mirrored as a conversation.
func (s *Syncer) isAgentCwd(cwd string) bool {
	s.mu.Lock()
	root := s.agentsRoot
	s.mu.Unlock()
	if root == "" || cwd == "" {
		return false
	}
	rel, err := filepath.Rel(root, cwd)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	first := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
	return s.isAgentDir(first)
}

// ownDefinition makes the definition the proxy's: root-owned, world-readable,
// writable by nobody but root. Errors are ignored off platform (no root).
func ownDefinition(local string, uid int) {
	_ = filepath.WalkDir(local, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path != local && agentOutputDirs[firstSegment(local, path)] {
			return filepath.SkipDir
		}
		if d.IsDir() {
			_ = os.Chmod(path, 0o755)
		} else {
			_ = os.Chmod(path, 0o644)
		}
		_ = os.Chown(path, 0, 0)
		return nil
	})
	_ = uid
}

// dirModeFor is the mode of a directory holding files of mode perm: a
// world-readable file needs a world-searchable directory to be reached by the
// worker's uid (a skill pulled from Drive is read by dsh, not by the proxy).
func dirModeFor(perm os.FileMode) os.FileMode {
	if perm&0o004 != 0 {
		return 0o755
	}
	return perm | 0o100
}

// chownAll hands a tree to a uid (no-op for uid 0 or off platform).
func chownAll(root string, uid int) {
	if uid <= 0 {
		return
	}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil {
			_ = os.Chown(path, uid, uid)
		}
		return nil
	})
}

// ErrNoAgents is returned by callers that need at least one agent.
var ErrNoAgents = errors.New("the holder has no agents")

// firstSegment is the first path element of path below root.
func firstSegment(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return ""
	}
	return strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
}

// IsAgentDir is isAgentDir for callers outside the package.
func (s *Syncer) IsAgentDir(name string) bool { return s.isAgentDir(name) }
