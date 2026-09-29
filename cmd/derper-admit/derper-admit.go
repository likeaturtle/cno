// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

// derper-admit is a minimal admission controller for derper's
// --verify-client-url flag. It allowlists DERP clients by node public
// key so that knowing a self-hosted DERP's address is not enough to
// use it as a free relay.
//
// Each accepted key is one line in the allowlist file: either the
// "nodekey:..." form printed by "tailcat genkey" / "tailcat genkey
// --client", or its bare hex. Blank lines and # comments are ignored.
// The file is re-read when it changes, so adding or revoking a key
// does not restart this process (or derper). An empty allowlist admits
// no one.
//
// Start it next to derper and point derper at it:
//
//	derper-admit -listen 127.0.0.1:9090 -allowlist ./allowlist.txt
//	derper --verify-client-url=http://127.0.0.1:9090/admit \
//	       --verify-client-url-fail-open=false
//
// Keep -listen on loopback (or otherwise unreachable from the public
// internet): anyone who can POST {"Allow":true} to this port can
// authorize themselves to your DERP.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:9090", "address to listen on for admission requests; keep this off the public internet")
	allowlist := flag.String("allowlist", "", "path to the allowlist file of node public keys, one per line (required)")
	flag.Parse()

	if *allowlist == "" {
		log.Fatal("derper-admit: -allowlist is required")
	}

	al, err := loadAllowlist(*allowlist)
	if err != nil {
		log.Fatalf("derper-admit: %v", err)
	}
	log.Printf("derper-admit: loaded %d allowed key(s) from %s", al.Len(), *allowlist)

	srv := &server{path: *allowlist, list: al}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admit", srv.handleAdmit)
	mux.HandleFunc("POST /", srv.handleAdmit)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("derper-admit: listen: %v", err)
	}
	log.Printf("derper-admit: listening on http://%s/admit", ln.Addr())
	log.Fatal(http.Serve(ln, mux))
}

// server answers derper admission requests from a hot-reloaded allowlist.
type server struct {
	path string
	mu   sync.Mutex
	list allowlist
}

func (s *server) handleAdmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var req tailcfg.DERPAdmitClientRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.NodePublic.IsZero() {
		http.Error(w, "missing NodePublic", http.StatusBadRequest)
		return
	}

	allow, err := s.allow(req.NodePublic)
	if err != nil {
		log.Printf("derper-admit: reload %s: %v (keeping previous list)", s.path, err)
	}

	src := req.Source.String()
	if !req.Source.IsValid() {
		src = "?"
	}
	log.Printf("derper-admit: %s %s from %s", decision(allow), req.NodePublic, src)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tailcfg.DERPAdmitClientResponse{Allow: allow})
}

func decision(allow bool) string {
	if allow {
		return "allow"
	}
	return "deny "
}

// allow reports whether k is on the allowlist, reloading the file if it
// has changed on disk.
func (s *server) allow(k key.NodePublic) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	list, err := s.list.reloadIfChanged(s.path)
	if err != nil {
		// Fail closed on reload errors: the previous list stays in use,
		// and if there is no previous list the zero value denies all.
		return s.list.has(k), err
	}
	s.list = list
	return s.list.has(k), nil
}

// allowlist is a set of permitted node public keys, with the file
// metadata it was loaded from so reloadIfChanged can skip unchanged files.
type allowlist struct {
	keys map[key.NodePublic]bool
	mod  time.Time
	size int64
}

func (a allowlist) Len() int { return len(a.keys) }

func (a allowlist) has(k key.NodePublic) bool { return a.keys[k] }

// reloadIfChanged returns the current contents of path, or a itself if
// the file is unchanged since a was loaded.
func (a allowlist) reloadIfChanged(path string) (allowlist, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return a, err
	}
	if a.keys != nil && fi.ModTime().Equal(a.mod) && fi.Size() == a.size {
		return a, nil
	}
	return loadAllowlist(path)
}

func loadAllowlist(path string) (allowlist, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return allowlist{}, err
	}
	keys := map[key.NodePublic]bool{}
	for lineNo, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Accept "nodekey:<hex>" as printed by tailcat genkey, or bare hex.
		if !strings.Contains(line, ":") {
			line = "nodekey:" + line
		}
		var k key.NodePublic
		if err := k.UnmarshalText([]byte(line)); err != nil {
			return allowlist{}, fmt.Errorf("%s:%d: %v", path, lineNo+1, err)
		}
		if k.IsZero() {
			return allowlist{}, fmt.Errorf("%s:%d: zero key", path, lineNo+1)
		}
		keys[k] = true
	}
	fi, err := os.Stat(path)
	if err != nil {
		return allowlist{}, err
	}
	return allowlist{keys: keys, mod: fi.ModTime(), size: fi.Size()}, nil
}
