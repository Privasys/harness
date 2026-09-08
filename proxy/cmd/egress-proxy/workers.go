// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

// One dsh process per signed-in user (WS5, the multi-tenancy rails).
//
// dsh is one trust domain per process: every client of a host sees every
// session, and users are not a concept it has. A mutualised harness therefore
// runs one dsh WORKER per subject, and this measured Go layer is the only
// thing that knows which worker is whose:
//
//   - ingress: the relay-asserted X-Privasys-Sub picks the worker; a request
//     without a subject reaches only the SYSTEM worker, which serves the
//     public shell and the plugin bundles and is never handed an /api call;
//   - egress (model + tools): each worker holds a private bearer the proxy
//     minted for it, and that bearer names the subject a call acts for — the
//     model or dsh cannot pick another user's identity, they can only present
//     the token they were started with;
//   - shell egress (the forward proxy): each worker runs as its own uid, and
//     the connecting uid names the subject.
//
// A worker's session logs live on tmpfs and its workspace and dsh home on
// the encrypted volume, both as a write-back CACHE of the user's Drive
// (drive-as-remote-disk.md, tier C decision): restored before the worker
// starts, mirrored while it runs. Idle workers are stopped; their cache
// stays for the next start.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Privasys/attested-harness/proxy/internal/capability"
)

const (
	workerBasePort    = 3080
	workerBaseUID     = 10000
	workerReadyWait   = 180 * time.Second
	workerIdleDefault = 30 * time.Minute
	systemSubject     = "" // the subject-less worker
)

// Worker is one dsh process bound to one subject.
type Worker struct {
	Subject   string
	Key       string // subjectKey(Subject); "" is the system worker's dir "system"
	UID       int
	Port      int
	Token     string
	Dir       string // per-user cache root on the encrypted volume
	Sessions  string
	Workspace string
	Home      string

	cmd    *exec.Cmd
	syncer *capability.Syncer

	mu       sync.Mutex
	ready    bool
	lastSeen time.Time
	started  time.Time
	exited   chan struct{}
}

// Upstream is the worker's loopback web server.
func (w *Worker) Upstream() string { return fmt.Sprintf("http://127.0.0.1:%d", w.Port) }

func (w *Worker) touch() {
	w.mu.Lock()
	w.lastSeen = time.Now()
	w.mu.Unlock()
}

func (w *Worker) isReady() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ready
}

// WorkerManager owns the workers and the maps that attribute requests.
type WorkerManager struct {
	cfg       config
	broker    *capability.Broker
	client    *http.Client
	driveHost string
	appID     string
	useUIDs   bool
	idle      time.Duration

	mu       sync.Mutex
	bySub    map[string]*Worker
	byToken  map[string]*Worker
	byUID    map[int]*Worker
	nextPort int
	nextUID  int
}

func newWorkerManager(cfg config, broker *capability.Broker, client *http.Client) *WorkerManager {
	idle := workerIdleDefault
	if v := os.Getenv("HARNESS_WORKER_IDLE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			idle = d
		}
	}
	useUIDs := os.Getenv("HARNESS_WORKER_UIDS") != "0" && os.Getuid() == 0
	if _, err := exec.LookPath("setpriv"); err != nil {
		useUIDs = false
	}
	return &WorkerManager{
		cfg: cfg, broker: broker, client: client,
		driveHost: cfg.toolHosts["drive"], appID: os.Getenv("HARNESS_APP_ID"),
		useUIDs: useUIDs, idle: idle,
		bySub: map[string]*Worker{}, byToken: map[string]*Worker{}, byUID: map[int]*Worker{},
		nextPort: workerBasePort, nextUID: workerBaseUID,
	}
}

// workersEnabled reports whether this process supervises dsh itself. Off
// (the legacy single-user layout) the entrypoint runs one dsh and the
// process-wide acting subject applies.
func workersEnabled() bool { return os.Getenv("HARNESS_WORKERS") == "1" }

func workerKey(sub string) string {
	if sub == systemSubject {
		return "system"
	}
	sum := sha256.Sum256([]byte(sub))
	return hex.EncodeToString(sum[:8])
}

