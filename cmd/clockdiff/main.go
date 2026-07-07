package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bougou/clockdiff"
	"golang.org/x/term"
)

const version = "0.1.0"

func main() {
	os.Args = normalizeArgs(os.Args)

	var (
		help       = flag.Bool("h", false, "print help and exit")
		showVer    = flag.Bool("V", false, "print version and exit")
		useIPOpts  = flag.Bool("o", false, "use IP TIMESTAMP with ICMP ECHO")
		useIPOpts1 = flag.Bool("o1", false, "use three-term IP TIMESTAMP with ICMP ECHO")
		timeFmt    = flag.String("time-format", "ctime", "output time format: ctime or iso")
		timeFmtT   = flag.String("T", "", "specify display time format: ctime or iso")
		isoFmt     = flag.Bool("I", false, "alias of --time-format=iso")
	)
	flag.Usage = func() { usage(os.Stderr) }
	flag.Parse()

	if *help {
		usage(os.Stdout)
		os.Exit(0)
	}
	if *showVer {
		fmt.Println("clockdiff", version)
		os.Exit(0)
	}
	if *useIPOpts && *useIPOpts1 {
		fmt.Fprintln(os.Stderr, "clockdiff: -o and -o1 are mutually exclusive")
		os.Exit(1)
	}

	args := flag.Args()
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "clockdiff: destination required")
		fmt.Fprintln(os.Stderr, "Try 'clockdiff -h' for more information.")
		os.Exit(1)
	}

	format := *timeFmt
	if *timeFmtT != "" {
		format = *timeFmtT
	}
	if *isoFmt {
		format = "iso"
	}
	switch format {
	case "ctime", "iso":
	default:
		fmt.Fprintf(os.Stderr, "clockdiff: invalid time-format argument: %s\n", format)
		os.Exit(1)
	}

	var measureOpts []clockdiff.Option
	switch {
	case *useIPOpts:
		measureOpts = append(measureOpts, clockdiff.WithMode(clockdiff.ModeIPTimestamp))
	case *useIPOpts1:
		measureOpts = append(measureOpts, clockdiff.WithMode(clockdiff.ModeIPTimestamp3))
	}
	interactive := isInteractive()
	if interactive {
		measureOpts = append(measureOpts, clockdiff.WithOnReply(func() {
			fmt.Print(".")
			_ = os.Stdout.Sync()
		}))
	}

	result, err := clockdiff.Measure(args[0], measureOpts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clockdiff: %v\n", err)
		os.Exit(1)
	}

	printResult(result, format, interactive)
}

func usage(w *os.File) {
	fmt.Fprintf(w, `Usage: clockdiff [-o] [-o1] [--time-format ctime|iso] [-V] destination

Measure clock difference between local host and destination with 1 msec
resolution using ICMP TIMESTAMP packets.

Options:
  without -o, use icmp timestamp only (see RFC 792)
  -o      use IP TIMESTAMP with ICMP ECHO
  -o1     use three-term IP TIMESTAMP with ICMP ECHO
  -T ctime|iso
          specify display time format, ctime is the default
  -time-format ctime|iso
          specify display time format
  -I      alias of --time-format=iso
  -h, --help
          print help and exit
  -V, --version
          print version and exit

destination
          DNS name or IP address

`)
}

func normalizeArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}
	out := make([]string, 0, len(args))
	out = append(out, args[0])
	for i := 1; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--help":
			out = append(out, "-h")
		case arg == "--version":
			out = append(out, "-V")
		case arg == "--time-format":
			if i+1 < len(args) {
				i++
				out = append(out, "-time-format", args[i])
			}
		case strings.HasPrefix(arg, "--time-format="):
			out = append(out, "-time-format", strings.TrimPrefix(arg, "--time-format="))
		case arg == "-o1":
			out = append(out, "-o1")
		case arg == "-o":
			out = append(out, "-o")
		default:
			out = append(out, arg)
		}
	}
	return out
}

func isInteractive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

func printResult(r *clockdiff.Result, format string, interactive bool) {
	deltaMS := r.Delta.Milliseconds()
	delta1MS := r.DeltaBest.Milliseconds()
	if !interactive {
		fmt.Printf("%d %d %d\n", time.Now().Unix(), deltaMS, delta1MS)
		return
	}

	ts := formatTime(time.Now(), format)
	fmt.Printf("\nhost=%s rtt=%d(%d)ms/%dms delta=%dms/%dms %s\n",
		r.Host,
		r.RTT.Milliseconds(),
		r.RTTSigma.Milliseconds(),
		r.MinRTT.Milliseconds(),
		deltaMS,
		delta1MS,
		ts,
	)
}

func formatTime(t time.Time, format string) string {
	local := t.Local()
	if format == "iso" {
		return local.Format("2006-01-02T15:04:05-07:00")
	}
	return local.Format("Mon Jan _2 15:04:05 2006")
}
