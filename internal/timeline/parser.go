package timeline

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	headerRE         = regexp.MustCompile(`(?i)^(?:####)?<([^>]+)>\s+<(Trace|Debug|Info|Notice|Warning|Error|Critical|Alert|Emergency)>\s+<`)
	fieldsRE         = regexp.MustCompile(`<(.*?)>\s+`)
	codeRE           = regexp.MustCompile(`<([A-Za-z][\w]*-\d{6,}|\d{6})>`)
	epochRE          = regexp.MustCompile(`<(\d{13})>`)
	englishRE        = regexp.MustCompile(`(?i)^(\w{3}) (\d{1,2}), (\d{4}) (\d{1,2}):(\d{2}):(\d{2})(?:[.,](\d{1,6}))? (AM|PM)(?: (\S+))?$`)
	numericZoneRE    = regexp.MustCompile(`^[+-]\d{2}:?\d{2}$`)
	xidRE            = regexp.MustCompile(`\bBEA1-[A-Za-z0-9]+`)
	lifecycleRE      = regexp.MustCompile(`(?i)\bServer state changed to\b`)
	busRE            = regexp.MustCompile(`\b(?:tmb|tmbs|tcp|ssl)://(\[[^\]]+\]|[\w.-]+):(\d+)(?:\.(\d+))?`)
	memberEndpointRE = regexp.MustCompile(`\bAddress=(\[[^\]]+\]|[\w.-]+):(\d+)`)
	serviceRE        = regexp.MustCompile(`\b(?:Service=|ServiceName=|Name=)([\w:$.-]+)`)
	serviceActionRE  = regexp.MustCompile(`(?i)\b(?:for service|joined service|left service)\s+["']?([\w:$.-]+)`)
	serviceLifeRE    = regexp.MustCompile(`\bService\s+([\w:$.-]+)\s+(?:left|joined|started|stopped)`)
	clusterRE        = regexp.MustCompile(`(?i)\bcluster\s+"([^"]+)"`)
	memberRE         = regexp.MustCompile(`\bMember\(Id=(\d+)`)
	memberNameRE     = regexp.MustCompile(`\bmember:([\w.-]+)`)
	localMemberRE    = regexp.MustCompile(`\bmember=(\d+|n/a)\):`)
	engineThreadRE   = regexp.MustCompile(`\(thread=(.*), member=(?:\d+|n/a)\):`)
	bodyPrefixRE     = regexp.MustCompile(`^.*?\bmember=(?:\d+|n/a)\):\s*`)
)
var categories = map[string]string{
	"BEA-000337": "Stuck threads", "BEA-000339": "Thread recovery", "BEA-111015": "Transaction timeouts",
	"BEA-101017": "HTTP errors", "BEA-011139": "EJB errors", "BEA-010026": "EJB errors", "BEA-000627": "JDBC capacity",
	"BEA-000628": "Pool maintenance", "BEA-002939": "WorkManager", "BEA-002936": "WorkManager",
}
var offsets = map[string]int{"UTC": 0, "GMT": 0, "CET": 60, "CEST": 120, "EST": -300, "EDT": -240, "CST": -360, "CDT": -300, "MST": -420, "MDT": -360, "PST": -480, "PDT": -420}

type Event struct {
	ID            string     `json:"id"`
	Epoch         int64      `json:"epoch"`
	Date          string     `json:"date"`
	Time          string     `json:"time"`
	OriginalTime  string     `json:"originalTime"`
	Severity      string     `json:"severity"`
	Category      string     `json:"category"`
	Code          string     `json:"code"`
	Subsystem     string     `json:"subsystem"`
	Host          string     `json:"host"`
	Server        string     `json:"server"`
	Thread        string     `json:"thread"`
	Line          int        `json:"line"`
	EndLine       int        `json:"endline"`
	Summary       string     `json:"summary"`
	Detail        string     `json:"detail"`
	Truncated     bool       `json:"truncated"`
	OriginalChars int        `json:"originalChars"`
	XIDs          []string   `json:"xids"`
	Source        string     `json:"source"`
	Coherence     *Coherence `json:"coherence,omitempty"`
	EventType     string     `json:"eventType,omitempty"`
}
type Coherence struct {
	Endpoints   []string `json:"endpoints"`
	Connections []string `json:"connections"`
	Services    []string `json:"services"`
	Clusters    []string `json:"clusters"`
	Members     []string `json:"members"`
	MemberNames []string `json:"memberNames"`
	LocalMember string   `json:"localMember"`
	Thread      string   `json:"thread"`
	EventType   string   `json:"eventType"`
}
type stringSet map[string]struct{}