// Get returns the worker for a subject without starting one.
func (m *WorkerManager) Get(sub string) *Worker {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bySub[sub]
}

// ByToken attributes an egress call to the worker holding that bearer.
func (m *WorkerManager) ByToken(token string) *Worker {
	if token == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byToken[token]
}

// ByUID attributes a shell connection to the worker running as that uid.
func (m *WorkerManager) ByUID(uid int) *Worker {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byUID[uid]
}

// Ensure returns the subject's worker, starting one if needed. Starting is
// asynchronous: the caller gets the record at once and the ingress answers
// "starting" until the worker's web server is up.
func (m *WorkerManager) Ensure(sub string) *Worker {
	m.mu.Lock()
	if w := m.bySub[sub]; w != nil {
		m.mu.Unlock()
		w.touch()
		return w
	}
	key := workerKey(sub)
	w := &Worker{
		Subject: sub, Key: key,
		Port:     m.nextPort,
		Token:    randomToken(),
		Dir:      filepath.Join(envOr("HARNESS_USERS_DIR", "/data/users"), key),
		exited:   make(chan struct{}),
		lastSeen: time.Now(), started: time.Now(),
	}
	m.nextPort++
	if m.useUIDs {
		w.UID = m.loadOrAssignUID(w)
	}
	w.Sessions = filepath.Join(envOr("HARNESS_SESSIONS_TMPFS", "/dev/shm/privasys-users"), key, "sessions")
	w.Workspace = filepath.Join(w.Dir, "workspace")
	w.Home = filepath.Join(w.Dir, "dsh-home")
	m.bySub[sub] = w
	m.byToken[w.Token] = w
	if w.UID != 0 {
		m.byUID[w.UID] = w
	}
	m.mu.Unlock()
	go m.start(w)
	return w
}

// loadOrAssignUID keeps one uid per user across restarts (the cache on the
// volume is owned by it), allocated from a small file beside the cache.
func (m *WorkerManager) loadOrAssignUID(w *Worker) int {
	if w.Subject == systemSubject {
		return workerBaseUID
	}
	path := filepath.Join(w.Dir, ".uid")
	if raw, err := os.ReadFile(path); err == nil {
		if v, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && v > workerBaseUID {
			if v >= m.nextUID {
				m.nextUID = v + 1
			}
			return v
		}
	}
	m.nextUID++
	uid := m.nextUID
	_ = os.MkdirAll(w.Dir, 0o700)
	_ = os.WriteFile(path, []byte(strconv.Itoa(uid)+"\n"), 0o600)
	return uid
}

func randomToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic("no entropy for worker tokens")
	}
	return hex.EncodeToString(b)
}

