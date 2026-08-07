package usage_test

import (
	"strings"
	"testing"

	"github.com/bensyverson/timetattle/internal/usage"
)

func TestARecordThatSaysNothingEncodesToNoPayloadAtAll(t *testing.T) {
	encoded, err := usage.Record{}.Encode()
	if err != nil {
		t.Fatalf("encode an empty record: %v", err)
	}
	if encoded != "" {
		t.Fatalf("an empty record encoded to %q; a record that recorded nothing must produce no payload, "+
			"so the column stays NULL and reads as nothing-was-recorded rather than as an empty object", encoded)
	}
}

func TestASessionWithoutTokensEncodesTheSessionAlone(t *testing.T) {
	encoded, err := usage.Record{Session: "s-1"}.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if want := `{"session":"s-1"}`; encoded != want {
		t.Fatalf("encoded %q, want %q; a record with no usage figures must not carry a tokens object, "+
			"because an absent figure and a zero figure are different facts", encoded, want)
	}
}

func TestAStatedZeroSurvivesEncodingWhileAnUnstatedFigureIsOmitted(t *testing.T) {
	encoded, err := usage.Record{
		Session: "s-1",
		Tokens:  &usage.Tokens{Input: usage.Stated(0), Output: usage.Stated(214)},
	}.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(encoded, `"input":0`) {
		t.Fatalf("encoded %q, which lost a stated zero; observed-zero is an observation and must not "+
			"encode the same way as nothing-was-recorded", encoded)
	}
	if strings.Contains(encoded, "cache_read") {
		t.Fatalf("encoded %q, which invented a cache_read figure the record never stated", encoded)
	}
}

func TestAnEncodedRecordDecodesBackToItself(t *testing.T) {
	original := usage.Record{
		Session: "s-1",
		Tokens: &usage.Tokens{
			Input:         usage.Stated(2),
			Output:        usage.Stated(214),
			CacheRead:     usage.Stated(0),
			CacheCreation: usage.Stated(46724),
		},
	}
	encoded, err := original.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	back, err := usage.Decode(encoded)
	if err != nil {
		t.Fatalf("decode %q: %v", encoded, err)
	}
	if back.Session != original.Session {
		t.Fatalf("session came back as %q, want %q", back.Session, original.Session)
	}
	if back.Tokens == nil {
		t.Fatalf("tokens came back nil from %q", encoded)
	}
	for _, c := range []struct {
		name      string
		got, want *int64
	}{
		{"input", back.Tokens.Input, original.Tokens.Input},
		{"output", back.Tokens.Output, original.Tokens.Output},
		{"cache_read", back.Tokens.CacheRead, original.Tokens.CacheRead},
		{"cache_creation", back.Tokens.CacheCreation, original.Tokens.CacheCreation},
	} {
		if c.got == nil || *c.got != *c.want {
			t.Fatalf("%s came back as %v, want %d", c.name, c.got, *c.want)
		}
	}
}

func TestDecodingNothingIsNotAnErrorAndReportsNothing(t *testing.T) {
	back, err := usage.Decode("")
	if err != nil {
		t.Fatalf("decode an absent payload: %v; a row with no payload is a row that recorded nothing, "+
			"which is ordinary rather than broken", err)
	}
	if back.Session != "" || back.Tokens != nil {
		t.Fatalf("decoding nothing produced %+v", back)
	}
}

func TestDecodingSomethingThatIsNotAPayloadSaysSo(t *testing.T) {
	if _, err := usage.Decode("not json"); err == nil {
		t.Fatal("decoding a malformed payload succeeded; a payload that cannot be read is a fault to report, " +
			"never a record that quietly says nothing")
	}
}

// The paths are what the store's SQL extracts, and the field tags are what the
// encoder writes. Two spellings of one thing that nothing else compares would
// drift silently, and the failure is a sum that reads zero for ever.
func TestTheDeclaredJSONPathsMatchTheKeysTheEncoderWrites(t *testing.T) {
	encoded, err := usage.Record{
		Session: "s-1",
		Tokens: &usage.Tokens{
			Input:         usage.Stated(1),
			Output:        usage.Stated(2),
			CacheRead:     usage.Stated(3),
			CacheCreation: usage.Stated(4),
		},
	}.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for _, path := range []string{
		usage.PathSession, usage.PathInput, usage.PathOutput,
		usage.PathCacheRead, usage.PathCacheCreation,
	} {
		key := path[strings.LastIndex(path, ".")+1:]
		if !strings.Contains(encoded, `"`+key+`"`) {
			t.Fatalf("path %q names key %q, which the encoder never wrote into %q", path, key, encoded)
		}
	}
}
