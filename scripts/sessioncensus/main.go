// Command sessioncensus counts what the transcript corpus can say about
// sessions and tokens, and is the tool that reproduces every figure quoted in
// migration 0014, in internal/usage and in
// project/2026-08-07-transcript-payloads.md.
//
// # Why this exists
//
// The decision those documents record — that a transcript record's session and
// token figures belong in event.payload, outside event_identity — rests on
// numbers about a real corpus: how many records name a session, how many
// sessions touch more than one working directory, how large the four token
// figures are relative to each other, and how often two readings of ONE record
// disagree. A decision argued from numbers nobody can re-run is an assertion.
//
//	go run ./scripts/sessioncensus
//	go run ./scripts/sessioncensus -root /path/to/projects
//
// # Two passes, because they are two different questions
//
// THE COLLECTOR PASS runs internal/transcripts itself and counts what it
// actually emitted. Re-implementing the collector's rules here would measure
// this file's idea of them, which is the mistake scripts/transcriptreconcile
// documents at length: the tempting shorthand and the real rule are close, and
// quoting one as the other attributes a number to a rule that did not produce
// it.
//
// THE RAW PASS reads the lines on disk and counts how often one uuid is read
// more than once and whether the two readings agree. It cannot go through the
// collector, because resolving that disagreement is precisely what the
// collector does before anything downstream sees it. It is a census of FILES
// and is labelled as such wherever it is quoted.
//
// # What it deliberately does not print
//
// No path, no directory name, no session id. The corpus lives under
// ~/.claude/projects, whose directory names encode real client working
// directories, and this output is meant to be quotable in a published document.
// Counts are safe; names are the thing three published leaks were made of.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/bensyverson/timetattle/internal/transcripts"
	"github.com/bensyverson/timetattle/internal/usage"
)

func main() {
	root := flag.String("root", "", "the transcripts root to read (default: this user's ~/.claude/projects)")
	flag.Parse()

	if err := run(*root); err != nil {
		fmt.Fprintln(os.Stderr, "sessioncensus:", err)
		os.Exit(1)
	}
}

func run(root string) error {
	if root == "" {
		resolved, err := transcripts.DefaultRoot()
		if err != nil {
			return err
		}
		root = resolved
	}

	started := time.Now()
	events, err := transcripts.NewReader(root).Collect(context.Background(), time.Time{})
	if err != nil {
		return fmt.Errorf("collect: %w", err)
	}

	// Sessions are counted by the locators their records resolved to, because
	// that is what a per-scope session count is made of. A locator is a
	// working directory here and not yet a project: attribution is the
	// store's pass, and this tool deliberately stops short of it so that its
	// figures do not depend on one machine's registry.
	sessions := map[string]map[string]bool{}
	withSession, withTokens := 0, 0
	var input, output, cacheRead, cacheCreation int64
	stated := map[string]int{}

	for _, e := range events {
		rec, err := usage.Decode(e.Payload)
		if err != nil {
			return fmt.Errorf("a payload the collector produced cannot be read back: %w", err)
		}
		if rec.Session != "" {
			withSession++
			locators, ok := sessions[rec.Session]
			if !ok {
				locators = map[string]bool{}
				sessions[rec.Session] = locators
			}
			locators[e.Locator] = true
		}
		if rec.Tokens == nil {
			continue
		}
		withTokens++
		for _, f := range []struct {
			name   string
			figure *int64
		}{
			{"input", rec.Tokens.Input},
			{"output", rec.Tokens.Output},
			{"cache_read", rec.Tokens.CacheRead},
			{"cache_creation", rec.Tokens.CacheCreation},
		} {
			if f.figure != nil {
				stated[f.name]++
			}
		}
		input += value(rec.Tokens.Input)
		output += value(rec.Tokens.Output)
		cacheRead += value(rec.Tokens.CacheRead)
		cacheCreation += value(rec.Tokens.CacheCreation)
	}

	spanning := 0
	for _, locators := range sessions {
		if len(locators) > 1 {
			spanning++
		}
	}

	raw, err := rawCensus(root)
	if err != nil {
		return err
	}

	fmt.Printf("root read in %s\n\n", time.Since(started).Round(time.Millisecond))
	fmt.Println("COLLECTOR PASS (internal/transcripts, after its own replay resolution)")
	fmt.Printf("  events emitted                        %d\n", len(events))
	fmt.Printf("  carrying a session                    %d\n", withSession)
	fmt.Printf("  naming no session                     %d\n", len(events)-withSession)
	fmt.Printf("  distinct sessions                     %d\n", len(sessions))
	fmt.Printf("  sessions spanning >1 working dir      %d\n", spanning)
	fmt.Printf("  carrying token figures                %d\n", withTokens)
	for _, name := range []string{"input", "output", "cache_read", "cache_creation"} {
		fmt.Printf("    %-14s stated by            %d\n", name, stated[name])
	}
	fmt.Printf("  input tokens                          %d\n", input)
	fmt.Printf("  output tokens                         %d\n", output)
	fmt.Printf("  cache read tokens                     %d\n", cacheRead)
	fmt.Printf("  cache creation tokens                 %d\n", cacheCreation)
	fmt.Println()
	fmt.Println("RAW PASS (lines on disk, before any rule of the collector's)")
	fmt.Printf("  files                                 %d\n", raw.files)
	fmt.Printf("  timed records                         %d\n", raw.timed)
	fmt.Printf("  repeat readings of one uuid           %d\n", raw.repeats)
	fmt.Printf("    disagreeing about the session       %d\n", raw.differingSession)
	fmt.Printf("    disagreeing about the token figures %d\n", raw.differingUsage)
	fmt.Println()
	fmt.Println("A repeat reading that disagrees is the case event_identity cannot hold: in the key,")
	fmt.Println("each one is a second row for a record that happened once. See migration 0014.")
	return nil
}