// start prepares the worker's roots, restores the user's data from their
// Drive, and launches dsh. Errors are logged; the ingress keeps answering
// "starting" and a later request retries a failed start.
func (m *WorkerManager) start(w *Worker) {
	if err := m.prepare(w); err != nil {
		log.Printf("[workers] %s: prepare: %v", w.Key, err)
		m.forget(w)
		return
	}
	if w.Subject != systemSubject {
		// Restore BEFORE dsh starts: it lists its workspaces once at boot.
		w.syncer = capability.NewSyncerFor(m.broker, m.client, m.driveHost, m.appID, w.Sessions, w.Workspace, w.Subject)
		w.syncer.LoadState()
		if n, err := w.syncer.RestoreFor(w.Subject); err != nil {
			log.Printf("[workers] %s: restore: %v", w.Key, err)
		} else if n > 0 {
			log.Printf("[workers] %s: restored %d file(s) from the holder's Drive", w.Key, n)
		}
		w.syncer.SetReady()
		if w.UID != 0 {
			chownTree(w.Sessions, w.UID)
			chownTree(w.Workspace, w.UID)
		}
	}
	cmd, err := m.command(w)
	if err != nil {
		log.Printf("[workers] %s: %v", w.Key, err)
		m.forget(w)
		return
	}
	w.cmd = cmd
	if err := cmd.Start(); err != nil {
		log.Printf("[workers] %s: start dsh: %v", w.Key, err)
		m.forget(w)
		return
	}
	log.Printf("[workers] %s: dsh started (pid %d, port %d, uid %d, subject %.8s…)", w.Key, cmd.Process.Pid, w.Port, w.UID, w.Subject)
	go func() {
		err := cmd.Wait()
		log.Printf("[workers] %s: dsh exited: %v", w.Key, err)
		close(w.exited)
		m.forget(w)
	}()
	// Readiness: dsh serves its index once booted.
	deadline := time.Now().Add(workerReadyWait)
	for time.Now().Before(deadline) {
		select {
		case <-w.exited:
			return
		default:
		}
		resp, err := http.Get(w.Upstream() + "/")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				w.mu.Lock()
				w.ready = true
				w.mu.Unlock()
				log.Printf("[workers] %s: ready after %s", w.Key, time.Since(w.started).Round(time.Second))
				if w.syncer != nil {
					w.syncer.Start(15 * time.Second)
				}
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	log.Printf("[workers] %s: dsh did not become ready within %s; stopping it", w.Key, workerReadyWait)
	m.Stop(w)
}

// prepare lays out the worker's directories. The dsh home is per worker so
// settings, the KV store and attachments never mix between users; the
// measured profiles are shared read-only through a symlink.
func (m *WorkerManager) prepare(w *Worker) error {
	for _, d := range []string{w.Dir, w.Sessions, w.Workspace, w.Home} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	profiles := filepath.Join(w.Home, "profiles")
	if _, err := os.Lstat(profiles); err != nil {
		src := filepath.Join(envOr("DSH_HOME", "/dsh-home"), "profiles")
		if err := os.Symlink(src, profiles); err != nil {
			return fmt.Errorf("profiles link: %w", err)
		}
	}
	patch := fmt.Sprintf("- id: webserver\n  name: \"@deepseek-ai/dsh-host-webserver\"\n  config:\n    host: \"127.0.0.1\"\n    port: %d\n"+
		"- id: session-persistence-jsonl\n  name: \"@deepseek-ai/dsh-session-persistence-jsonl\"\n  config:\n    root: %s\n", w.Port, w.Sessions)
	if err := os.WriteFile(filepath.Join(w.Dir, "worker.cordis.yml"), []byte(patch), 0o600); err != nil {
		return err
	}
	if w.UID != 0 {
		chownTree(w.Dir, w.UID)
		chownTree(w.Sessions, w.UID)
	}
	return nil
}

func chownTree(root string, uid int) {
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		_ = os.Lchown(path, uid, uid)
		return nil
	})
}

// command builds the dsh launch: the measured profile plus the worker's own
// patch, the worker's home and workspace, and the bearer that names it on
// every egress call.
func (m *WorkerManager) command(w *Worker) (*exec.Cmd, error) {
	bin := envOr("HARNESS_DSH_BIN", "/dsh/apps/cli/lib/bin.js")
	args := []string{bin, "--profile", "web",
		"--patch", envOr("HARNESS_PROFILE_PATCH", "/app/profile.cordis.yml"),
		"--patch", filepath.Join(w.Dir, "worker.cordis.yml"),
		"--", "--no-open"}
	if h := os.Getenv("HARNESS_PUBLIC_HOST"); h != "" {
		args = append(args, "--trusted-host", h)
	}
	var cmd *exec.Cmd
	if w.UID != 0 {
		full := append([]string{"--reuid=" + strconv.Itoa(w.UID), "--regid=" + strconv.Itoa(w.UID), "--clear-groups", "--", "node"}, args...)
		cmd = exec.Command("setpriv", full...)
	} else {
		cmd = exec.Command("node", args...)
	}
	cmd.Dir = w.Workspace
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	env["HOME"] = w.Workspace
	env["DSH_HOME"] = w.Home
	env["PRIVASYS_BEARER"] = w.Token
	env["DEEPSEEK_API_KEY"] = w.Token
	env["DEEPSEEK_BASE_URL"] = "http://" + m.cfg.listenAddr + "/model/v1"
	env["USER"] = "harness-" + w.Key
	// The governed fast path for the worker's shell: every child inherits
	// these. Loopback and the enclave manager stay direct (the manager sits
	// on the gateway IP, not loopback). Set here rather than inherited: the
	// proxy's own environment must never route through itself.
	if m.cfg.forwardListen != "" {
		proxyURL := "http://" + m.cfg.forwardListen
		noProxy := "localhost,127.0.0.1,::1,[::1]"
		if mgrURL, err := neturl.Parse(os.Getenv("PRIVASYS_MANAGER_URL")); err == nil && mgrURL.Hostname() != "" {
			noProxy += "," + mgrURL.Hostname()
		}
		for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
			env[k] = proxyURL
		}
		env["NO_PROXY"], env["no_proxy"] = noProxy, noProxy
	}
	delete(env, "PRIVASYS_CONTAINER_TOKEN") // the runtime's credential is the proxy's, never a worker's
	delete(env, "HARNESS_WORKERS")
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	setProcessGroup(cmd)
	return cmd, nil
}

