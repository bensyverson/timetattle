package config

// This file WRITES the one section of the config that holds money, and it is
// the only file in this package that writes anything at all. Everything here
// exists to make four failures impossible rather than merely unlikely.
//
// # It writes through the reader, not beside it
//
// There is no second schema. An edit arrives as one of the two input types
// below, becomes an Engagement, is put through Engagement.validate — the same
// function the loader calls — and is marshalled through the same struct tags
// and the same Date.MarshalJSON the loader reads back. Before the file is
// replaced, what is about to be written is run through parse(), which is
// Load's own body: a write that could not be read back is refused and nothing
// is touched. Two schemas for the same money would agree for a while and then
// stop, silently.
//
// # Only the engagements key is rewritten
//
// The rest of the file is copied through member by member, with each value's
// bytes preserved exactly as they were written. This is not politeness about
// formatting. This package's defaults are PROVISIONAL AND UNFITTED — see
// Default, where every value is documented as a placeholder resting on nothing
// — and a writer that re-emitted the whole struct would freeze whichever
// values this build happens to hold into the user's own file. From then on the
// file would contradict every later change to those defaults, with no symptom.
// Sessions.PresenceSources is the sharpest case: it ships non-empty, so a file
// stating [] means "none" and an absent key means "unsaid", and only leaving
// the key exactly as it was keeps those two apart.
//
// # A blank input is an omitted key
//
// Absence is a pointer, not a zero, and this is the layer where a form's empty
// string would otherwise become a 0. The optional fields — currency and a
// contract's start date — are omitted from the file entirely when blank, never
// written as "" or 0. The required figures are REFUSED when blank rather than
// defaulted, because there is no defensible default for somebody's rate. And
// the fields belonging to the shape an entry did not choose are omitted too:
// an hourly entry carrying "contract_total": 0 declares BOTH shapes, which the
// loader refuses outright, so a writer that filled unused fields with zeros
// could never save anything at all.
//
// # One shape, unconstructible otherwise
//
// HourlyInput and ContractInput are separate types satisfying an interface
// whose only method is unexported. Nothing outside this package can implement
// a third, and neither of the two can express the other's fields — so an
// engagement declaring both shapes cannot be BUILT, rather than being built
// and then rejected. Switching an entry from one shape to the other replaces
// it whole; nothing of the old shape is left behind to be refused later.
//
// # Divergence is refused, never overwritten
//
// Hand-editing config.json stays legitimate — the validator's messages are
// written to guide exactly that — so an editor holding a copy of the file must
// not win an argument with the person who edited it in the meantime.
//
// The check is a BYTE COMPARISON of a re-read against what this editor last
// saw, taken immediately before the write. Mtime was rejected: its resolution
// is a second on some filesystems, it can go backwards, and a comparison that
// can silently pass is worth nothing here. Content hashing is the same test
// with a collision risk added and nothing gained on a file this size. Three-way
// merging was rejected as well: merging money edits nobody asked to merge is a
// worse failure than refusing.
//
// WHEN DIVERGENCE IS FOUND, NOTHING IS WRITTEN and Save returns
// ErrConfigChanged. The file on disk is left exactly as the hand-editor left
// it; the pending edit stays in memory, where the caller can re-open, show what
// changed and offer the edit again. A file that appeared since an Open on a
// missing path counts as divergence too — absence is not emptiness, and a file
// this editor never read is a file it must not replace.
//
// One window remains and is stated rather than papered over: between the
// re-read and the rename, another writer could land. Closing it would need a
// lock file, and a stale lock on a single-user local tool costs more than the
// microseconds it removes. The replacement itself is atomic — a temporary file
// in the same directory, fsynced, then renamed — so no reader ever sees a
// half-written config, and the loser of that race loses a whole edit rather
// than half a file.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
)

// ErrConfigChanged reports that the file changed underneath an editor, so
// nothing was written. Callers test for it with errors.Is and re-open.
var ErrConfigChanged = errors.New("the config changed since it was read")

