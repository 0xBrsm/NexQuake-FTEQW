package orch

import (
	"bytes"
	"testing"
)

func TestBuildGetInfoQuery(t *testing.T) {
	req := buildGetInfoQuery()
	want := []byte("\xFF\xFF\xFF\xFFgetinfo poll\n")
	if !bytes.Equal(req, want) {
		t.Fatalf("getinfo query = %q, want %q", req, want)
	}
	// Must be a fresh copy so callers cannot mutate the shared template.
	req[0] = 0x00
	if buildGetInfoQuery()[0] != 0xFF {
		t.Fatalf("buildGetInfoQuery() returned aliased slice")
	}
}

func TestParseInfoResponse(t *testing.T) {
	// FTE infoResponse: connectionless header + "infoResponse\n" + infostring.
	packet := []byte("\xFF\xFF\xFF\xFFinfoResponse\n\\hostname\\fragfest\\mapname\\dm6\\modname\\ctf\\clients\\12\\sv_maxclients\\16\\protocol\\3\\")

	info, ok := parseInfoResponse(packet)
	if !ok {
		t.Fatalf("parseInfoResponse() returned ok=false for valid packet")
	}
	if info.Hostname != "fragfest" || info.MapName != "dm6" {
		t.Fatalf("unexpected host/map: got %q/%q", info.Hostname, info.MapName)
	}
	if info.GameDir != "ctf" {
		t.Fatalf("unexpected gamedir: got %q, want ctf", info.GameDir)
	}
	if info.Players != 12 || info.MaxPlayers != 16 {
		t.Fatalf("unexpected counters: got players=%d max=%d", info.Players, info.MaxPlayers)
	}
}

func TestParseInfoResponse_FieldFallbacks(t *testing.T) {
	// No hostname/modname/sv_maxclients; exercise sv_hostname, *gamedir, maxclients.
	packet := []byte("\xFF\xFF\xFF\xFFinfoResponse\n\\sv_hostname\\alpha\\mapname\\start\\*gamedir\\hipnotic\\clients\\3\\maxclients\\8\\")

	info, ok := parseInfoResponse(packet)
	if !ok {
		t.Fatalf("parseInfoResponse() returned ok=false for valid packet")
	}
	if info.Hostname != "alpha" {
		t.Fatalf("hostname fallback failed: got %q, want alpha", info.Hostname)
	}
	if info.GameDir != "hipnotic" {
		t.Fatalf("gamedir fallback failed: got %q, want hipnotic", info.GameDir)
	}
	if info.Players != 3 || info.MaxPlayers != 8 {
		t.Fatalf("counter fallback failed: got players=%d max=%d", info.Players, info.MaxPlayers)
	}
}

func TestParseInfoResponse_RejectsNonInfoResponse(t *testing.T) {
	cases := [][]byte{
		[]byte("\xFF\xFF\xFF\xFFgetinfo poll\n"),               // a query, not a response
		[]byte("\xFF\xFF\xFF\xFFprint\nsomething\n"),           // wrong command
		[]byte("infoResponse\n\\hostname\\x\\"),                // missing header
		[]byte("\xFF\xFF\xFF\xFFinfoResponse"),                 // no infostring
		[]byte("\xFF\xFF\xFF\xFFinfoResponse\nno-backslash"),   // no infostring start
	}
	for i, c := range cases {
		if _, ok := parseInfoResponse(c); ok {
			t.Fatalf("case %d: parseInfoResponse() accepted invalid packet %q", i, c)
		}
	}
}

func TestBuildCCREPServerListEncodesU16Fields(t *testing.T) {
	packet, count := buildCCREPServerList([]serverListEntry{{
		ListenPort: 26000,
		Hostname:   "fragfest",
		MapName:    "dm6",
		GameDir:    "id1",
		Users:      873,
		MaxUsers:   10000,
		Instances:  100,
	}})
	if count != 1 {
		t.Fatalf("entry count = %d, want 1", count)
	}
	if len(packet) < 8 {
		t.Fatalf("packet too small: %d", len(packet))
	}
	if packet[5] != 1 {
		t.Fatalf("entry count byte = %d, want 1", packet[5])
	}

	i := 6
	portText, next, ok := readCString(packet, i)
	if !ok || portText != "26000" {
		t.Fatalf("port text = %q, want 26000", portText)
	}
	i = next
	hostname, next, ok := readCString(packet, i)
	if !ok || hostname != "fragfest" {
		t.Fatalf("hostname = %q, want fragfest", hostname)
	}
	i = next
	mapName, next, ok := readCString(packet, i)
	if !ok || mapName != "dm6" {
		t.Fatalf("map = %q, want dm6", mapName)
	}
	i = next
	gameDir, next, ok := readCString(packet, i)
	if !ok || gameDir != "id1" {
		t.Fatalf("gamedir = %q, want id1", gameDir)
	}
	i = next

	if i+7 > len(packet) {
		t.Fatalf("missing numeric tail")
	}
	users := uint16(packet[i]) | uint16(packet[i+1])<<8
	maxUsers := uint16(packet[i+2]) | uint16(packet[i+3])<<8
	instances := uint16(packet[i+4]) | uint16(packet[i+5])<<8

	if users != 873 {
		t.Fatalf("users = %d, want 873", users)
	}
	if maxUsers != 10000 {
		t.Fatalf("maxUsers = %d, want 10000", maxUsers)
	}
	if instances != 100 {
		t.Fatalf("instances = %d, want 100", instances)
	}
}

func TestBuildCCREPServerListPreservesZeroInstances(t *testing.T) {
	packet, count := buildCCREPServerList([]serverListEntry{{
		ListenPort: 26000,
		Hostname:   "fragfest",
		MapName:    "dm6",
		GameDir:    "id1",
		Users:      1,
		MaxUsers:   16,
		Instances:  0,
	}})
	if count != 1 {
		t.Fatalf("entry count = %d, want 1", count)
	}
	i := 6
	for step := 0; step < 4; step++ {
		_, next, ok := readCString(packet, i)
		if !ok {
			t.Fatalf("missing cstring %d", step)
		}
		i = next
	}
	if i+7 > len(packet) {
		t.Fatalf("missing numeric fields")
	}
	instances := uint16(packet[i+4]) | uint16(packet[i+5])<<8
	if instances != 0 {
		t.Fatalf("instances = %d, want 0", instances)
	}
}