func (s stringSet) add(v string) { s[v] = struct{}{} }
func (s stringSet) sorted() []string {
	a := make([]string, 0, len(s))
	for v := range s {
		a = append(a, v)
	}
	sort.Strings(a)
	return a
}

type metadata struct{ endpoints, connections, services, clusters, members, memberNames stringSet }

func newMetadata() *metadata {
	return &metadata{stringSet{}, stringSet{}, stringSet{}, stringSet{}, stringSet{}, stringSet{}}
}
func (m *metadata) accumulate(s string) {
	for _, v := range busRE.FindAllStringSubmatch(s, -1) {
		m.endpoints.add(v[1] + ":" + v[2])
		m.connections.add(v[0])
	}
	for _, v := range memberEndpointRE.FindAllStringSubmatch(s, -1) {
		m.endpoints.add(v[1] + ":" + v[2])
	}
	for _, p := range []struct {
		re  *regexp.Regexp
		set stringSet
	}{{serviceRE, m.services}, {serviceActionRE, m.services}, {serviceLifeRE, m.services}, {clusterRE, m.clusters}, {memberRE, m.members}, {memberNameRE, m.memberNames}} {
		for _, v := range p.re.FindAllStringSubmatch(s, -1) {
			p.set.add(v[1])
		}
	}
}
func (m *metadata) classify(message string) *Coherence {
	c := &Coherence{Endpoints: m.endpoints.sorted(), Connections: m.connections.sorted(), Services: m.services.sorted(), Clusters: m.clusters.sorted(), Members: m.members.sorted(), MemberNames: m.memberNames.sorted(), EventType: "Other Coherence"}
	body := bodyPrefixRE.ReplaceAllString(message, "")
	for _, p := range coherencePatterns {
		if p.pattern.MatchString(body) {
			c.EventType = p.label
			break
		}
	}
	if v := localMemberRE.FindStringSubmatch(message); v != nil {
		c.LocalMember = v[1]
	}
	if v := engineThreadRE.FindStringSubmatch(message); v != nil {
		c.Thread = v[1]
	}
	return c
}

