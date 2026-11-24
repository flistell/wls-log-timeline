package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestServeArguments(t *testing.T) {
	for _, args := range [][]string{
		{"--serve", "server.log"},
		{"server.log", "--serve"},
		{"first.log", "--serve", "second.log", "--listen", "127.0.0.1:0"},
	} {
		var stderr bytes.Buffer
		o, err := parseArgs(args, &stderr)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !o.serve || len(o.paths) == 0 {
			t.Fatalf("serve consumed input files: %+v", o)
		}
	}
	var stderr bytes.Buffer
	o, err := parseArgs([]string{"server.log"}, &stderr)
	if err != nil || o.serve || o.listen != "127.0.0.1:8080" {
		t.Fatalf("wrong defaults: %+v, %v", o, err)
	}
	o, err = parseArgs([]string{"--serve=false", "server.log"}, &stderr)
	if err != nil || o.serve || len(o.paths) != 1 {
		t.Fatalf("--serve=false: %+v, %v", o, err)
	}
	o, err = parseArgs([]string{"--serve", "--", "-server.log"}, &stderr)
	if err != nil || !o.serve || len(o.paths) != 1 || o.paths[0] != "-server.log" {
		t.Fatalf("-- separator: %+v, %v", o, err)
	}
	if _, err = parseArgs([]string{"server.log", "--serve=invalid"}, &stderr); err == nil {
		t.Fatal("invalid boolean accepted")
	}
}

func TestReportHandler(t *testing.T) {
	const page = "<!doctype html><html><body>é timeline</body></html>"
	handler := reportHandler(page)
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/", 200}, {"GET", "/?view=timeline", 200}, {"HEAD", "/", 200},
		{"POST", "/", 405}, {"GET", "/server.log", 404}, {"GET", "/../server.log", 404}, {"GET", "/web/", 404},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != tc.status {
				t.Fatalf("got %d, want %d", w.Code, tc.status)
			}
			if tc.status == 200 {
				if w.Header().Get("Content-Type") != "text/html; charset=utf-8" || w.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("wrong headers: %v", w.Header())
				}
				if tc.method == "HEAD" {
					if w.Body.Len() != 0 {
						t.Fatal("HEAD returned body")
					}
				} else if w.Body.String() != page {
					t.Fatal("report changed")
				}
			}
			if tc.status == 405 && w.Header().Get("Allow") != "GET, HEAD" {
				t.Fatal("missing Allow header")
			}
		})
	}
}

type serverOutput chan string

func (c serverOutput) Write(p []byte) (int, error) { c <- string(p); return len(p), nil }

func TestServeReport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output := make(serverOutput, 1)
	done := make(chan error, 1)
	const page = "<!doctype html><title>served timeline</title>"
	go func() { done <- serveReport(ctx, "127.0.0.1:0", page, output) }()
	var url string
	select {
	case line := <-output:
		fields := strings.Fields(line)
		if len(fields) < 4 {
			t.Fatalf("missing URL: %q", line)
		}
		url = fields[3]
	case err := <-done:
		t.Fatalf("server failed to start: %v", err)
	case <-ctx.Done():
		t.Fatal("server startup timed out")
	}
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || string(body) != page {
		t.Fatalf("wrong response: %d %q", response.StatusCode, body)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("server did not stop after cancellation")
	}
}

func TestServeListenFailure(t *testing.T) {
	var stdout bytes.Buffer
	if err := serveReport(context.Background(), "invalid-address", "report", &stdout); err == nil {
		t.Fatal("invalid listen address accepted")
	}
	if stdout.Len() != 0 {
		t.Fatal("printed URL before successful bind")
	}
}