// EngagementInput is a submitted engagement, before it is anything this
// package would store.
//
// The interface is closed: its only method is unexported, so HourlyInput and
// ContractInput are the only two implementations that can ever exist. That is
// the one-shape rule made structural — there is no type here that can hold an
// hourly rate and a contract total at the same time, so the invalid state
// cannot be constructed and then have to be caught.
type EngagementInput interface {
	// engagement parses the submission into the stored shape. It does not
	// validate; Editor.Set does that, through the loader's own validator.
	engagement() (Engagement, error)
}

// HourlyInput is shape (a) as it arrives from an editing form: strings,
// because a text box contains characters and one of the characters it can
// contain is none at all. Parsing here rather than in the caller is
// deliberate — a caller that converted "" to 0 on its way in would have
// destroyed the distinction before this package ever saw it.
type HourlyInput struct {
	// Currency is the label printed before an amount. Blank means unstated
	// and writes no key. It is NOT trimmed: "EUR " is a legitimate label
	// whose trailing space is printed, so trimming would silently change what
	// somebody typed.
	Currency string
	// Rate is the nominal hourly rate. Required: blank is refused rather than
	// read as zero.
	Rate string
	// BudgetedHours is how many hours Period is meant to hold. Required.
	BudgetedHours string
	// Period is what the budget recurs over. It is the config's own type, so
	// a form offers exactly the two values the loader accepts.
	Period BudgetPeriod
}

// ContractInput is shape (b) as it arrives from an editing form.
type ContractInput struct {
	// Currency is as HourlyInput's.
	Currency string
	// ContractTotal is the whole fixed value of the engagement. Required.
	ContractTotal string
	// ExpectedHours is what that total was priced against. Required.
	ExpectedHours string
	// Started is the civil day the contract began, written the one way the
	// file spells a date. Blank means unstated and writes no key.
	Started string
}

func (in HourlyInput) engagement() (Engagement, error) {
	rate, err := requiredFigure("rate", in.Rate)
	if err != nil {
		return Engagement{}, err
	}
	hours, err := requiredFigure("budgeted_hours", in.BudgetedHours)
	if err != nil {
		return Engagement{}, err
	}
	return Engagement{
		Currency:      optionalCurrency(in.Currency),
		Rate:          rate,
		BudgetedHours: hours,
		Period:        in.Period,
	}, nil
}

func (in ContractInput) engagement() (Engagement, error) {
	total, err := requiredFigure("contract_total", in.ContractTotal)
	if err != nil {
		return Engagement{}, err
	}
	hours, err := requiredFigure("expected_hours", in.ExpectedHours)
	if err != nil {
		return Engagement{}, err
	}
	started, err := optionalDate("started", in.Started)
	if err != nil {
		return Engagement{}, err
	}
	return Engagement{
		Currency:      optionalCurrency(in.Currency),
		ContractTotal: total,
		ExpectedHours: hours,
		Started:       started,
	}, nil
}

// requiredFigure reads a figure the chosen shape cannot do without.
//
// A BLANK BOX IS NOT A ZERO. Defaulting one to 0 would either be refused by
// Validate — which is the lucky case, because at least it says so — or, on a
// field Validate cannot see, would report somebody's money wrong for ever. So
// blank is refused here, by name, before anything is stored.
func requiredFigure(key, text string) (*float64, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, fmt.Errorf("%s was left blank; it is required by this shape, and a blank figure is "+
			"not a zero — write the number, or choose the other shape if this one is not what is owed", key)
	}
	v, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return nil, fmt.Errorf("%s is %q, which is not a number", key, trimmed)
	}
	return &v, nil
}

// optionalCurrency is the currency label, or nil for "nobody said".
//
// Nil is the whole point: Validate refuses a currency written as "", because a
// key written empty is a person having tried to say something and said
// nothing. An unstated currency has to be an ABSENT KEY or it cannot be saved
// at all.
func optionalCurrency(text string) *string {
	if text == "" {
		return nil
	}
	return &text
}