// localDate chooses the first occurrence of an ambiguous wall clock, matching
// Python's fold=0. It also uses the pre-transition offset for a DST gap.
func localDate(y int, month time.Month, d, h, min, s, ns int, zone *time.Location) time.Time {
	wall := time.Date(y, month, d, h, min, s, ns, time.UTC)
	offsetsSeen := map[int]bool{}
	for _, delta := range []time.Duration{-48 * time.Hour, -24 * time.Hour, 0, 24 * time.Hour, 48 * time.Hour} {
		_, off := wall.Add(delta).In(zone).Zone()
		offsetsSeen[off] = true
	}
	var first time.Time
	for off := range offsetsSeen {
		candidate := wall.Add(-time.Duration(off) * time.Second)
		t := candidate.In(zone)
		if t.Year() == y && t.Month() == month && t.Day() == d && t.Hour() == h && t.Minute() == min && t.Second() == s && (first.IsZero() || candidate.Before(first)) {
			first = candidate
		}
	}
	if !first.IsZero() {
		return first
	}
	_, off := wall.Add(-48 * time.Hour).In(zone).Zone()
	return wall.Add(-time.Duration(off) * time.Second)
}
func roundedMillis(t time.Time) int64 {
	// Round half to even, as Python round(timestamp * 1000) does.
	ms := t.Unix()*1000 + int64(t.Nanosecond()/1000000)
	rest := t.Nanosecond() % 1000000
	if rest > 500000 || rest == 500000 && ms%2 != 0 {
		ms++
	}
	return ms
}
func parseTimestamp(header, date string, assume *time.Location) (int64, error) {
	before := header
	if loc := codeRE.FindStringIndex(header); loc != nil {
		before = header[:loc[0]]
	}
	if epochs := epochRE.FindAllStringSubmatch(before, -1); len(epochs) > 0 {
		return strconv.ParseInt(epochs[len(epochs)-1][1], 10, 64)
	}
	// ISO 8601 forms supported by WebLogic exports, including fractional seconds.
	normalized := strings.ReplaceAll(date, ",", ".")
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05Z07:00", "2006-01-02T15:04:05-0700", "2006-01-02 15:04:05-0700", "2006-01-02T15:04:05Z07", "2006-01-02 15:04:05Z07"} {
		if t, err := time.Parse(layout, normalized); err == nil {
			return roundedMillis(t), nil
		}
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, normalized); err == nil {
			if assume == nil {
				return 0, fmt.Errorf("timestamp has no timezone; use --assume-timezone: %s", date)
			}
			return roundedMillis(localDate(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), assume)), nil
		}
	}
	m := englishRE.FindStringSubmatch(date)
	if m == nil {
		return 0, fmt.Errorf("unsupported timestamp: %s", date)
	}
	month := 0
	for i, name := range strings.Fields("Jan Feb Mar Apr May Jun Jul Aug Sep Oct Nov Dec") {
		if strings.EqualFold(m[1], name) {
			month = i + 1
		}
	}
	if month == 0 {
		return 0, fmt.Errorf("unsupported timestamp: %s", date)
	}
	number := func(i int) int { n, _ := strconv.Atoi(m[i]); return n }
	y, d, h, min, s := number(3), number(2), number(4), number(5), number(6)
	if h < 1 || h > 12 {
		return 0, fmt.Errorf("invalid 12-hour clock: %s", date)
	}
	h %= 12
	if strings.EqualFold(m[8], "PM") {
		h += 12
	}
	ns := 0
	if m[7] != "" {
		ns, _ = strconv.Atoi(m[7] + strings.Repeat("0", 9-len(m[7])))
	}
	zone := assume
	if off, ok := offsets[strings.ToUpper(m[9])]; ok {
		zone = time.FixedZone(m[9], off*60)
	} else if numericZoneRE.MatchString(m[9]) {
		z := strings.ReplaceAll(m[9], ":", "")
		hh, _ := strconv.Atoi(z[1:3])
		mm, _ := strconv.Atoi(z[3:])
		off := (hh*60 + mm) * 60
		if z[0] == '-' {
			off = -off
		}
		if off <= -86400 || off >= 86400 {
			return 0, fmt.Errorf("invalid timezone offset: %s", date)
		}
		zone = time.FixedZone(m[9], off)
	}
	if zone == nil {
		return 0, fmt.Errorf("unknown or missing timezone; use --assume-timezone: %s", date)
	}
	t := time.Date(y, time.Month(month), d, h, min, s, ns, time.UTC)
	if y < 1 || t.Year() != y || int(t.Month()) != month || t.Day() != d || min > 59 || s > 59 {
		return 0, fmt.Errorf("invalid date/time: %s", date)
	}
	return roundedMillis(localDate(y, time.Month(month), d, h, min, s, ns, zone)), nil
}

type record struct {
	header                            string
	line, endLine, chars, detailChars int
	detail                            strings.Builder
	xids                              stringSet
	coherence                         *metadata
}

// Python text input accepts LF, CRLF, and CR line endings. Normalize before
// splitting records, including CRLF pairs divided between underlying reads.
type newlineReader struct {
	reader     io.Reader
	previousCR bool
}

func (r *newlineReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	written := 0
	for _, b := range p[:n] {
		if b == '\n' && r.previousCR {
			r.previousCR = false
			continue
		}
		r.previousCR = b == '\r'
		if r.previousCR {
			b = '\n'
		}
		p[written] = b
		written++
	}
	return written, err
}

