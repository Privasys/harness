// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

// What the assistant calls the person it works for, and their standing
// instructions.
//
// Preferences, not identity claims: the identity provider puts the name a
// wallet discloses in the sign-in token, but only on a sign-in the wallet took
// part in, and the person may want to be addressed otherwise; their standing
// instructions (how they like to work, their language) are theirs alone. The
// setting is the user's own document (profile.json, beside their policy and
// workspaces.json in their Drive folder), set from the user panel and seeded
// from the sign-in name. Two readers: the sidebar (over the sealed session)
// and the agent, through a file in the worker's own directory that
// app/privasys-profile.mjs reads on every prompt assembly, so a change applies
// to the next turn without a restart.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/Privasys/attested-harness/proxy/internal/policy"
)

const (
	profileFile = "profile.json"
	// workerProfileFile is the copy in a worker's directory the agent reads.
	workerProfileFile = "privasys-profile.json"
	maxProfileName    = 80
	// maxProfileInstructions bounds the standing instructions, in characters:
	// a couple of pages. They go into every prompt, so they are kept short.
	maxProfileInstructions = 8000
	maxProfileBytes        = 64 << 10
)

// profiles is the process-wide store, set at startup (main.go); nil off the
// platform.
var profiles *profileStore

type profileDoc struct {
	// Name is what the assistant calls the user; "" when unset.
	Name string `json:"name"`
	// Instructions are the user's standing instructions to the assistant,
	// Markdown; "" when unset.
	Instructions string `json:"instructions,omitempty"`
}

// profileUpdate is what the user panel sends: only the fields it names change.
type profileUpdate struct {
	Name         *string `json:"name"`
	Instructions *string `json:"instructions"`
}

type profileStore struct {
	docs  namedDocs
	mu    sync.Mutex
	bySub map[string]profileDoc
}

func newProfileStore(docs namedDocs) *profileStore {
	return &profileStore{docs: docs, bySub: map[string]profileDoc{}}
}

// Get returns the user's profile, loading it once. Missing or unreadable
// reads as empty.
func (s *profileStore) Get(sub string) profileDoc {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d, ok := s.bySub[sub]; ok {
		return d
	}
	var d profileDoc
	if s.docs != nil && sub != "" {
		if raw, found, err := s.docs.LoadNamed(sub, profileFile); err == nil && found {
			var loaded profileDoc
			if json.Unmarshal(raw, &loaded) == nil {
				d = loaded
			}
		} else if err != nil {
			log.Printf("[profile] %.8s…: load: %v", sub, err)
		}
	}
	s.bySub[sub] = d
	return d
}

// cleanProfileName trims a name and refuses what cannot be a name: empty is
// allowed (unset), control characters and overlong text are not, since the
// value goes into the agent's instructions.
func cleanProfileName(name string) (string, error) {
	if !utf8.ValidString(name) {
		return "", errors.New("the name is not valid text")
	}
	// Checked before spaces are tidied, which would turn a line break into
	// a space and let a second line through.
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune("{}<>`", r) {
			return "", errors.New("the name contains characters a name does not use")
		}
	}
	name = strings.Join(strings.Fields(name), " ")
	if utf8.RuneCountInString(name) > maxProfileName {
		return "", errors.New("the name is too long")
	}
	return name, nil
}

// cleanProfileInstructions keeps the text as written (line breaks and tabs
// are its structure), refuses the other control characters, and bounds it.
func cleanProfileInstructions(text string) (string, error) {
	if !utf8.ValidString(text) {
		return "", errors.New("the instructions are not valid text")
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	for _, r := range text {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f {
			return "", errors.New("the instructions contain characters text does not use")
		}
	}
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) > maxProfileInstructions {
		return "", fmt.Errorf("the instructions are too long (at most %d characters)", maxProfileInstructions)
	}
	return text, nil
}

// Set applies the fields the update names, writes the profile through to the
// user's Drive, and hands it to their running worker. persisted is false
// when no Drive is connected: the change applies now and is lost at the next
// restart.
func (s *profileStore) Set(sub string, u profileUpdate) (persisted bool, err error) {
	d := s.Get(sub)
	if u.Name != nil {
		if d.Name, err = cleanProfileName(*u.Name); err != nil {
			return false, err
		}
	}
	if u.Instructions != nil {
		if d.Instructions, err = cleanProfileInstructions(*u.Instructions); err != nil {
			return false, err
		}
	}
	s.mu.Lock()
	s.bySub[sub] = d
	s.mu.Unlock()
	if w := workerOf(sub); w != nil {
		if err := writeWorkerProfile(w, d); err != nil {
			log.Printf("[profile] %.8s…: worker copy: %v", sub, err)
		}
	}
	if s.docs == nil {
		return false, nil
	}
	raw, _ := json.MarshalIndent(d, "", "  ")
	if err := s.docs.SaveNamed(sub, profileFile, raw); err != nil {
		if errors.Is(err, policy.ErrNotPersisted) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// workerOf finds the user's running worker; nil in the single-worker layout
// or when none runs.
var workerOf = func(string) *Worker { return nil }

// writeWorkerProfile puts the profile where the worker's agent reads it,
// owned by the worker's user.
func writeWorkerProfile(w *Worker, d profileDoc) error {
	path := filepath.Join(w.Dir, workerProfileFile)
	raw, _ := json.Marshal(d)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return err
	}
	if w.UID != 0 {
		return os.Chown(path, w.UID, w.UID)
	}
	return nil
}

// profilePatch composes the profile plugin for a worker, naming the file
// writeWorkerProfile keeps current.
func profilePatch(w *Worker) string {
	cfg, _ := json.Marshal(map[string]string{"file": filepath.Join(w.Dir, workerProfileFile)})
	return fmt.Sprintf("    - id: privasys-profile\n      name: %q\n      config: %s\n",
		envOr("HARNESS_PROFILE_PLUGIN", "/dsh/apps/cli/config/privasys/privasys-profile.mjs"), cfg)
}

func profileResponse(d profileDoc) map[string]any {
	return map[string]any{"name": d.Name, "instructions": d.Instructions}
}

func registerProfileAPI(mux *http.ServeMux, ps *profileStore) {
	mux.HandleFunc("GET /privasys/profile", func(w http.ResponseWriter, r *http.Request) {
		sub := r.Header.Get("X-Privasys-Sub")
		if sub == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "a signed-in session is required"})
			return
		}
		writeJSON(w, http.StatusOK, profileResponse(ps.Get(sub)))
	})
	mux.HandleFunc("PUT /privasys/profile", func(w http.ResponseWriter, r *http.Request) {
		sub := r.Header.Get("X-Privasys-Sub")
		if sub == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "a signed-in session is required"})
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxProfileBytes))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		var u profileUpdate
		if err := json.Unmarshal(raw, &u); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be {name?, instructions?}"})
			return
		}
		persisted, err := ps.Set(sub, u)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		out := profileResponse(ps.Get(sub))
		out["persisted"] = persisted
		if !persisted {
			out["notice"] = "this applies now but is not saved anywhere: connect your Drive to keep it"
		}
		writeJSON(w, http.StatusOK, out)
	})
}