// optionalDate reads a civil day through Date's own parser, so the file has
// exactly one accepted spelling and one error message for it.
func optionalDate(key, text string) (Date, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return Date{}, nil
	}
	var d Date
	if err := d.UnmarshalJSON([]byte(strconv.Quote(trimmed))); err != nil {
		return Date{}, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}

// Editor is a config file open for editing: the configuration it parsed, and
// the exact bytes it parsed them from.
//
// The bytes are what makes the write safe. They are what an untouched section
// is copied through from, and they are what a re-read is compared against
// before anything is replaced.
type Editor struct {
	path string
	cfg  *Config
	seen seen
}

// seen is the file as this editor last observed it — after Open, and again
// after each successful Save.
type seen struct {
	// exists distinguishes "there was no file" from "there was an empty
	// file". Collapsing those two would let an editor opened on a missing
	// path silently replace a file somebody created in the meantime.
	exists bool
	raw    []byte
	mode   fs.FileMode
}

// Open reads path for editing.
//
// A file that is not there is not an error, exactly as it is not one for Load:
// it is an unconfigured machine, and adding the first engagement to one must
// not require creating a file by hand first. Every other failure is returned,
// and a file that does not parse is refused here rather than being silently
// replaced by whatever this editor writes next.
func Open(path string) (*Editor, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Editor{path: path, cfg: Default()}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the config at %s: %w", path, err)
	}
	cfg, err := parse(raw, path)
	if err != nil {
		return nil, err
	}
	mode := fs.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	return &Editor{path: path, cfg: cfg, seen: seen{exists: true, raw: raw, mode: mode}}, nil
}

// Path is the file being edited.
func (ed *Editor) Path() string { return ed.path }

// Config is the configuration as it currently stands in memory, for browsing.
// Edits made to it directly are not written: only the engagements section is,
// and only through Set and Remove.
func (ed *Editor) Config() *Config { return ed.cfg }

// SetClient adds or replaces the engagement for a client. It REPLACES: an
// entry that was hourly and is now a contract keeps nothing of its old shape,
// because an entry carrying both is refused at load.
func (ed *Editor) SetClient(name string, in EngagementInput) error {
	return ed.set(&ed.cfg.Engagements.Clients, "engagements.clients", name, in)
}

// SetProject adds or replaces the engagement for a project.
func (ed *Editor) SetProject(name string, in EngagementInput) error {
	return ed.set(&ed.cfg.Engagements.Projects, "engagements.projects", name, in)
}

// RemoveClient drops a client's engagement and reports whether there was one.
func (ed *Editor) RemoveClient(name string) bool {
	return remove(&ed.cfg.Engagements.Clients, name)
}

// RemoveProject drops a project's engagement and reports whether there was one.
func (ed *Editor) RemoveProject(name string) bool {
	return remove(&ed.cfg.Engagements.Projects, name)
}

// set parses, validates through the loader's own validator, and only then
// stores. A refused edit changes nothing at all — the caller can show the
// message beside the form the person is still looking at.
func (ed *Editor) set(into *map[string]Engagement, section, name string, in EngagementInput) error {
	if in == nil {
		return fmt.Errorf("%s[%q] was given no engagement to store", section, name)
	}
	e, err := in.engagement()
	if err != nil {
		return fmt.Errorf("%s[%q]: %w", section, name, err)
	}
	if err := e.validate(section, name); err != nil {
		return err
	}
	if *into == nil {
		*into = make(map[string]Engagement)
	}
	(*into)[name] = e
	return nil
}

// remove deletes an entry, and empties the map to nil when it was the last
// one. Nil rather than empty matters: an empty map and an absent one must
// marshal the same way, or a save would write a section the reader would then
// hand back as something else.
func remove(from *map[string]Engagement, name string) bool {
	if _, ok := (*from)[name]; !ok {
		return false
	}
	delete(*from, name)
	if len(*from) == 0 {
		*from = nil
	}
	return true
}