func fieldValues(header string) []string {
	matches := fieldsRE.FindAllStringSubmatch(header, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}
func prefixChars(s string, n int) string {
	if n <= 0 {
		return ""
	}
	for i := range s {
		if n == 0 {
			return s[:i]
		}
		n--
	}
	return s
}
func parseLog(path string, zone *time.Location, maxChars int, assume *time.Location, detailed string) ([]Event, []string, int, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, 0, err
	}
	defer file.Close()
	var input io.Reader = file
	if strings.HasSuffix(path, ".gz") {
		gz, e := gzip.NewReader(file)
		if e != nil {
			return nil, nil, 0, e
		}
		defer gz.Close()
		input = gz
	}
	reader := bufio.NewReader(&newlineReader{reader: input})
	events := []Event{}
	warnings := []string{}
	var current *record
	total, preamble := 0, 0
	finish := func() {
		if current == nil {
			return
		}
		r := current
		h := r.header
		match := headerRE.FindStringSubmatch(h)
		epoch, e := parseTimestamp(h, match[1], assume)
		if e != nil {
			warnings = append(warnings, fmt.Sprintf("Line %d: %v", r.line, e))
			return
		}
		dt := time.UnixMilli(epoch).In(zone)
		if dt.Year() < 1 || dt.Year() > 9999 {
			warnings = append(warnings, fmt.Sprintf("Line %d: timestamp outside supported year range", r.line))
			return
		}
		code, message := "NO-CODE", h
		if idx := codeRE.FindStringSubmatchIndex(h); idx != nil {
			code = h[idx[2]:idx[3]]
			message = strings.TrimSpace(h[idx[1]:])
		}
		message = strings.TrimPrefix(message, "<")
		message = strings.TrimRight(message, " >")
		fields := fieldValues(h)
		subsystem := "Unknown"
		if len(fields) > 2 {
			subsystem = fields[2]
		}
		category := categories[code]
		if category == "" {
			category = subsystem
			if category == "" {
				category = "Other"
			}
		}
		if category == "Work Manager" {
			category = "WorkManager"
		}
		if strings.EqualFold(subsystem, "WebLogicServer") && lifecycleRE.MatchString(message) {
			category = "WebLogic Server Lifecycle"
		}
		severity := strings.ToUpper(match[2][:1]) + strings.ToLower(match[2][1:])
		event := Event{ID: fmt.Sprintf("L%d", r.line), Epoch: epoch, Date: dt.Format("2006-01-02"), Time: dt.Format("15:04:05.000"), OriginalTime: match[1], Severity: severity, Category: category, Code: code, Subsystem: subsystem, Line: r.line, EndLine: r.endLine, Summary: prefixChars(message, 500), Detail: r.detail.String(), Truncated: r.chars > r.detailChars, OriginalChars: r.chars, XIDs: r.xids.sorted()}
		if strings.HasPrefix(h, "####") {
			if len(fields) > 3 {
				event.Host = fields[3]
			}
			if len(fields) > 4 {
				event.Server = fields[4]
			}
			if len(fields) > 5 {
				event.Thread = fields[5]
			}
		}
		if r.coherence != nil {
			event.Coherence = r.coherence.classify(message)
			event.EventType = event.Coherence.EventType
			event.Category = "Coherence: " + event.EventType
		}
		events = append(events, event)
	}
	// ReadString has no scanner token limit: individual diagnostic lines may be huge.
	for {
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 {
			// Match Python's universal-newline text reader and replacement decoding.
			line = strings.ToValidUTF8(line, "\ufffd")
			total++
			if headerRE.MatchString(line) {
				finish()
				current = &record{header: strings.TrimRight(line, "\r\n"), line: total, endLine: total, xids: stringSet{}}
				fields := fieldValues(line)
				if detailed == "coherence" && len(fields) > 2 && strings.Contains(strings.ToLower(fields[2]), "coherence") {
					current.coherence = newMetadata()
				}
			}
			if current == nil {
				preamble++
			} else {
				r := current
				r.endLine = total
				r.chars += utf8.RuneCountInString(line)
				if room := maxChars - r.detailChars; room > 0 {
					excerpt := prefixChars(line, room)
					r.detail.WriteString(excerpt)
					r.detailChars += utf8.RuneCountInString(excerpt)
				}
				for _, xid := range xidRE.FindAllString(line, -1) {
					r.xids.add(xid)
				}
				if r.coherence != nil {
					r.coherence.accumulate(line)
				}
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				return nil, nil, total, readErr
			}
			break
		}
	}
	finish()
	sort.SliceStable(events, func(i, j int) bool { return events[i].Epoch < events[j].Epoch })
	if preamble > 0 {
		warnings = append([]string{fmt.Sprintf("%d preamble line(s) before the first recognized record were not plotted.", preamble)}, warnings...)
	}
	return events, warnings, total, nil
}

// ParseLogs merges independent logs by timestamp while retaining their source locations.
func ParseLogs(paths []string, zone *time.Location, maxChars int, assume *time.Location, detailed string) ([]Event, []string, int, error) {
	events := []Event{}
	warnings := []string{}
	total := 0
	for i, path := range paths {
		fileEvents, fileWarnings, lines, err := parseLog(path, zone, maxChars, assume, detailed)
		if err != nil {
			return nil, nil, total, fmt.Errorf("%s: %w", path, err)
		}
		for _, e := range fileEvents {
			e.ID = fmt.Sprintf("F%d-%s", i+1, e.ID)
			e.Source = path
			events = append(events, e)
		}
		for _, w := range fileWarnings {
			warnings = append(warnings, path+": "+w)
		}
		total += lines
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].Epoch < events[j].Epoch })
	return events, warnings, total, nil
}
