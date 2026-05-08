package embedding

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// fakeTokenizer is the unit-test double used by the embedder tests. It
// implements the Tokenizer interface without calling into the sugarme
// library so tests run without an on-disk tokenizer.json.
type fakeTokenizer struct {
	// idsFor maps text -> input ids. Missing entries produce a
	// default id sequence of length defaultLen so the embedder tests
	// can build predictable batches.
	idsFor map[string][]int64

	// defaultLen is used when idsFor has no entry for a given text.
	// If zero it defaults to 3 (to represent [CLS], body, [SEP]).
	defaultLen int

	closed bool

	// tokenizeErr is returned from every Tokenize call when set,
	// letting tests exercise the error path.
	tokenizeErr error
}

func (f *fakeTokenizer) Tokenize(text string) ([]int64, []int64, error) {
	if f.closed {
		return nil, nil, ErrTokenizerClosed
	}
	if f.tokenizeErr != nil {
		return nil, nil, f.tokenizeErr
	}
	if ids, ok := f.idsFor[text]; ok {
		mask := make([]int64, len(ids))
		for i := range mask {
			mask[i] = 1
		}
		return ids, mask, nil
	}
	n := f.defaultLen
	if n == 0 {
		n = 3
	}
	ids := make([]int64, n)
	mask := make([]int64, n)
	for i := 0; i < n; i++ {
		ids[i] = int64(i + 1)
		mask[i] = 1
	}
	return ids, mask, nil
}

func (f *fakeTokenizer) Close() error {
	f.closed = true
	return nil
}

func TestFakeTokenizerHonoursCloseContract(t *testing.T) {
	ft := &fakeTokenizer{}
	if _, _, err := ft.Tokenize("hello"); err != nil {
		t.Fatalf("unexpected error before close: %v", err)
	}
	if err := ft.Close(); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}
	_, _, err := ft.Tokenize("hello")
	if !errors.Is(err, ErrTokenizerClosed) {
		t.Fatalf("expected ErrTokenizerClosed, got %v", err)
	}
}

func TestToInt64SliceWidens(t *testing.T) {
	got := toInt64Slice([]int{1, 2, 3})
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("unexpected widened slice: %v", got)
	}
	if toInt64Slice(nil) != nil {
		t.Fatalf("nil input should produce nil output")
	}
}

// TestNewTokenizerRejectsEmptyPath asserts that a missing path is
// reported with a clear error rather than causing a panic deep inside
// the sugarme library.
func TestNewTokenizerRejectsEmptyPath(t *testing.T) {
	_, err := NewTokenizer("")
	if err == nil {
		t.Fatal("expected error for empty path")
	}
	if !strings.Contains(err.Error(), "tokenizer json path") {
		t.Fatalf("error should mention path argument, got %v", err)
	}
}

// TestNewTokenizerRejectsMissingFile surfaces the underlying wrapped
// error for a missing tokenizer.json file.
func TestNewTokenizerRejectsMissingFile(t *testing.T) {
	_, err := NewTokenizer("/definitely/does/not/exist/tokenizer.json")
	if err == nil {
		t.Fatal("expected error for missing tokenizer.json")
	}
}

// TestTokenizerE2E exercises the real sugarme loader against a
// tokenizer.json provided by the developer via
// BEDDEL_TOKENIZER_JSON. It is skipped unless BEDDEL_EMBED_E2E=1 so
// that CI, which has no such file available, stays green.
func TestTokenizerE2E(t *testing.T) {
	if os.Getenv("BEDDEL_EMBED_E2E") != "1" {
		t.Skip("set BEDDEL_EMBED_E2E=1 to run end-to-end tokenizer tests")
	}
	path := os.Getenv("BEDDEL_TOKENIZER_JSON")
	if path == "" {
		t.Skip("set BEDDEL_TOKENIZER_JSON to the path of a real tokenizer.json")
	}

	tok, err := NewTokenizer(path)
	if err != nil {
		t.Fatalf("NewTokenizer: %v", err)
	}
	t.Cleanup(func() { _ = tok.Close() })

	cases := []struct {
		name string
		text string
	}{
		{name: "short english", text: "hello world"},
		{name: "unicode surrogates", text: "emoji test \U0001F600 \U0001F4A9 done"},
		{name: "empty", text: ""},
		{name: "over-long", text: strings.Repeat("word ", 1024)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ids, mask, err := tok.Tokenize(tc.text)
			if err != nil {
				t.Fatalf("Tokenize: %v", err)
			}
			if len(ids) != len(mask) {
				t.Fatalf("ids/mask length mismatch: %d vs %d", len(ids), len(mask))
			}
			if len(ids) > MaxSequenceLength {
				t.Fatalf("ids exceeded MaxSequenceLength: %d", len(ids))
			}
		})
	}
}