// Save writes the engagements section back, or explains why it did not.
//
// Nothing is written unless every one of these holds: the whole configuration
// validates, what would be written parses back into the same engagements, and
// the file on disk is still the one this editor read. A save that would produce
// the bytes already on disk writes nothing at all, so an unchanged save does
// not touch the file's mtime or replace a hand-written layout.
func (ed *Editor) Save() error {
	if err := ed.cfg.Validate(); err != nil {
		return fmt.Errorf("refusing to write %s: %w", ed.path, err)
	}
	rendered, err := ed.render()
	if err != nil {
		return fmt.Errorf("refusing to write %s: %w", ed.path, err)
	}
	// WHAT IS ABOUT TO BE WRITTEN IS READ BACK FIRST, through Load's own body.
	// A config this build cannot load is a config no command can run against,
	// including the scheduled harvest — so it must fail here, where nothing has
	// been replaced, rather than on disk.
	readback, err := parse(rendered, ed.path)
	if err != nil {
		return fmt.Errorf("refusing to write %s: what this would write cannot be read back: %w", ed.path, err)
	}
	// No test can reach this branch while the writer and the reader agree,
	// which is the point of it: it catches a disagreement neither side would
	// notice on its own. Measured by introducing one — Date.String swapped to
	// print month and day the wrong way round, so a start date of 2026-01-02
	// is written as "2026-02-01" and parses back, cleanly and wrongly, as the
	// 1st of February. Both halves accept it; only the comparison sees it, and
	// Save refuses with the message below rather than storing a contract that
	// began on a day nobody chose.
	if !reflect.DeepEqual(readback.Engagements, ed.cfg.Engagements) {
		return fmt.Errorf("refusing to write %s: what this would write reads back as a different set of "+
			"engagements, which means this package's writer and its reader disagree", ed.path)
	}

	changed, err := ed.diverged()
	if err != nil {
		return fmt.Errorf("re-read the config at %s before writing it: %w", ed.path, err)
	}
	if changed {
		return fmt.Errorf("%s was edited since it was read, so nothing was written: %w; "+
			"re-open it to see the current contents, then make the change again", ed.path, ErrConfigChanged)
	}

	if ed.seen.exists && bytes.Equal(rendered, ed.seen.raw) {
		return nil
	}
	if !ed.seen.exists && ed.cfg.Engagements.IsEmpty() {
		// An unconfigured machine with no engagement to add has nothing to say,
		// and a file holding {} says it no better than no file does — Load
		// reads either as the defaults. Creating one would leave a reader
		// wondering what it was for.
		return nil
	}
	mode := ed.seen.mode
	if !ed.seen.exists {
		mode = 0o600
	}
	if err := replaceFile(ed.path, rendered, mode); err != nil {
		return err
	}
	ed.seen = seen{exists: true, raw: rendered, mode: mode}
	return nil
}

// diverged reports whether the file is no longer what this editor last saw.
func (ed *Editor) diverged() (bool, error) {
	raw, err := os.ReadFile(ed.path)
	if errors.Is(err, fs.ErrNotExist) {
		// A file that has been deleted since it was read has diverged as
		// surely as one that was edited: writing would resurrect a file
		// somebody removed on purpose.
		return ed.seen.exists, nil
	}
	if err != nil {
		return false, err
	}
	if !ed.seen.exists {
		return true, nil
	}
	return !bytes.Equal(raw, ed.seen.raw), nil
}

// render is the whole file as it should now stand: the engagements section
// marshalled through this package's own tags, dropped into the member list the
// file already had.
func (ed *Editor) render() ([]byte, error) {
	if ed.cfg.Engagements.IsEmpty() {
		// An engagements section with nothing in it says exactly what no
		// section at all says, and the shorter of the two is the one a person
		// would have written.
		return replaceMember(ed.seen.raw, "engagements", nil)
	}
	section, err := json.Marshal(ed.cfg.Engagements)
	if err != nil {
		return nil, fmt.Errorf("marshal the engagements section: %w", err)
	}
	return replaceMember(ed.seen.raw, "engagements", section)
}

