package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

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
