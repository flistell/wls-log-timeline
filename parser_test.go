package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func testLog(t *testing.T, name, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func serverHeader(date, severity, subsystem, code, message string) string {
	return fmt.Sprintf("####<%s> <%s> <%s> <host> <server> <thread> <<anonymous>> <> <rid> <%s> <%s>\n", date, severity, subsystem, code, message)
}
func TestTimestamps(t *testing.T) {
	rome, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, date, epoch string
		assume            *time.Location
		want              string
		invalid           bool
	}{
		{name: "epoch wins", date: "invalid", epoch: "<1794783785672>", want: "2026-11-15T23:03:05.672Z"},
		{name: "ISO", date: "2026-11-16T12:34:56.123+02:00", want: "2026-11-16T10:34:56.123Z"},
		{name: "UTC", date: "2026-11-16T12:34:56Z", want: "2026-11-16T12:34:56Z"},
		{name: "English", date: "Nov 16, 2026 12:03:05.672 AM CET", want: "2026-11-15T23:03:05.672Z"},
		{name: "numeric", date: "Nov 16, 2026 12:03:05 PM +0230", want: "2026-11-16T09:33:05Z"},
		{name: "assumed", date: "Nov 16, 2026 12:03:05 PM UNKNOWN", assume: rome, want: "2026-11-16T11:03:05Z"},
		{name: "naive ISO", date: "2026-11-16 12:03:05", assume: rome, want: "2026-11-16T11:03:05Z"},
		{name: "DST repeat", date: "2026-10-25T02:30:00", assume: rome, want: "2026-10-25T00:30:00Z"},
		{name: "DST gap", date: "2026-03-29T02:30:00", assume: rome, want: "2026-03-29T01:30:00Z"},
		{name: "unknown zone", date: "Nov 16, 2026 12:03:05 AM XYZ", invalid: true},
		{name: "missing zone", date: "2026-11-16T12:03:05", invalid: true},
		{name: "invalid day", date: "Feb 30, 2026 12:03:05 AM UTC", invalid: true},
		{name: "invalid clock", date: "Nov 16, 2026 13:03:05 AM UTC", invalid: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseTimestamp("<"+c.date+"> "+c.epoch+" <BEA-000000> <message>", c.date, c.assume)
			if c.invalid {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want, err := time.Parse(time.RFC3339Nano, c.want)
			if err != nil {
				t.Fatal(err)
			}
			if got != want.UnixMilli() {
				t.Fatalf("got %s, want %s", time.UnixMilli(got).UTC(), want)
			}
		})
	}
}
func TestMultilineAndMerge(t *testing.T) {
	h := serverHeader("2026-11-16T12:00:00Z", "Error", "WebLogicServer", "BEA-999999", "Server state changed to RUNNING")
	path := testLog(t, "server.log", "preamble\n"+h+"  exception BEA1-abc123\n"+serverHeader("unsupported", "Info", "JDBC", "BEA-000627", "omitted"))
	second := testLog(t, "stdout.log", "<2026-11-16T12:00:00Z> <Notice> <Console> <123456> <console>\n")
	events, warnings, total, err := parseLogs([]string{path, second, path}, time.UTC, 30, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || len(warnings) != 4 || total != 9 {
		t.Fatalf("events=%d warnings=%v total=%d", len(events), warnings, total)
	}
	if events[0].ID != "F1-L2" || events[1].ID != "F2-L1" || events[2].ID != "F3-L2" {
		t.Fatalf("unstable ordering: %+v", events)
	}
	e := events[0]
	if e.Category != "WebLogic Server Lifecycle" || e.EndLine != 3 || !e.Truncated || len([]rune(e.Detail)) != 30 || !reflect.DeepEqual(e.XIDs, []string{"BEA1-abc123"}) {
		t.Fatalf("bad event: %+v", e)
	}
	if events[1].Host != "" || events[1].Server != "" || events[1].Thread != "" {
		t.Fatal("stdout should have unspecified host/server/thread")
	}
}
func TestCoherenceEvidenceBeyondExcerpt(t *testing.T) {
	text := serverHeader("2026-11-16T12:00:00Z", "Warning", "Coherence", "BEA-000000", "(thread=Worker, member=n/a): Restarting NamedCache: Payments") +
		"  tmb://host:1234.567 Address=[::1]:9999 Service=Payments cluster \"Production\" Member(Id=7 member:node1 BEA1-xid9\n" +
		"  Detected hard timeout\n"
	path := testLog(t, "coherence.log", text)
	events, _, _, err := parseLogs([]string{path}, time.UTC, 10, nil, "coherence")
	if err != nil {
		t.Fatal(err)
	}
	c := events[0].Coherence
	if c.EventType != "restarting cache" || c.LocalMember != "n/a" || c.Thread != "Worker" || !reflect.DeepEqual(c.Endpoints, []string{"[::1]:9999", "host:1234"}) || !reflect.DeepEqual(c.Services, []string{"Payments"}) || !reflect.DeepEqual(c.Clusters, []string{"Production"}) || !reflect.DeepEqual(c.Members, []string{"7"}) || !reflect.DeepEqual(events[0].XIDs, []string{"BEA1-xid9"}) {
		t.Fatalf("lost metadata: %+v", c)
	}
	for _, tc := range []struct{ message, want string }{
		{"stopping cluster service after guardian timeout; member left Cluster", "stopping cluster"},
		{"Restarting cluster", "Restarting cluster"},
		{"A worker thread has been executing task: PutRequest and appears to be stuck", "Worker STUCK"},
		{"Connection established with peer", "Connection established"},
		{"unknown message", "Other Coherence"},
	} {
		if got := newMetadata().classify(tc.message).EventType; got != tc.want {
			t.Errorf("%s: got %s", tc.message, got)
		}
	}
}
func TestGzipLongUnicodeAndCRLF(t *testing.T) {
	text := serverHeader("2026-11-16T12:00:00Z", "Info", "JDBC", "BEA-000627", "é世界🙂") + strings.Repeat("界", 100000) + " BEA1-late\n"
	text = strings.ReplaceAll(text, "\n", "\r\n")
	plain := testLog(t, "server.log", text)
	compressed := filepath.Join(t.TempDir(), "server.log.gz")
	f, err := os.Create(compressed)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	if _, err = gz.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	if err = gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	a, _, _, err := parseLog(plain, time.UTC, 201, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	b, _, _, err := parseLog(compressed, time.UTC, 201, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) || len([]rune(a[0].Detail)) != 201 || len(a[0].XIDs) != 1 || a[0].OriginalChars != len([]rune(strings.ReplaceAll(text, "\r\n", "\n"))) {
		t.Fatal("gzip/unicode/long-line parsing differs")
	}
}

func TestCRLineEndings(t *testing.T) {
	text := serverHeader("2026-11-16T12:00:00Z", "Info", "Console", "BEA-000000", "first") + "continuation\n" + serverHeader("2026-11-16T12:01:00Z", "Info", "Console", "BEA-000000", "second")
	lf := testLog(t, "lf.log", text)
	cr := testLog(t, "cr.log", strings.ReplaceAll(text, "\n", "\r"))
	a, _, _, err := parseLog(lf, time.UTC, 12000, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	b, _, lines, err := parseLog(cr, time.UTC, 12000, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if lines != 3 || !reflect.DeepEqual(a, b) {
		t.Fatal("CR line endings changed records")
	}
}
func TestHTMLAndCLI(t *testing.T) {
	path := testLog(t, "server.log", serverHeader("2026-11-16T12:00:00Z", "Info", "Console", "BEA-000000", "</script><script>alert(1)</script> __TITLE__"))
	dir := t.TempDir()
	out := filepath.Join(dir, "report.html")
	css := testLog(t, "custom.css", "\ufeff:root { --bg: red } /* </style> */")
	var stdout, stderr bytes.Buffer
	args := []string{path, "--title", "<safe> __PAYLOAD__", "--stylesheet", css, "--timezone=Europe/Rome", "-o", out}
	if status := run(args, &stdout, &stderr); status != 0 {
		t.Fatalf("status %d: %s", status, stderr.String())
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)
	if strings.Contains(page, "<title><safe>") || strings.Contains(page, "<script>alert(1)") || !strings.Contains(page, "data:text/css;charset=utf-8;base64,") {
		t.Fatal("unsafe or missing embedded content")
	}
	match := regexp.MustCompile(`(?s)<script type="application/json" id="data">(.*?)</script>`).FindStringSubmatch(page)
	var p payload
	if len(match) != 2 {
		t.Fatal("missing payload")
	}
	if err := json.Unmarshal([]byte(match[1]), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Events) != 1 || !strings.Contains(p.Events[0].Summary, "__TITLE__") || p.Zone != "Europe/Rome" {
		t.Fatal("payload changed")
	}
	for _, args := range [][]string{{path, "-o", path}, {path, "--stylesheet", css, "-o", css}, {path, "--timezone", "Not/AZone", "-o", out}, {path, "--max-detail-chars", "0"}, {path, "--detailed-events", "unknown"}, {"--bogus"}, {}} {
		if run(args, &stdout, &stderr) == 0 {
			t.Fatalf("accepted invalid arguments: %v", args)
		}
	}
	if run([]string{"--help"}, &stdout, &stderr) != 0 {
		t.Fatal("help failed")
	}
	alias := filepath.Join(dir, "hardlink.log")
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	if protectInputs(alias, []string{path}) == nil {
		t.Fatal("hardlink overwrite allowed")
	}
	symlink := filepath.Join(dir, "symlink.log")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatal(err)
	}
	if protectInputs(symlink, []string{path}) == nil {
		t.Fatal("symlink overwrite allowed")
	}
	empty := testLog(t, "empty.log", "no records\n")
	preserved := testLog(t, "existing.html", "keep me")
	if run([]string{empty, "-o", preserved}, &stdout, &stderr) == 0 {
		t.Fatal("empty input accepted")
	}
	data, _ = os.ReadFile(preserved)
	if string(data) != "keep me" {
		t.Fatal("failed generation overwrote report")
	}
}

func TestSampleLogs(t *testing.T) {
	var paths []string
	err := filepath.WalkDir("sample-logs", func(path string, entry os.DirEntry, err error) error {
		if os.IsNotExist(err) && path == "sample-logs" {
			return filepath.SkipDir
		}
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Skip("sample-logs not present")
	}
	zone, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		t.Fatal(err)
	}
	events, warnings, lines, err := parseLogs(paths, zone, 12000, nil, "coherence")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || lines == 0 {
		t.Fatal("no sample data parsed")
	}
	counts := map[string]int{}
	seen := map[string]bool{}
	for i, e := range events {
		if seen[e.ID] {
			t.Fatalf("duplicate ID %s", e.ID)
		}
		seen[e.ID] = true
		if i > 0 && events[i-1].Epoch > e.Epoch {
			t.Fatal("events not sorted")
		}
		if e.Source == "" || e.EndLine < e.Line {
			t.Fatal("missing source location")
		}
		if e.Coherence != nil {
			counts[e.EventType]++
		}
	}
	if len(counts) < 5 {
		t.Fatalf("insufficient Coherence coverage: %v", counts)
	}
	t.Logf("%d logs, %d events, %d lines, %d warnings; Coherence types: %v", len(paths), len(events), lines, len(warnings), counts)
}
