// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package capability

// Agents: a folder per agent in the holder's Drive, a workspace per agent
// in dsh (plan §3.6, decided 2026-09-14).
//
// An agent is `agents/<name>/` in the holder's Drive: `agent.md` (persona and
// standing instructions), `agent.yaml` (trigger, budget, declared
// resources), `skills/`, and two folders the runs write, `state/` and
// `runs/`. The harness app stays the trust boundary; what the agent DOES is
// this folder, which the holder reads, edits, exports and hands to someone
// else, and which the chat itself writes through Drive's own tools.
//
// It is mirrored BY DIRECTION, per subfolder, so there is no merge and no
// conflict: the definition is pulled (Drive is the source of truth, local
// copies are owned by the proxy and read-only for the worker, so a stray
// local write fails loudly instead of vanishing a tick later); `state/` and
// `runs/` are pushed (the run writes them locally, they land in Drive, they
// are never pulled). The agent's `skills/` is pulled to `.agents/skills/`
// under the workspace, which is where dsh's skill provider looks for a
// workspace's own skills; those outrank the holder's shared set.
//
// The local folder is a sibling of the holder's other workspaces, so dsh's
// Files tab shows it as it shows any workspace, and the blob snapshot of the
// working tree leaves it out: Drive already holds the truth of it.

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

// AgentTrigger is one of: every (a Go duration), or on (an event source
// named "<tool>.<call>": a mounted tool's change-feed call, held by the proxy
// as the holder; the contract is in proxy routines.go).
type AgentTrigger struct {
	Every string `yaml:"every"`
	On    string `yaml:"on"`
}

// eventSourcePattern is "<tool>.<call>", each a plain lowercase identifier.
var eventSourcePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)

