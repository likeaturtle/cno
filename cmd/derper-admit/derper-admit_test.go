// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	now := time.Now()
	if ok, _ := al.lookup(k1.Public(), now); !ok {
		t.Fatal("loaded keys missing from set")
	}
	if ok, _ := al.lookup(k2.Public(), now); !ok {
		t.Fatal("loaded keys missing from set")
	}
	other := key.NewNode().Public()
	if ok, _ := al.lookup(other, now); ok {
		t.Fatal("unexpected key allowed")
	}
}

// TestLoadAllowlistFields covers the optional expires=/remark fields and
// inline comments.
func TestLoadAllowlistFields(t *testing.T) {
	k := key.NewNode()
	path := filepath.Join(t.TempDir(), "allowlist.txt")
	content := fmt.Sprintf("%s\texpires=2035-01-01T00:00:00Z  alice-laptop # 上海席位\n",
		k.Public().String())
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	al, err := loadAllowlist(path)
	if err != nil {
		t.Fatal(err)
	}
	ent, ok := al.keys[k.Public()]
	if !ok {
		t.Fatal("key not loaded")
	}
	if ent.remark != "alice-laptop" {
		t.Fatalf("remark = %q, want %q", ent.remark, "alice-laptop")
	}
	if want := time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC); !ent.expires.Equal(want) {
		t.Fatalf("expires = %v, want %v", ent.expires, want)
	}
	allowed, note := al.lookup(k.Public(), time.Now())
	if !allowed || note != "alice-laptop" {
		t.Fatalf("lookup = (%v, %q), want (true, %q)", allowed, note, "alice-laptop")
	}
	// An expired entry is denied, and says so.
	allowed, note = al.lookup(k.Public(), time.Date(2036, 1, 1, 0, 0, 0, 0, time.UTC))
	if allowed {
		t.Fatal("expired key must be denied")
	}
	if note != "expired 2035-01-01T00:00:00Z" {
		t.Fatalf("note = %q, want the expiry reason", note)
	}
}

func TestLoadAllowlistBadFields(t *testing.T) {
	k := key.NewNode().Public().String()
	for _, tc := range []struct {
		name string
		line string
	}{
		{"bad expires value", k + " expires=yesterday\n"},
		{"space after equals", k + " expires = 2035-01-01\n"},
		{"empty expires", k + " expires=\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "allowlist.txt")
			if err := os.WriteFile(path, []byte(tc.line), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadAllowlist(path); err == nil {
				t.Fatalf("want error for %q, got nil", tc.line)
			}
		})
	}
}

func TestParseExpiryBareDateIsEndOfDay(t *testing.T) {
	got, err := parseExpiry("2026-12-31")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 12, 31, 23, 59, 59, int(time.Second-time.Nanosecond), time.Local)
	if !got.Equal(want) {
		t.Fatalf("parseExpiry = %v, want %v", got, want)
	}
	if _, err := parseExpiry("2026-13-01"); err == nil {
		t.Fatal("want error for month 13")
	}
}

// TestExpiresZoneOffsetIsAbsolute checks the documented advice to write
// a +08:00 offset: the deadline is that exact instant, whatever time
// zone the process itself runs in.
func TestExpiresZoneOffsetIsAbsolute(t *testing.T) {
	got, err := parseExpiry("2027-10-02T23:59:59+08:00")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2027, 10, 2, 15, 59, 59, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("parseExpiry = %v, want the same instant as %v", got, want)
	}
	if _, off := got.Zone(); off != 8*3600 {
		t.Fatalf("zone offset = %d, want 28800 (the log echoes what you wrote)", off)
	}

	// A date-only value, by contrast, is process-zone relative, so it is
	// NOT a fixed instant; this is why the docs prefer the +08:00 form.
	dateOnly, err := parseExpiry("2027-10-02")
	if err != nil {
		t.Fatal(err)
	}
	if dateOnly.Equal(got) {
		t.Fatal("a bare date must differ from an explicit +08:00 timestamp unless the process zone is UTC+8 and the time is 23:59:59")
	}
}

// TestAllowlistReloadSameSizeSameTick is a regression test for the old
// mtime+size reload check: it forces identical mtimes and sizes for two
// different lists, which the old code treated as "unchanged".
func TestAllowlistReloadSameSizeSameTick(t *testing.T) {
	oldKey, newKey := key.NewNode().Public(), key.NewNode().Public()
	b1 := []byte(oldKey.String() + "\n")
	b2 := []byte(newKey.String() + "\n")
	if len(b1) != len(b2) {
		t.Fatalf("test needs equal-size lists, got %d and %d", len(b1), len(b2))
	}

	path := filepath.Join(t.TempDir(), "allowlist.txt")
	if err := os.WriteFile(path, b1, 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	al, err := loadAllowlist(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, b2, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}

	s := &server{path: path, list: al}
	now := time.Now()
	if ok, _, _ := s.allow(oldKey, now); ok {
		t.Fatal("revoked key still allowed after same-size same-tick rewrite")
	}
	if ok, _, err := s.allow(newKey, now); !ok || err != nil {
		t.Fatalf("newly added key = (%v, %v), want allowed with no error", ok, err)
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

	// Revoking the key via a file rewrite takes effect on the next
	// request, even though the rewritten file has the same size and is
	// written in the same mtime tick as the original.
	if err := os.WriteFile(path, []byte(denied.Public().String()+"\n"), 0o600); err != nil {
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