// member is one key and its value, with the value's bytes exactly as they were
// written.
type member struct {
	key   string
	value json.RawMessage
}

// replaceMember rewrites one top-level member of a JSON object and copies
// every other member through unchanged. A nil value removes the member.
//
// Only whitespace is normalised — to two spaces, so the file stays something a
// person can hand-edit. Every value's own spelling survives, numbers included,
// because each is copied as the bytes it was read as rather than re-encoded
// from a parsed float.
func replaceMember(raw []byte, key string, value json.RawMessage) ([]byte, error) {
	members, err := topLevelMembers(raw)
	if err != nil {
		return nil, err
	}

	var out bytes.Buffer
	out.WriteString("{")
	written := 0
	replaced := false
	for _, m := range members {
		v := m.value
		if m.key == key {
			// JSON permits a repeated key and Go's decoder merges the objects
			// under it, so a hand-edited file can carry two `engagements`
			// blocks and still load. The rewritten section replaces the FIRST
			// of them and the rest are dropped: writing the new section into
			// every slot would leave a file stating the same thing twice, which
			// stops being the same thing the moment somebody edits one copy.
			if replaced {
				continue
			}
			replaced = true
			if value == nil {
				continue
			}
			v = value
		}
		if err := writeMember(&out, &written, m.key, v); err != nil {
			return nil, err
		}
	}
	if !replaced && value != nil {
		if err := writeMember(&out, &written, key, value); err != nil {
			return nil, err
		}
	}
	if written > 0 {
		out.WriteString("\n")
	}
	out.WriteString("}\n")
	return out.Bytes(), nil
}

// writeMember appends one indented `"key": value` to out.
func writeMember(out *bytes.Buffer, written *int, key string, value json.RawMessage) error {
	if *written > 0 {
		out.WriteString(",")
	}
	out.WriteString("\n  ")
	encoded, err := json.Marshal(key)
	if err != nil {
		return fmt.Errorf("marshal the key %q: %w", key, err)
	}
	out.Write(encoded)
	out.WriteString(": ")

	var compact bytes.Buffer
	if err := json.Compact(&compact, value); err != nil {
		return fmt.Errorf("the value of %q is not valid JSON: %w", key, err)
	}
	if err := json.Indent(out, compact.Bytes(), "  ", "  "); err != nil {
		return fmt.Errorf("indent the value of %q: %w", key, err)
	}
	*written++
	return nil
}

// topLevelMembers reads the file's top-level object in the order it was
// written. Order is preserved because the file is somebody's to read: a writer
// that reordered the keys every time would make every edit look like a whole
// new file to a diff.
func topLevelMembers(raw []byte) ([]member, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	open, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("read the config as JSON: %w", err)
	}
	if delim, ok := open.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("the config is %v where a JSON object was expected", open)
	}
	var members []member
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("read the config as JSON: %w", err)
		}
		name, ok := key.(string)
		if !ok {
			return nil, fmt.Errorf("the config has a key that is not a string: %v", key)
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, fmt.Errorf("read the value of %q: %w", name, err)
		}
		members = append(members, member{key: name, value: value})
	}
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("read the config as JSON: %w", err)
	}
	return members, nil
}

// replaceFile puts data at path atomically: a temporary file beside it,
// flushed to the disk, then renamed over. A reader that opens the config
// halfway through sees the old file or the new one and never a truncated one,
// which matters because every command reads this file and one of them runs on
// a schedule.
func replaceFile(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	// The directory is ~/.timetattle, which holds the store; it usually
	// exists. Creating it is what lets the first engagement be added on a
	// machine nobody has configured.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return fmt.Errorf("write a new config beside %s: %w", path, err)
	}
	name := tmp.Name()
	defer os.Remove(name) // a no-op once the rename has happened

	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("set the mode of the new config: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write the new config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("flush the new config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close the new config: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("put the new config at %s: %w", path, err)
	}
	return nil
}