// Scheduled reports whether the agent has any unattended trigger.
func (a AgentSpec) Scheduled() bool {
	return !a.Paused && (a.Trigger.Every != "" || a.Trigger.On != "")
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
	if spec.Trigger.Every != "" {
		if _, err := time.ParseDuration(spec.Trigger.Every); err != nil {
			return AgentSpec{}, fmt.Errorf("trigger.every %q is not a duration", spec.Trigger.Every)
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
	if s.agentDirs == nil {
		s.agentDirs = map[string]bool{}
	}
	if s.agents == nil {
		s.agents = map[string]AgentSpec{}
	}
	s.mu.Unlock()
}

// ValidAgentName reports whether a name can be an agent folder, and so a
// workspace title. Exported for the harness's own agents tools.
func ValidAgentName(name string) bool { return validAgentName(name) }

// ParseAgentSpec parses an agent.yaml, for a caller that refuses a bad one
// before it is written.
func ParseAgentSpec(data []byte) (AgentSpec, error) { return parseAgentSpec(data) }

// WriteAgent writes an agent's definition files into `agents/<name>/` in the
// holder's Drive, creating the folders as needed, and returns that path.
//
// This is how the CHAT makes an agent (the agent-builder skill). Drive's
// assistant tools are read-only by design, and the harness's own storage
// capability already covers its app folder, so writing the folder is the
// harness's job; the mirror pulls it back on its next pass, which is what
// makes the agent exist here. The folders are resolved afresh rather than
// from the path cache: a holder who deleted the agent in Drive and asks for
// it again must get a new folder, not a write into the old id.
func (s *Syncer) WriteAgent(name string, files map[string][]byte) (string, error) {
	if !validAgentName(name) {
		return "", fmt.Errorf("%q is not a valid agent name", name)
	}
	ds, _ := s.store()
	if ds == nil {
		return "", errors.New("the user's Drive folder for this assistant is not connected, so there is nowhere to write the agent")
	}
	agentsID, err := ds.EnsureFolder(ds.RootID(), agentsFolder)
	if err != nil {
		return "", fmt.Errorf("create %s/: %w", agentsFolder, err)
	}
	folderID, err := ds.EnsureFolder(agentsID, name)
	if err != nil {
		return "", fmt.Errorf("create %s/%s/: %w", agentsFolder, name, err)
	}
	rel := agentsFolder + "/" + name
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, fname := range names {
		if _, err := ds.PutIn(folderID, fname, files[fname]); err != nil {
			return "", fmt.Errorf("write %s/%s: %w", rel, fname, err)
		}
		s.mu.Lock()
		if s.uploaded != nil {
			s.uploaded[rel+"/"+fname] = contentHash(files[fname])
		}
		s.mu.Unlock()
	}
	return rel, nil
}

// Agents returns the agents pulled on the last pass, sorted by name.
func (s *Syncer) Agents() []AgentSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AgentSpec, 0, len(s.agents))
	for _, a := range s.agents {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// isAgentDir reports whether a top-level workspace entry is an agent folder
// the mirror owns, so the snapshot leaves it out.
func (s *Syncer) isAgentDir(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agentDirs[name]
}

// syncAgents mirrors every agent folder: definition down, outputs up.
func (s *Syncer) syncAgents(ds *DriveStore) error {
	s.mu.Lock()
	root, uid := s.agentsRoot, s.agentUID
	s.mu.Unlock()
	if root == "" {
		return nil
	}
	agentsID, err := s.folderIfExists(ds, agentsFolder)
	if err != nil {
		return err
	}
	// No agents folder: nothing to pull, and every agent this mirror created
	// is gone with it (the holder deleted the whole folder, 2026-09-16), so
	// the pruning below still runs. The chat creates the folder when asked.
	var children []Node
	if agentsID != "" {
		if children, err = ds.ListIn(agentsID); err != nil {
			return err
		}
	}
	seen := map[string]AgentSpec{}
	pulled, pushed := 0, 0
	for _, c := range children {
		if !c.IsFolder() || !validAgentName(c.Name) {
			continue
		}
		local := filepath.Join(root, c.Name)
		if !s.claimAgentDir(local, c.Name) {
			log.Printf("[sync] agents: %q exists as a workspace that is not an agent; not mirroring over it", c.Name)
			continue
		}
		n, spec, err := s.pullAgent(ds, c.ID, local, c.Name, uid)
		pulled += n
		if err != nil {
			return err
		}
		m, err := s.pushAgentOutputs(ds, c.ID, local, c.Name)
		pushed += m
		if err != nil {
			return err
		}
		seen[c.Name] = spec
	}
	// An agent whose folder left Drive leaves here too. The mirror owns the
	// directories it created, and a workspace that no longer exists in Drive
	// would otherwise stay in the sidebar with the engine still polling for
	// it. Only reached once Drive answered the listing, so an outage never
	// reads as a deletion.
	s.mu.Lock()
	var gone []string
	for name := range s.agentDirs {
		if _, ok := seen[name]; !ok {
			gone = append(gone, name)
		}
	}
	s.mu.Unlock()
	sort.Strings(gone)
	for _, name := range gone {
		if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
			log.Printf("[sync] agents: %q left the holder's Drive but its workspace could not be removed: %v", name, err)
			continue
		}
		s.mu.Lock()
		delete(s.agentDirs, name)
		s.mu.Unlock()
		log.Printf("[sync] agents for %.8s…: %q left the holder's Drive; its workspace is removed", s.subject, name)
	}
	s.mu.Lock()
	s.agents = seen
	s.mu.Unlock()
	if pulled > 0 || pushed > 0 {
		log.Printf("[sync] agents for %.8s…: %d agent(s), %d definition file(s) read from the holder's Drive, %d output file(s) written to it", s.subject, len(seen), pulled, pushed)
	}
	return nil
}

// claimAgentDir marks a local directory as an agent's, refusing to take over
// a directory the holder made themselves (a workspace that carries no agent
// definition). A directory the mirror created is always its own.
func (s *Syncer) claimAgentDir(local, name string) bool {
	s.mu.Lock()
	known := s.agentDirs[name]
	s.mu.Unlock()
	if known {
		return true
	}
	if _, err := os.Stat(local); err == nil {
		if _, perr := os.Stat(filepath.Join(local, agentPersonaFile)); perr != nil {
			if _, serr := os.Stat(filepath.Join(local, agentSpecFile)); serr != nil {
				return false
			}
		}
	}
	s.mu.Lock()
	s.agentDirs[name] = true
	s.mu.Unlock()
	return true
}

// pullAgent mirrors one agent's definition down and makes it the proxy's:
// directories 0755, files 0644, owned by root, so the worker reads and never
// writes. The output folders exist, owned by the worker.
func (s *Syncer) pullAgent(ds *DriveStore, nodeID, local, name string, uid int) (int, AgentSpec, error) {
	if err := os.MkdirAll(local, 0o755); err != nil {
		return 0, AgentSpec{}, err
	}
	children, err := ds.ListIn(nodeID)
	if err != nil {
		return 0, AgentSpec{}, err
	}
	rel := agentsFolder + "/" + name
	present := map[string]bool{}
	count := 0
	for _, c := range children {
		if agentOutputDirs[c.Name] || isLockFile(c.Name) || strings.HasPrefix(c.Name, ".") {
			continue
		}
		target := filepath.Join(local, c.Name)
		if c.IsFolder() {
			if c.Name == agentSkillsFolder {
				target = filepath.Join(local, filepath.FromSlash(agentSkillsLocal))
			}
			n, err := s.pullTree(ds, c.ID, target, rel+"/"+c.Name, 0o644, present)
			count += n
			if err != nil {
				return count, AgentSpec{}, err
			}
			continue
		}
		data, err := ds.Get(c.ID)
		if err != nil {
			return count, AgentSpec{}, err
		}
		present[target] = true
		if sameOnDisk(target, data) {
			continue
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return count, AgentSpec{}, err
		}
		s.mu.Lock()
		s.uploaded[rel+"/"+c.Name] = contentHash(data)
		s.mu.Unlock()
		count++
	}
	pruned := pruneDefinition(local, present)
	count += pruned
	ownDefinition(local, uid)
	for dir := range agentOutputDirs {
		out := filepath.Join(local, dir)
		_ = os.MkdirAll(out, 0o755)
		chownAll(out, uid)
	}
	spec := AgentSpec{Name: name, Path: local}
	if data, err := os.ReadFile(filepath.Join(local, agentSpecFile)); err == nil {
		parsed, perr := parseAgentSpec(data)
		if perr != nil {
			log.Printf("[sync] agents: %s/%s: %v (the agent runs only when asked)", name, agentSpecFile, perr)
		} else {
			spec = parsed
			spec.Name, spec.Path = name, local
		}
	}
	return count, spec, nil
}

// pullTree mirrors one Drive folder down, writing only what differs, with the
// given file mode. `present` collects the local paths the folder holds, for
// pruning; nil to skip that.
func (s *Syncer) pullTree(ds *DriveStore, nodeID, dir, rel string, perm os.FileMode, present map[string]bool) (int, error) {
	children, err := ds.ListIn(nodeID)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dir, dirModeFor(perm)); err != nil {
		return 0, err
	}
	if present != nil {
		present[dir] = true
	}
	count := 0
	for _, c := range children {
		if isLockFile(c.Name) || strings.HasPrefix(c.Name, ".") {
			continue
		}
		target := filepath.Join(dir, c.Name)
		if c.IsFolder() {
			n, err := s.pullTree(ds, c.ID, target, rel+"/"+c.Name, perm, present)
			count += n
			if err != nil {
				return count, err
			}
			continue
		}
		data, err := ds.Get(c.ID)
		if err != nil {
			return count, err
		}
		if present != nil {
			present[target] = true
		}
		if sameOnDisk(target, data) {
			continue
		}
		if err := os.WriteFile(target, data, perm); err != nil {
			return count, err
		}
		s.mu.Lock()
		s.uploaded[rel+"/"+c.Name] = contentHash(data)
		s.mu.Unlock()
		count++
	}
	return count, nil
}

