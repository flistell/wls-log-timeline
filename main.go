package main

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"
	"unicode/utf8"
)

//go:embed web/timeline.html
var pageTemplate string

type options struct {
	paths                                                         []string
	output, title, timezone, assumeTimezone, detailed, stylesheet string
	maxChars                                                      int
	serve                                                         bool
	listen                                                        string
}

// flag's default parser stops at the first positional argument; the original
// command accepts options before, between, and after input files.
func parseArgs(args []string, stderr io.Writer) (options, error) {
	o := options{}
	f := flag.NewFlagSet("wls-events-timeline", flag.ContinueOnError)
	f.SetOutput(stderr)
	f.StringVar(&o.output, "output", "weblogic-timeline.html", "HTML destination")
	f.StringVar(&o.output, "o", "weblogic-timeline.html", "HTML destination (short option)")
	f.StringVar(&o.title, "title", "WebLogic event timeline", "Report title")
	f.StringVar(&o.timezone, "timezone", "UTC", "IANA timezone for display and filters")
	f.StringVar(&o.assumeTimezone, "assume-timezone", "", "IANA timezone for timestamps without a recognized zone")
	f.IntVar(&o.maxChars, "max-detail-chars", 12000, "Maximum embedded characters per record")
	f.StringVar(&o.detailed, "detailed-events", "", "Detailed classification: coherence")
	f.StringVar(&o.stylesheet, "stylesheet", "", "Local CSS file to embed in the report")
	f.BoolVar(&o.serve, "serve", false, "Serve the generated report over HTTP until Ctrl+C")
	f.StringVar(&o.listen, "listen", "127.0.0.1:8080", "HTTP listen address (used with --serve; port 0 selects a free port)")
	f.Usage = func() {
		fmt.Fprintln(stderr, "Usage: wls-events-timeline [options] LOG [LOG ...]\n\nGenerate an offline, filterable timeline from multiline WebLogic log records.")
		f.PrintDefaults()
	}
	var flags []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			o.paths = append(o.paths, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			o.paths = append(o.paths, arg)
			continue
		}
		flags = append(flags, arg)
		name := strings.TrimLeft(strings.SplitN(arg, "=", 2)[0], "-")
		if name == "h" || name == "help" {
			f.Usage()
			return o, flag.ErrHelp
		}
		option := f.Lookup(name)
		if option == nil {
			return o, fmt.Errorf("unknown option: %s", arg)
		}
		if boolean, ok := option.Value.(interface{ IsBoolFlag() bool }); ok && boolean.IsBoolFlag() {
			continue
		}
		if !strings.Contains(arg, "=") {
			if i+1 == len(args) {
				return o, fmt.Errorf("option %s requires a value", arg)
			}
			i++
			flags = append(flags, args[i])
		}
	}
	if err := f.Parse(flags); err != nil {
		return o, err
	}
	if len(o.paths) == 0 {
		return o, errors.New("at least one input log is required (use --help for usage)")
	}
	if o.maxChars < 1 {
		return o, errors.New("--max-detail-chars must be positive")
	}
	if o.detailed != "" && o.detailed != "coherence" {
		return o, errors.New("--detailed-events must be coherence")
	}
	return o, nil
}

type payload struct {
	Events         []Event  `json:"events"`
	Source         string   `json:"source"`
	Zone           string   `json:"zone"`
	Warnings       []string `json:"warnings"`
	TotalLines     int      `json:"totalLines"`
	DetailedEvents *string  `json:"detailedEvents"`
}

func makeHTML(events []Event, paths []string, zone string, warnings []string, title string, total int, detailed string, css *string) (string, error) {
	var mode *string
	if detailed != "" {
		mode = &detailed
	}
	data, err := json.Marshal(payload{events, strings.Join(paths, ", "), zone, warnings, total, mode})
	if err != nil {
		return "", err
	}
	style := ""
	if css != nil {
		style = `<link id="custom-stylesheet" rel="stylesheet" href="data:text/css;charset=utf-8;base64,` + base64.StdEncoding.EncodeToString([]byte(*css)) + `">`
	}
	// A single pass prevents log text or titles containing template markers from
	// being processed as another replacement. JSON escapes HTML-sensitive bytes.
	return strings.NewReplacer("__STYLESHEET__", style, "__TITLE__", html.EscapeString(title), "__PAYLOAD__", string(data)).Replace(pageTemplate), nil
}