func value(figure *int64) int64 {
	if figure == nil {
		return 0
	}
	return *figure
}

type rawFigures struct {
	files            int
	timed            int
	repeats          int
	differingSession int
	differingUsage   int
}

// rawLine is the projection the raw pass needs. It is not the collector's
// record type, and it is not meant to be: this pass answers what the FILES say
// before any rule is applied to them.
type rawLine struct {
	Timestamp string `json:"timestamp"`
	UUID      string `json:"uuid"`
	SessionID string `json:"sessionId"`
	Message   *struct {
		Usage *struct {
			Input         *int64 `json:"input_tokens"`
			Output        *int64 `json:"output_tokens"`
			CacheRead     *int64 `json:"cache_read_input_tokens"`
			CacheCreation *int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// reading is what one line said about a record, reduced to the facts two
// readings could disagree about.
type reading struct {
	session string
	tokens  string
}

func rawCensus(root string) (rawFigures, error) {
	var out rawFigures
	seen := map[string]reading{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// One unreadable directory loses its own files and not the
			// census, the same tolerance the collector gives.
			if path == root {
				return err
			}
			return nil
		}
		if d.IsDir() || filepath.Ext(d.Name()) != ".jsonl" {
			return nil
		}
		out.files++

		f, openErr := os.Open(path)
		if openErr != nil {
			return nil
		}
		defer f.Close()

		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			var rec rawLine
			if json.Unmarshal(line, &rec) != nil {
				continue
			}
			if rec.Timestamp == "" || rec.UUID == "" {
				continue
			}
			out.timed++
			now := reading{session: rec.SessionID, tokens: tokensOf(rec)}
			before, repeated := seen[rec.UUID]
			if !repeated {
				seen[rec.UUID] = now
				continue
			}
			out.repeats++
			if before.session != now.session {
				out.differingSession++
			}
			if before.tokens != now.tokens {
				out.differingUsage++
			}
		}
		return nil
	})
	if err != nil {
		return rawFigures{}, fmt.Errorf("walk %s: %w", root, err)
	}
	return out, nil
}

// tokensOf renders a line's four figures as a comparable string. An absent
// figure and a zero figure render differently, because two readings that
// disagree about which of those a record stated disagree.
func tokensOf(rec rawLine) string {
	if rec.Message == nil || rec.Message.Usage == nil {
		return "none"
	}
	u := rec.Message.Usage
	return fmt.Sprintf("%s/%s/%s/%s", figure(u.Input), figure(u.Output), figure(u.CacheRead), figure(u.CacheCreation))
}

func figure(n *int64) string {
	if n == nil {
		return "-"
	}
	return fmt.Sprint(*n)
}
