// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

// derper-admit is a minimal admission controller for derper's
// --verify-client-url flag. It allowlists DERP clients by node public
// key so that knowing a self-hosted DERP's address is not enough to
// use it as a free relay.
//
// Each accepted key is one line in the allowlist file: either the
// "nodekey:..." form printed by "tailcat genkey" / "tailcat genkey
// --client", or its bare hex. A line may be followed by optional
// fields:
//
//	nodekey:abcd...  expires=2026-12-31  alice-laptop  # a remark
//
// "expires=" takes an RFC 3339 timestamp or a bare YYYY-MM-DD date
// (interpreted in the server's local time zone, through the end of
// that day). Any other field is free-form remark text, echoed in the
// log for that key. Everything from a "#" to the end of the line is a
// comment, and blank or comment-only lines are ignored.
//
// The file is read and hashed on each admission check (it is small,
// and checks happen at connection setup), and re-parsed only when its
// contents change, so adding or revoking a key does not restart this
// process (or derper). An empty allowlist admits no one; so does an
// expired entry. Write the file atomically (temporary file, then
// rename it into place) so a concurrent check never reads a
// half-written list; a parse error keeps the previous list in use. If
// the same key appears on more than one line, the last line wins.
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
	"crypto/sha256"
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
	allowlist := flag.String("allowlist", "", "path to the allowlist file: one node public key per line, optionally followed by expires= and a remark (required)")
	flag.Parse()

	if *allowlist == "" {
		log.Fatal("derper-admit: -allowlist is required")
	}

	al, err := loadAllowlist(*allowlist)
	if err != nil {
		log.Fatalf("derper-admit: %v", err)
	}
	log.Printf("derper-admit: loaded %d key(s) from %s (%d expired)", al.Len(), *allowlist, al.expiredLen(time.Now()))

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

	allow, note, err := s.allow(req.NodePublic, time.Now())
	if err != nil {
		log.Printf("derper-admit: reload %s: %v (keeping previous list)", s.path, err)
	}

	src := req.Source.String()
	if !req.Source.IsValid() {
		src = "?"
	}
	if note != "" {
		note = " (" + note + ")"
	}
	log.Printf("derper-admit: %s %s from %s%s", decision(allow), req.NodePublic, src, note)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tailcfg.DERPAdmitClientResponse{Allow: allow})
}

func decision(allow bool) string {
	if allow {
		return "allow"
	}
	return "deny "
}

// allow reports whether k is permitted at now, reloading the file if
// its contents have changed on disk.
func (s *server) allow(k key.NodePublic, now time.Time) (bool, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	list, err := s.list.reloadIfChanged(s.path)
	if err != nil {
		// Fail closed on reload errors: the previous list stays in use,
		// and if there is no previous list the zero value denies all.
		ok, note := s.list.lookup(k, now)
		return ok, note, err
	}
	s.list = list
	ok, note := list.lookup(k, now)
	return ok, note, nil
}

// allowlist is a set of permitted node public keys with optional
// per-key expiry, plus the hash of the file contents it was parsed
// from so reloadIfChanged can skip unchanged files.
type allowlist struct {
	keys map[key.NodePublic]allowEntry
	sum  [sha256.Size]byte
}

// allowEntry is the optional metadata attached to an allowlist line.
type allowEntry struct {
	expires time.Time // zero means the key never expires
	remark  string
}

func (a allowlist) Len() int { return len(a.keys) }

// expiredLen reports how many entries are past their expiry at now.
func (a allowlist) expiredLen(now time.Time) int {
	n := 0
	for _, ent := range a.keys {
		if !ent.expires.IsZero() && !now.Before(ent.expires) {
			n++
		}
	}
	return n
}

// lookup reports whether k is permitted at now, along with a short
// note for the log: the entry's remark when the key is allowed and one
// is set, or the reason when it is refused. An expired key is denied
// as if it were absent.
func (a allowlist) lookup(k key.NodePublic, now time.Time) (allow bool, note string) {
	ent, ok := a.keys[k]
	if !ok {
		return false, ""
	}
	if !ent.expires.IsZero() && !now.Before(ent.expires) {
		return false, "expired " + ent.expires.Format(time.RFC3339)
	}
	return true, ent.remark
}

// reloadIfChanged returns the current contents of path, or a itself if
// the file's contents are unchanged since a was loaded.
//
// Comparing a content hash rather than mtime+size means a revoke that
// rewrites the file to the same size within the same mtime tick is
// still picked up. Allowlist files are small and admission checks
// happen at connection setup, so hashing on each check is cheap.
func (a allowlist) reloadIfChanged(path string) (allowlist, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return a, err
	}
	if a.keys != nil && sha256.Sum256(data) == a.sum {
		return a, nil
	}
	return parseAllowlist(path, data)
}

func loadAllowlist(path string) (allowlist, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return allowlist{}, err
	}
	return parseAllowlist(path, data)
}

// parseAllowlist parses the file format described in the package
// comment. It is all-or-nothing: a single bad line rejects the whole
// file, so the caller keeps using the previous list.
func parseAllowlist(path string, data []byte) (allowlist, error) {
	keys := map[key.NodePublic]allowEntry{}
	for lineNo, line := range strings.Split(string(data), "\n") {
		// Anything from "#" to the end of the line is a comment; a key
		// never contains one.
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		keyText := fields[0]
		// Accept "nodekey:<hex>" as printed by tailcat genkey, or bare hex.
		if !strings.Contains(keyText, ":") {
			keyText = "nodekey:" + keyText
		}
		var k key.NodePublic
		if err := k.UnmarshalText([]byte(keyText)); err != nil {
			return allowlist{}, fmt.Errorf("%s:%d: %v", path, lineNo+1, err)
		}
		if k.IsZero() {
			return allowlist{}, fmt.Errorf("%s:%d: zero key", path, lineNo+1)
		}
		var ent allowEntry
		var remark []string
		for _, f := range fields[1:] {
			v, isExpires := strings.CutPrefix(f, "expires=")
			if !isExpires {
				if f == "expires" {
					return allowlist{}, fmt.Errorf("%s:%d: write expires=<time> with no space after '='", path, lineNo+1)
				}
				remark = append(remark, f)
				continue
			}
			t, err := parseExpiry(v)
			if err != nil {
				return allowlist{}, fmt.Errorf("%s:%d: %v", path, lineNo+1, err)
			}
			ent.expires = t
		}
		ent.remark = strings.Join(remark, " ")
		keys[k] = ent
	}
	return allowlist{keys: keys, sum: sha256.Sum256(data)}, nil
}

// expiryLayouts are the accepted expires= formats, tried in order.
// Times are interpreted in the server's local time zone.
var expiryLayouts = []string{
	"2006-01-02",
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04",
}

// parseExpiry parses an expires= value. A bare date means the end of
// that day in local time, so "expires=2026-12-31" stays valid through
// December 31.
func parseExpiry(s string) (time.Time, error) {
	for _, layout := range expiryLayouts {
		t, err := time.ParseInLocation(layout, s, time.Local)
		if err != nil {
			continue
		}
		if layout == "2006-01-02" {
			t = t.Add(24*time.Hour - time.Nanosecond)
		}
		return t, nil
	}
	return time.Time{}, fmt.Errorf("bad expires value %q (want e.g. 2026-12-31 or 2026-12-31T15:04:05Z)", s)
}