// forget drops a worker from the maps (its cache stays on disk).
func (m *WorkerManager) forget(w *Worker) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bySub[w.Subject] == w {
		delete(m.bySub, w.Subject)
	}
	delete(m.byToken, w.Token)
	if w.UID != 0 && m.byUID[w.UID] == w {
		delete(m.byUID, w.UID)
	}
	if w.syncer != nil {
		w.syncer.SaveState()
	}
}

// Stop ends a worker: a last mirror pass, then SIGTERM to its process group.
func (m *WorkerManager) Stop(w *Worker) {
	if w.syncer != nil {
		if err := w.syncer.SyncOnce(); err != nil {
			log.Printf("[workers] %s: final mirror: %v", w.Key, err)
		}
		w.syncer.SaveState()
	}
	if w.cmd != nil && w.cmd.Process != nil {
		_ = signalGroup(w.cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-w.exited:
		case <-time.After(20 * time.Second):
			_ = signalGroup(w.cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	m.forget(w)
}

// Reap stops workers idle for longer than the configured window. The system
// worker is never reaped: it serves the public shell.
func (m *WorkerManager) Reap(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		var idle []*Worker
		m.mu.Lock()
		for _, w := range m.bySub {
			if w.Subject == systemSubject {
				continue
			}
			w.mu.Lock()
			if w.ready && time.Since(w.lastSeen) > m.idle {
				idle = append(idle, w)
			}
			w.mu.Unlock()
		}
		m.mu.Unlock()
		for _, w := range idle {
			log.Printf("[workers] %s: idle for %s, stopping", w.Key, m.idle)
			m.Stop(w)
		}
	}
}

// Snapshot lists the workers for the attestation panel and for operators.
func (m *WorkerManager) Snapshot() []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]map[string]any, 0, len(m.bySub))
	for _, w := range m.bySub {
		w.mu.Lock()
		out = append(out, map[string]any{
			"key": w.Key, "system": w.Subject == systemSubject, "uid": w.UID, "port": w.Port,
			"ready": w.ready, "started": w.started, "last_seen": w.lastSeen,
		})
		w.mu.Unlock()
	}
	return out
}

// uidOfLoopbackPeer finds the uid owning the client end of a loopback TCP
// connection, from /proc/net/tcp{,6}: the kernel's own record of who holds
// the socket, which no process can forge for another. addr is the peer's
// address as the server saw it.
func uidOfLoopbackPeer(addr string) (int, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return 0, errors.New("not an ip")
	}
	for _, table := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		raw, err := os.ReadFile(table)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n")[1:] {
			f := strings.Fields(line)
			if len(f) < 8 {
				continue
			}
			// local_address is "HEXIP:HEXPORT"; we want the socket whose LOCAL
			// end is the peer's port (the client side of our connection).
			lp, ok := hexPort(f[1])
			if !ok || lp != port {
				continue
			}
			uid, err := strconv.Atoi(f[7])
			if err != nil {
				continue
			}
			return uid, nil
		}
	}
	return 0, errors.New("socket not found")
}

func hexPort(local string) (int, bool) {
	_, p, ok := strings.Cut(local, ":")
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseInt(p, 16, 32)
	if err != nil {
		return 0, false
	}
	return int(v), true
}
