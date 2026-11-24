package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"
	"unicode/utf8"

	"wls-timeline/internal/report"
	"wls-timeline/internal/timeline"
)

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

// Run executes the command and returns its process exit status.
func Run(args []string, stdout, stderr io.Writer) int {
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
	events, warnings, total, err := timeline.ParseLogs(o.paths, zone, o.maxChars, assume, o.detailed)
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
	page, err := report.Generate(events, o.paths, o.timezone, warnings, o.title, total, o.detailed, css)
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
