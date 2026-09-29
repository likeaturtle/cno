// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

func TestLoadAllowlist(t *testing.T) {
	k1 := key.NewNode()
	k2 := key.NewNode()
	path := filepath.Join(t.TempDir(), "allowlist.txt")
	// k1 in the "nodekey:..." form tailcat prints; k2 as bare hex.
	content := "# comment\n\n" +
		k1.Public().String() + "\n" +
		k2.Public().String()[len("nodekey:"):] + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	al, err := loadAllowlist(path)
	if err != nil {
		t.Fatal(err)
	}
	if al.Len() != 2 {
		t.Fatalf("Len = %d, want 2", al.Len())
	}
	if !al.has(k1.Public()) || !al.has(k2.Public()) {
		t.Fatal("loaded keys missing from set")
	}
	other := key.NewNode().Public()
	if al.has(other) {
		t.Fatal("unexpected key allowed")
	}
}

func TestLoadAllowlistBadKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "allowlist.txt")
	if err := os.WriteFile(path, []byte("nodekey:not-hex\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAllowlist(path); err == nil {
		t.Fatal("want error for bad key, got nil")
	}
}

func TestAdmit(t *testing.T) {
	allowed := key.NewNode()
	denied := key.NewNode()
	path := filepath.Join(t.TempDir(), "allowlist.txt")
	if err := os.WriteFile(path, []byte(allowed.Public().String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	al, err := loadAllowlist(path)
	if err != nil {
		t.Fatal(err)
	}
	s := &server{path: path, list: al}

	post := func(k key.NodePublic) tailcfg.DERPAdmitClientResponse {
		t.Helper()
		body, err := json.Marshal(tailcfg.DERPAdmitClientRequest{
			NodePublic: k,
			Source:     netip.MustParseAddr("203.0.113.7"),
		})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/admit", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		s.handleAdmit(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
		}
		var res tailcfg.DERPAdmitClientResponse
		if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
			t.Fatal(err)
		}
		return res
	}

	if res := post(allowed.Public()); !res.Allow {
		t.Fatal("want allow, got deny")
	}
	if res := post(denied.Public()); res.Allow {
		t.Fatal("want deny, got allow")
	}

	// Revoking the key via a file rewrite takes effect on the next request.
	if err := os.WriteFile(path, []byte(denied.Public().String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Bump mtime explicitly: some filesystems timestamp at coarse granularity.
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}

	if res := post(allowed.Public()); res.Allow {
		t.Fatal("revoked key still allowed")
	}
	if res := post(denied.Public()); !res.Allow {
		t.Fatal("want allow after re-add, got deny")
	}
}

func TestAdmitEmptyListDenies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "allowlist.txt")
	if err := os.WriteFile(path, []byte("# nobody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	al, err := loadAllowlist(path)
	if err != nil {
		t.Fatal(err)
	}
	s := &server{path: path, list: al}

	body, _ := json.Marshal(tailcfg.DERPAdmitClientRequest{
		NodePublic: key.NewNode().Public(),
		Source:     netip.MustParseAddr("203.0.113.7"),
	})
	req := httptest.NewRequest("POST", "/admit", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleAdmit(rec, req)

	var res tailcfg.DERPAdmitClientResponse
	if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	if res.Allow {
		t.Fatal("empty allowlist must deny")
	}
}

func TestAdmitBadJSON(t *testing.T) {
	s := &server{path: filepath.Join(t.TempDir(), "missing")}
	req := httptest.NewRequest("POST", "/admit", bytes.NewReader([]byte("{")))
	rec := httptest.NewRecorder()
	s.handleAdmit(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