func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	// Resolve symlinks in existing parents even if the destination does not exist.
	resolved, err := filepath.EvalSymlinks(abs)
	if os.IsNotExist(err) {
		parent := filepath.Dir(abs)
		if parent == abs {
			return abs, nil
		}
		p, e := canonicalPath(parent)
		if e != nil {
			return "", e
		}
		return filepath.Join(p, filepath.Base(abs)), nil
	}
	return resolved, err
}

func protectInputs(output string, inputs []string) error {
	out, err := canonicalPath(output)
	if err != nil {
		return err
	}
	outInfo, _ := os.Stat(output)
	for _, input := range inputs {
		path, err := canonicalPath(input)
		if err != nil {
			return err
		}
		info, _ := os.Stat(input)
		if out == path || outInfo != nil && info != nil && os.SameFile(outInfo, info) {
			return fmt.Errorf("output must differ from every input log and stylesheet: %s", input)
		}
	}
	return nil
}

func run(args []string, stdout, stderr io.Writer) int {
	o, err := parseArgs(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, "Error:", err)
		return 2
	}
	zone, err := time.LoadLocation(o.timezone)
	if err != nil {
		fmt.Fprintln(stderr, "Error: unknown timezone:", o.timezone)
		return 2
	}
	var assume *time.Location
	if o.assumeTimezone != "" {
		assume, err = time.LoadLocation(o.assumeTimezone)
		if err != nil {
			fmt.Fprintln(stderr, "Error: unknown timezone:", o.assumeTimezone)
			return 2
		}
	}
	inputs := append([]string{}, o.paths...)
	if o.stylesheet != "" {
		inputs = append(inputs, o.stylesheet)
	}
	if err := protectInputs(o.output, inputs); err != nil {
		fmt.Fprintln(stderr, "Error:", err)
		return 2
	}
	var css *string
	if o.stylesheet != "" {
		data, err := os.ReadFile(o.stylesheet)
		if err != nil {
			fmt.Fprintln(stderr, "Error:", err)
			return 1
		}
		if !utf8.Valid(data) {
			fmt.Fprintln(stderr, "Error: stylesheet is not valid UTF-8")
			return 1
		}
		text := strings.TrimPrefix(string(data), "\ufeff")
		css = &text
	}
	stopSpinner := startSpinner(stderr, fmt.Sprintf("Processing %d log file(s)...", len(o.paths)))
	defer stopSpinner()
	events, warnings, total, err := parseLogs(o.paths, zone, o.maxChars, assume, o.detailed)
	stopSpinner()
	if err != nil {
		fmt.Fprintln(stderr, "Error:", err)
		return 1
	}
	for _, warning := range warnings {
		fmt.Fprintln(stderr, "Warning:", warning)
	}
	if len(events) == 0 {
		fmt.Fprintln(stderr, "Error: no timestamped WebLogic records recognized; no output written")
		return 2
	}
	page, err := makeHTML(events, o.paths, o.timezone, warnings, o.title, total, o.detailed, css)
	if err == nil {
		err = os.WriteFile(o.output, []byte(page), 0644)
	}
	if err != nil {
		fmt.Fprintln(stderr, "Error:", err)
		return 1
	}
	fmt.Fprintf(stdout, "Created %s: %d records, %d parsing warnings; timezone %s.\n", o.output, len(events), len(warnings), o.timezone)
	if o.serve {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := serveReport(ctx, o.listen, page, stdout); err != nil {
			fmt.Fprintln(stderr, "Error:", err)
			return 1
		}
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
