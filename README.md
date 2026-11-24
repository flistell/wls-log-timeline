# WebLogic Event Timeline

Turn WebLogic logs into an interactive timeline you can open in a browser and share as a single HTML file. Merge logs across servers, hosts, and rotations to explore events alongside their original log evidence.

Built in Go using the standard library. The static executable embeds the interface and timezone database; running it requires no Go installation, Python, shared libraries, or frontend dependencies. Reports work offline.

[Quick start](#quick-start) · [Usage](#usage) · [Options](#options) · [Linux builds](#linux-builds) · [Development](#development) · [Full user guide](docs/user-guide.md)

## Features

- **Merge multiple logs:** plain text and gzip files, multiline records, stack traces, and source file/line references. Records are ordered by timestamp without deduplication.
- **Explore interactively:** zoom, drag to select a time range, search, filter, exclude records with regex patterns, and show or hide individual events.
- **Choose chart lanes:** categories, hostnames, servers, message codes, or hostname + thread ID.
- **Choose colors independently:** categories, severity, message codes, servers, hostnames, or source files, with a legend and stable colors across filters and zoom levels.
- **Inspect Coherence events:** detailed classification and explicit endpoint, service, cluster, and member references across hosts.
- **Keep reports portable:** export plotted events as JSON, embed custom CSS, open HTML directly, or serve it with the built-in HTTP server.

## Quick start

Build from the repository root with **Go 1.24+** and Make:

```bash
make build
./bin/wls-events-timeline /path/to/weblogic.log --timezone Europe/Rome
```

Open `weblogic-timeline.html` in a browser. Input logs are preserved. A terminal spinner shows progress while processing files and stays disabled when stderr is redirected.

Without Make:

```bash
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/wls-events-timeline ./cmd/wls-events-timeline
```

> [!IMPORTANT]
> Reports embed log excerpts. Handle and share them with the same care as the source logs.

## Usage

### Combine server logs and rotations

```bash
./bin/wls-events-timeline server1.log server2.log server1.log00001.gz \
  --timezone Europe/Rome \
  --title "Production server events" \
  --output production-timeline.html
```

Shell-expanded wildcards are supported. Each file is parsed independently, and equal timestamps retain input-file order followed by line order. Repeated or overlapping inputs retain duplicate records.

### Serve a report locally

```bash
./bin/wls-events-timeline weblogic.log --timezone Europe/Rome --serve
```

Open the printed URL, normally `http://127.0.0.1:8080/`. Use `--listen 127.0.0.1:9000` for another port, or `--listen 127.0.0.1:0` to select a free port. Press **Ctrl+C** to stop; the HTML file remains saved.

The server serves the report generated at startup at `/`. It does not watch input logs or serve other files. The default listener accepts local connections only. Choosing an externally reachable address exposes the report over HTTP without authentication.

### Analyze Coherence across hosts

```bash
./bin/wls-events-timeline logs/*/weblogic.log* \
  --detailed-events=coherence \
  --timezone Europe/Rome \
  --output coherence-timeline.html
```

Detailed mode classifies transport failures, cluster and service lifecycle changes, membership events, partition operations, guardian timeouts, cache restarts, and stuck workers. Endpoint tracking lets you follow explicit endpoint mentions across input hosts. References are evidence, not inferred relationships or root causes.

### Work with the timeline

Use **Group chart by** for lanes and **Color chart by** for colors. Filter categories, severity, message codes, servers, hostnames, and source files; detailed Coherence mode adds event-type and identifier filters. Search and regex exclusions combine with these filters.

Drag across the chart to zoom, or use the zoom buttons. Date/time filters use `DD/MM/YYYY HH:mm:ss.SSS` in the report timezone. Select a dot or event to inspect its source and excerpt, and export the currently plotted events as JSON.

Dense views aggregate dots into time buckets while retaining every matching record in the event list and export. The [user guide](docs/user-guide.md) explains controls, endpoint tracking, custom styles, and parsing details.

## Options

```text
wls-events-timeline [options] LOG [LOG ...]
```

Options can appear before, between, or after input paths.

| Option | Purpose | Default |
| --- | --- | --- |
| `-o`, `--output PATH` | HTML destination; replaces an existing report | `weblogic-timeline.html` |
| `--title TEXT` | Report title | `WebLogic event timeline` |
| `--timezone ZONE` | IANA timezone for display and filters | `UTC` |
| `--assume-timezone ZONE` | Source timezone for textual timestamps without a recognized zone | Unset |
| `--detailed-events coherence` | Enable detailed Coherence classification and identifier filters | Disabled |
| `--max-detail-chars N` | Maximum embedded excerpt characters per record | `12000` |
| `--stylesheet PATH` | Embed a local CSS file after the default styles | Unset |
| `--serve` | Serve the generated report until stopped | Disabled |
| `--listen HOST:PORT` | HTTP listener address when using `--serve` | `127.0.0.1:8080` |
| `-h`, `--help` | Show command help | — |

WebLogic epoch milliseconds take precedence over textual timestamps. Unsupported timestamps are reported as warnings and omitted. The parser handles WebLogic server and console/stdout headers; standalone Coherence logs without WebLogic headers are outside its scope.

Excerpts may be truncated, and search cannot match omitted payload text. The original logs remain authoritative. Output cannot overwrite an input log or the custom stylesheet.

## Linux builds

Build static executables and checksums for both supported release targets:

```bash
make release
cd dist
sha256sum -c SHA256SUMS
```

| Executable | Target |
| --- | --- |
| `wls-events-timeline-linux-amd64` | Linux x86-64, baseline `GOAMD64=v1` |
| `wls-events-timeline-linux-arm64` | Linux ARM64, baseline `GOARM64=v8.0` |

Copy the matching executable to another machine and run it directly. Builds use `CGO_ENABLED=0` and have no glibc or musl dependency. The target CPU and kernel must meet the Go runtime's requirements; see the [portability notes](docs/user-guide.md#build-and-linux-portability).

## Development

```bash
make check   # Go tests and go vet
```

Tests cover parsing, timestamps and DST, multiline records, gzip, source locations, Coherence metadata, report generation, overwrite protection, and HTTP serving. If `sample-logs/` is present, tests also exercise those files; local sample logs are ignored by Git.

To compare parsed events and report metadata against the original Python implementation:

```bash
make parity
```

This optional migration check requires Python 3.9+, its timezone data, and local logs in `sample-logs/`. Python is used only for this check. The original implementation is retained in `testdata/reference/wls-events-timeline.py`.

The browser interface lives in `internal/report/timeline.html` and is embedded at compile time. Rebuild the executable and regenerate reports after changing it.

### Project layout

```text
cmd/wls-events-timeline/  Command entry point
internal/
  cli/                   Options, command execution, HTTP server, terminal progress
  timeline/              Log parsing, timestamps, event types, Coherence classification
  report/                HTML generation and embedded browser interface
docs/user-guide.md       Detailed usage and portability notes
scripts/                 Development checks
testdata/reference/      Original Python implementation used for parity checks
sample-logs/             Optional local test inputs (ignored by Git)
bin/                     Local executable from make build (ignored by Git)
dist/                    Linux release binaries and checksums (ignored by Git)
```

Tests live beside the packages they exercise. Application packages use `internal/` so they can evolve without becoming a public library API. Run `go run ./cmd/wls-events-timeline --help` to try the command directly from source.
