package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"wls-timeline/internal/timeline"
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
func TestHTMLAndCLI(t *testing.T) {
	path := testLog(t, "server.log", serverHeader("2026-11-16T12:00:00Z", "Info", "Console", "BEA-000000", "</script><script>alert(1)</script> __TITLE__"))
	dir := t.TempDir()
	out := filepath.Join(dir, "report.html")
	css := testLog(t, "custom.css", "\ufeff:root { --bg: red } /* </style> */")
	var stdout, stderr bytes.Buffer
	args := []string{path, "--title", "<safe> __PAYLOAD__", "--stylesheet", css, "--timezone=Europe/Rome", "-o", out}
	if status := Run(args, &stdout, &stderr); status != 0 {
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
	var p struct {
		Events []timeline.Event `json:"events"`
		Zone   string           `json:"zone"`
	}
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
		if Run(args, &stdout, &stderr) == 0 {
			t.Fatalf("accepted invalid arguments: %v", args)
		}
	}
	if Run([]string{"--help"}, &stdout, &stderr) != 0 {
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
	if Run([]string{empty, "-o", preserved}, &stdout, &stderr) == 0 {
		t.Fatal("empty input accepted")
	}
	data, _ = os.ReadFile(preserved)
	if string(data) != "keep me" {
		t.Fatal("failed generation overwrote report")
	}
}
