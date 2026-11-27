// Command providerspike runs ADR 0042's spike items 1, 2, 3, 6 and 8 against
// real accounts, once the owner has made them (see ../../notes.md, "Runbook").
// It reads credentials only from ~/.config/permaily/ (mode 0600) or
// environment variables, never prints them, and writes its findings to
// results/ (git ignored) for the owner to summarise into notes.md.
//
// Nothing here runs in tests or in CI: every subcommand talks to a real
// provider.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"time"
)

const usage = `usage: providerspike [-results dir] <check> [flags]

checks (each needs the accounts notes.md's runbook lists):
  google-trip     [-label ticked|unticked]   item 1: consent trip, records the token response and its scope
  google-refresh                             item 1: one refresh; after day 7 in testing mode, expect invalid_grant
  gmail-baseline                             item 2: getProfile's historyId, kept as the baseline
  gmail-history   [-start id] [-twice]       item 2: history.list from the baseline (or -start)
  gmail-history-age                          item 2: the 1, 7 and 14-day readings of the baseline
  gmail-message   -id id | -q query          item 3: metadata fields, historyId vs baseline, raw streamed, UNREAD kept
                                             (-q picks the first match, e.g. -q 'larger:20M' or -q 'newer_than:1h')
  gmail-list      [-days n]                  item 3: messages.list with q=after:
  imap-host       -host id [-folder-writes] [-max-sessions n]   item 6: one host of the closed list
  http-versions   [-microsoft]               item 8: ALPN of each destination (no credentials)
`

func main() {
	top := flag.NewFlagSet("providerspike", flag.ExitOnError)
	dir := top.String("results", "results", "directory for the findings (git ignored)")
	top.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	_ = top.Parse(os.Args[1:])
	if top.NArg() == 0 {
		top.Usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	res := newResults(*dir)
	err := run(ctx, res, top.Arg(0), top.Args()[1:])
	if len(res.units) > 0 {
		units := map[string]any{}
		total := 0
		for k, v := range res.units {
			units[k] = v
			total += v
		}
		units["total"] = total
		_ = res.record("gmail-units", map[string]any{"documented_units_spent": units})
	}
	if err != nil {
		if errors.Is(err, errNoCredentials) {
			fmt.Fprintln(os.Stderr, "providerspike:", err, "(see notes.md, Runbook)")
		} else {
			fmt.Fprintln(os.Stderr, "providerspike:", err)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, res *results, check string, args []string) error {
	fs := flag.NewFlagSet(check, flag.ExitOnError)
	label := fs.String("label", "ticked", "google-trip: which consent choice this run made")
	start := fs.String("start", "", "gmail-history: startHistoryId (default: the baseline)")
	twice := fs.Bool("twice", false, "gmail-history: read the same start twice and compare")
	id := fs.String("id", "", "gmail-message: message id")
	query := fs.String("q", "", "gmail-message: pick the first message matching this Gmail query")
	days := fs.Int("days", 30, "gmail-list: days back")
	host := fs.String("host", "", "imap-host: identifier from the closed list")
	writes := fs.Bool("folder-writes", false, "imap-host: create, delete and remake a scratch folder")
	sessions := fs.Int("max-sessions", 0, "imap-host: try this many sessions at once")
	microsoft := fs.Bool("microsoft", false, "http-versions: also Microsoft's hosts (before Outlook's release, T201)")
	_ = fs.Parse(args)

	switch check {
	case "google-trip":
		return googleTrip(ctx, res, *label)
	case "google-refresh":
		return googleRefresh(ctx, res)
	case "gmail-baseline":
		return gmailBaseline(ctx, res)
	case "gmail-history":
		return gmailHistory(ctx, res, *start, *twice)
	case "gmail-history-age":
		return gmailHistoryAge(ctx, res)
	case "gmail-message":
		if *id == "" && *query == "" {
			return errors.New("gmail-message needs -id or -q")
		}
		if *id == "" {
			found, err := gmailFirst(ctx, res, *query)
			if err != nil {
				return err
			}
			*id = found
		}
		return gmailMessage(ctx, res, *id)
	case "gmail-list":
		return gmailList(ctx, res, *days)
	case "imap-host":
		return imapCheck(res, *host, imapOptions{folderWrites: *writes, maxSessions: *sessions})
	case "http-versions":
		hosts := googleHTTPHosts
		if *microsoft {
			hosts = append(append([]string(nil), hosts...), microsoftHTTPHosts...)
		}
		return httpVersions(res, hosts)
	default:
		return fmt.Errorf("unknown check %q", check)
	}
}

// httpVersions records what each destination negotiates when offered h2 and
// http/1.1 (item 8). It sends nothing after the handshake.
func httpVersions(res *results, hosts []string) error {
	out := map[string]any{}
	for _, h := range hosts {
		d := &net.Dialer{Timeout: 15 * time.Second}
		conn, err := tls.DialWithDialer(d, "tcp", h+":443", &tls.Config{ServerName: h, NextProtos: []string{"h2", "http/1.1"}, MinVersion: tls.VersionTLS12})
		if err != nil {
			out[h] = map[string]any{"error": "handshake failed"}
			continue
		}
		cs := conn.ConnectionState()
		out[h] = map[string]any{"alpn": cs.NegotiatedProtocol, "tls": tls.VersionName(cs.Version)}
		_ = conn.Close()
	}
	return res.record("http-versions", map[string]any{"hosts": out})
}
