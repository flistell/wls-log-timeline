# WebLogic interactive timeline generator

Implemented in Go with only the standard library. The compiled executable embeds the report interface and IANA timezone database. Running it requires no Go installation, Python, shared libraries, system timezone database, or frontend packages. The generated HTML works offline.

## Build and Linux portability

Build with Go 1.24 or newer and Make:

```bash
make build
./wls-events-timeline --help
```

Without Make, the equivalent command is:

```bash
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o wls-events-timeline .
```

Create binaries for Linux x86-64 and ARM64, plus checksums:

```bash
make release
cd dist
sha256sum -c SHA256SUMS
```

Copy the matching executable to the destination Linux machine, rename it to `wls-events-timeline` if desired, and run it directly. Each executable is a complete application; no adjacent HTML or timezone files are needed. `GOAMD64=v1` targets baseline x86-64 CPUs and `GOARM64=v8.0` targets baseline ARM64 CPUs. A binary runs across distributions with the same supported CPU architecture; it does not run across different CPU architectures.

`CGO_ENABLED=0` produces a static binary without a glibc or musl dependency. The Go runtime still has a minimum Linux kernel requirement: Go 1.24 and later document Linux 3.2 or newer for these targets. See the [Go minimum requirements](https://go.dev/wiki/MinimumRequirements) and [Linux support notes](https://go.dev/wiki/Linux) for requirements of the exact compiler and architecture used. This does not promise compatibility with every historical Linux release. The build has been checked for static linking and run on the development host; older kernels and ARM64 require testing on those targets.

## Generate a report

```bash
./wls-events-timeline weblogic.log --timezone Europe/Rome
```

This creates `weblogic-timeline.html` in the current directory and preserves the original log.

Pass multiple logs (including shell-expanded wildcards) to combine server logs or rotated files into one timeline:

```bash
./wls-events-timeline server1.log server2.log server1.log00001 \
  --output combined.timeline.html --timezone Europe/Rome
```

Records are merged by timestamp. Equal timestamps retain input-file order, then line order. Each record retains its source path and line numbers; source files can be filtered and searched, and exported events include their source. Each file is parsed independently, so continuation lines cannot cross file boundaries. Overlapping or repeated inputs retain all records; no deduplication is performed.

```bash
./wls-events-timeline /path/to/server.log \
  --output /tmp/server-timeline.html \
  --timezone Europe/Rome \
  --title "Production server events"
```

The HTML is written to the file specified by `--output` (or `-o`), or to `weblogic-timeline.html` in the current directory when no output argument is given. An existing HTML destination is replaced. None of the input logs can be used as the output path.

## Coherence events across hosts

Enable detailed Coherence classification while merging server logs from multiple hosts and rotations:

```bash
./wls-events-timeline \
  logs-20261116/salclp170*/miph0-coh/weblogic.log00001-20261115 \
  logs-20261116/salclp170*/miph0-cohext/weblogic.log00001-20261115 \
  --detailed-events=coherence \
  --timezone Europe/Rome \
  --output coherence-cluster.timeline.html
```

Both ordinary text files and `.gz` logs are accepted. Shell wildcards expand to individual input files; directories are not accepted. Prefer the WebLogic server logs when available: combining server logs with their STDOUT copies preserves duplicate events. Rotated filenames do not determine event dates; timestamps inside the records do.

Without `--detailed-events`, the existing generic categories are used. With `--detailed-events=coherence`, WebLogic-wrapped records from Coherence subsystems receive specific event types and category lanes:

- Transport ACK timeouts, migration, migration recovery, connection failures, `Connection established` messages and transport diagnostics (including `TEMP DEBUG post-flush`).
- Communication delays, cluster discovery failures and cluster formation.
- Member joins and departures; service joins, departures and lifecycle events.
- `Coherence: stopping cluster` for messages containing `stopping cluster service` (including senior heartbeat membership rejection) or `Exiting cluster`. This category takes precedence over generic timeout and departure classifications.
- `Coherence: restarting cache` for `Restarting NamedCache: <cache name>` messages, including `Payments`, `Banks` and other named caches.
- `Coherence: Restarting cluster` for `Restarting cluster` messages, including ServiceMonitor messages with `member=n/a`.
- `Coherence: Worker STUCK` for messages stating that a worker thread has been executing a task and `appears to be stuck`, including `PutRequest` diagnostics. Reported task duration remains record evidence, not an inferred timeline interval.
- Partition transfer, pending distribution, stabilization and recovery.
- Guardian timeout/recovery, unavailable storage, proxy request failures, protocol assertions and management failures.
- Other Coherence messages are retained as `Other Coherence`. Non-Coherence records remain available.

Use **Group chart by → Hostnames** or **Servers** to compare sources on parallel lanes. The detail panel shows the Coherence thread, local member number, explicit cluster/service references and endpoints. Select an endpoint button to track records mentioning that endpoint across all input hosts; **Clear endpoint tracking** removes this restriction. Existing time, text, severity and source filters still apply. Additional multi-value filters select event types, services, explicitly mentioned cluster names and endpoints. Values within a filter combine with OR; separate filters combine with AND. Records without a given identifier are shown as `(unspecified)` in that filter.

Endpoint tracking uses the literal `host:port` from bus addresses or `Member(... Address=...)` diagnostics. Full bus addresses (including their incarnation suffix) remain visible in the detail panel and exported JSON. Tracking spans process restarts; it is an evidence aid, not a claim that events share a cause or connection incarnation. Member IDs are scoped to a cluster incarnation and are never used alone to join records. DNS names and IP addresses are not resolved or treated as equivalent. STDOUT headers without host/server fields retain those fields as unspecified; use source-file filtering for those records.

Classification uses the message headline, so a quoted exception further down a stack trace does not override the event type. Endpoint, service, cluster and member references are extracted across the entire record even when the displayed excerpt is truncated. References found in diagnostics are mentions, not inferred service/cluster membership. No network discovery or Oracle knowledge lookup occurs during report generation. Standalone Coherence records without WebLogic headers remain outside the parser's scope.

For more than 10,000 plotted records, chart dots aggregate records into time buckets per lane. Click an aggregate to zoom into its time range. Every matching record remains in the paginated list and JSON export. Grouping does not deduplicate records or change record counts. This limits chart elements for large multi-host logs; the HTML still embeds all excerpts and may be large.

## Custom stylesheets and severity colors

Pass a local CSS file with `--stylesheet` to override the default appearance:

```bash
./wls-events-timeline \
  logs-20261116/*/*/weblogic.log00001-20261115 \
  --detailed-events=coherence \
  --stylesheet timeline-custom.css \
  --timezone Europe/Rome \
  --output coherence-custom.timeline.html
```

The CSS is embedded in the generated HTML after the default stylesheet. The CSS file is not needed when opening or sharing the report. Regenerate the HTML after editing the CSS; existing HTML reports do not reload the original CSS file. A missing/unreadable stylesheet fails generation before writing output. Output cannot overwrite the stylesheet or any input log.

For example, change Warning, Error and Trace accents with:

```css
:root {
  --severity-warning: #ffb020;
  --severity-error: #ff5252;
  --severity-trace: #94a3b8;
}
```

The severity variables are `--severity-trace`, `--severity-debug`, `--severity-info`, `--severity-notice`, `--severity-warning`, `--severity-error`, `--severity-critical`, `--severity-alert` and `--severity-emergency`. They control chart dots, event-list accent borders, severity badges and severity filter labels. Severity colors apply in both generic and detailed Coherence modes. Dense chart buckets keep different severities separate, including when they share a timestamp and lane.

You can also override page variables such as `--bg`, `--panel`, `--line`, `--muted` and `--accent`, or use normal CSS selectors. Event marks (`.mark`), list rows (`.row`), severity badges (`.badge`) and severity filter labels expose `data-severity="Warning"`, `data-severity="Error"`, `data-severity="Trace"`, etc. Attribute values use title case.

Use standalone CSS with inline/data-URL assets for offline reports. Relative `url()` and `@import` references are not supported by the embedded data-URL stylesheet; absolute external asset references require network access.

## Timeline controls

- Use **Zoom in** to halve the current time range, **Zoom out** to double it, and **Full range** to restore the complete input time range. Zoom stays within the original range and has millisecond precision. Existing category, host, search, exclusion and other filters remain active.
- Drag horizontally across the plotted area to select a time interval and zoom into it across all lanes. A translucent overlay shows the selection; dragging in either direction works. Click an event normally to inspect it. The From/To fields, list and export update to the selected time window. Press **Esc** to cancel a selection. Focus the chart and use **+ / -** to zoom or **Home** for the full range.
- Enter From and To dates and times in European `DD/MM/YYYY HH:mm:ss.SSS` format with a 24-hour clock to filter across multiple days. Seconds and milliseconds are optional (for example, `02/11/2026 17:30`). Invalid dates or times show a validation message. Text inputs ensure this format is consistent across browser locales. Inputs and chart labels use the report’s `--timezone` (default UTC), independently of the browser timezone. The initial range covers all records; clearing either bound removes that limit. During a daylight-saving clock repeat, both occurrences of a matching local time are included.
- Toggle categories, severity, individual message codes, servers, hostnames, and source files. Each group has All/None buttons; all groups combine with AND.
- **Group chart by** selects categories, hostnames, servers, **BEA / message code**, or **Hostname + thread ID**. Message-code grouping includes BEA codes and other recognized codes. Hostname/thread grouping uses the logged WebLogic thread field, keeping its queue name and removing state prefixes such as `[ACTIVE]`, `[STANDBY]`, and `[STUCK]` so state changes stay on the same lane. Missing hosts or threads appear as `(unspecified)`. Host and thread appear on separate lines; hover over a lane label to read the complete values. Grouping works with all filters and dense-chart aggregation.
- Search message text, retained stack traces, host, thread or transaction ID.
- Use **Exclude (regex)** like `grep -v -E` to hide entire records matching any pattern. Commas separate patterns, and `|` separates regex alternatives: `TEMP DEBUG|post-flush, Connection reset` excludes records containing any of those three phrases. Matching is case insensitive and checks retained original record text, including continuation/stack-trace lines. `^` and `$` match individual lines within a record. The regex syntax is JavaScript regular expressions, which differs from POSIX ERE in some details.
- Spaces around comma-separated patterns and empty comma entries are ignored. Commas inside parentheses, character classes, repetition counts such as `{1,3}`, or escaped as `\,` remain part of the regex. Escape a literal pipe as `\|`. Invalid expressions show a validation message and disable export until corrected. Exclusions combine with all other filters, affect charts/lists/exports, and are cleared by **Reset filters**. Text omitted by excerpt truncation cannot be matched by the exclusion filter.
- Toggle an individual event's checkbox to show/hide it on the chart. Hidden events stay in the event list, so they can be re-enabled.
- Show/hide all matching events, including other pages, or restore every individually hidden event.
- Click a chart dot or list entry for the source path, line numbers and original record excerpt.
- Export the currently plotted events as JSON. Reset filters restores every event and the full date/time range.

Records are plotted as points; reported timeout/stuck ages are not treated as measured activity intervals. Large lists are paginated at 100 rows; all matching records remain plotted. Many overlapping dots can be separated by narrowing the time window or filters.

## Parsing and scope

WebLogicServer messages containing `Server state changed to ...` use the category **WebLogic Server Lifecycle**, including startup, RUNNING, shutdown and failed states. This classification applies in both generic mode and `--detailed-events=coherence`; original severities and message codes are retained.

The parser handles WebLogic `####<timestamp> <severity> ...` server records and `<timestamp> <severity> ...` console/stdout records. Continuation lines, including stack traces, belong to the preceding header. Standard BEA codes, other prefixed numeric codes and bare six-digit codes are supported; unknown codes remain selectable. Unknown codes use their subsystem as category.

WebLogic epoch milliseconds take precedence over the timestamp text. If no epoch field exists, the parser supports ISO timestamps and English WebLogic dates, including fractional seconds and common fixed timezone abbreviations/numeric offsets. For missing or unrecognized textual timezones, specify the source timezone explicitly:

```bash
./wls-events-timeline server.log \
  --assume-timezone Europe/Rome --timezone Europe/Rome
```

Do not assume an ambiguous abbreviation represents the intended region. Use explicit source timezone information or epoch timestamps. Unsupported timestamps are reported as warnings and their records are omitted; preamble lines are also reported. If no timestamped records are recognized, no HTML is written. Warnings are printed to stderr and summarized in the report. Unrecognized header layouts may be retained as continuation text; check counts against the source for unfamiliar exports.

Embedded record excerpts default to 12,000 characters. Increase `--max-detail-chars` if needed. Truncation is labeled; transaction IDs are collected across the full record, even beyond the excerpt limit. Search covers retained excerpts and extracted IDs, not omitted payload text. The original log remains authoritative. All records and timestamps are retained unless parsing reports an unsupported timestamp. The generated HTML embeds log content; handle it as source-log data.

This is a generic log visualization: it does not infer root causes, incorporate screenshots, or synthesize request intervals. Large logs produce large HTML files and may render slowly; use a scoped log excerpt for very large inputs.

## Validation

```bash
make check
```

Go tests cover parsing, multiline records, stable merge order, gzip, long Unicode lines, timestamp fallbacks and DST, Coherence metadata beyond excerpt limits, HTML embedding, command options, and overwrite protection. When `sample-logs/` is present, the tests also parse all its files together and check event ordering, unique identifiers, source locations, and Coherence classification coverage.

For migration parity against the original Python implementation, run:

```bash
make parity
```

This optional development check requires Python 3.9+ and its timezone data. It compares every event field and all report metadata across the files in `sample-logs/`, in generic mode, detailed Coherence mode, and Coherence mode with 127-character excerpts. The original implementation is retained only as a test oracle in `testdata/wls-events-timeline.py`; the application never invokes it. The report interface may evolve independently of this data comparison.

The Go source is in `main.go` (command and report generation), `parser.go` (records, timestamps and metadata), and `coherence.go` (classification rules). `web/timeline.html` is the original offline interface, embedded at compile time. Editing it requires rebuilding the executable.