// pruneDefinition removes local definition files the holder's Drive no
// longer holds, so a deleted skill stops acting. Output folders are never
// touched, and directories are left (a workspace path must keep existing).
func pruneDefinition(local string, present map[string]bool) int {
	removed := 0
	_ = filepath.WalkDir(local, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path == local {
			return nil
		}
		relTop := firstSegment(local, path)
		if agentOutputDirs[relTop] {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		if !present[path] && !isLockFile(d.Name()) {
			if os.Remove(path) == nil {
				removed++
			}
		}
		return nil
	})
	return removed
}

// firstSegment is the top-level name of path under root.
func firstSegment(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return ""
	}
	rel = filepath.ToSlash(rel)
	if i := strings.Index(rel, "/"); i >= 0 {
		return rel[:i]
	}
	return rel
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

// pushAgentOutputs uploads what a run wrote under state/ and runs/, only what
// changed since the last push. Drive never overwrites these locally.
func (s *Syncer) pushAgentOutputs(ds *DriveStore, agentID, local, name string) (int, error) {
	count := 0
	for _, dir := range sortedKeys(agentOutputDirs) {
		src := filepath.Join(local, dir)
		entries, err := os.ReadDir(src)
		if err != nil || len(entries) == 0 {
			continue
		}
		folder, err := ds.EnsureFolder(agentID, dir)
		if err != nil {
			return count, err
		}
		n, err := s.pushTree(ds, folder, src, agentsFolder+"/"+name+"/"+dir)
		count += n
		if err != nil {
			return count, err
		}
	}
	return count, nil
}

// pushTree uploads changed regular files of a local tree into a Drive folder.
func (s *Syncer) pushTree(ds *DriveStore, parent, dir, rel string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || isLockFile(name) {
			continue
		}
		path := filepath.Join(dir, name)
		key := rel + "/" + name
		if e.IsDir() {
			folder, err := ds.EnsureFolder(parent, name)
			if err != nil {
				return count, err
			}
			n, err := s.pushTree(ds, folder, path, key)
			count += n
			if err != nil {
				return count, err
			}
			continue
		}
		if !e.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil || len(data) > maxSnapshotFile {
			continue
		}
		hash := contentHash(data)
		s.mu.Lock()
		same := s.uploaded[key] == hash
		s.mu.Unlock()
		if same {
			continue
		}
		if _, err := ds.PutIn(parent, name, data); err != nil {
			return count, err
		}
		s.mu.Lock()
		s.uploaded[key] = hash
		s.mu.Unlock()
		count++
	}
	return count, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ErrNoAgents is returned by callers that need at least one agent.
var ErrNoAgents = errors.New("the holder has no agents")
