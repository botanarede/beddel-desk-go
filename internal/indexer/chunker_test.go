package indexer

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestChunkKiroSession verifies that the Kiro schema parser produces
// one chunk per message, normalizes roles, preserves the message-level
// timestamp when present, and falls back to the file mtime otherwise.
func TestChunkKiroSession(t *testing.T) {
	path := filepath.Join("testdata", "kiro_session.json")
	chunks, warn, err := ChunkSession(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if warn != nil {
		t.Fatalf("unexpected warning: %+v", warn)
	}
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}
	wantRoles := []string{"user", "assistant", "system"}
	for i, c := range chunks {
		if c.ChunkIndex != i {
			t.Errorf("chunk %d: ChunkIndex = %d, want %d", i, c.ChunkIndex, i)
		}
		if c.Role != wantRoles[i] {
			t.Errorf("chunk %d: Role = %q, want %q", i, c.Role, wantRoles[i])
		}
		if c.SessionPath != path {
			t.Errorf("chunk %d: SessionPath = %q, want %q", i, c.SessionPath, path)
		}
		if c.Content == "" {
			t.Errorf("chunk %d: Content is empty", i)
		}
	}
	want, err := time.Parse(time.RFC3339, "2025-01-15T10:00:00Z")
	if err != nil {
		t.Fatalf("parse expected time: %v", err)
	}
	if !chunks[0].Timestamp.Equal(want) {
		t.Errorf("chunk 0 Timestamp = %v, want %v", chunks[0].Timestamp, want)
	}
	// The third message carries no timestamp, so it should fall back
	// to the file mtime (nonzero).
	if chunks[2].Timestamp.IsZero() {
		t.Error("chunk 2 Timestamp should fall back to file mtime, got zero value")
	}
}

// TestChunkGeminiSession verifies JSONL parsing, role mapping from
// "model" to "assistant", and dense chunk indices.
func TestChunkGeminiSession(t *testing.T) {
	path := filepath.Join("testdata", "gemini_session.jsonl")
	chunks, warn, err := ChunkSession(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if warn != nil {
		t.Fatalf("unexpected warning: %+v", warn)
	}
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}
	wantRoles := []string{"user", "assistant", "user"}
	for i, c := range chunks {
		if c.ChunkIndex != i {
			t.Errorf("chunk %d: ChunkIndex = %d, want %d", i, c.ChunkIndex, i)
		}
		if c.Role != wantRoles[i] {
			t.Errorf("chunk %d: Role = %q, want %q", i, c.Role, wantRoles[i])
		}
		if c.Timestamp.IsZero() {
			t.Errorf("chunk %d: Timestamp should fall back to mtime, got zero", i)
		}
	}
	if !strings.Contains(chunks[0].Content, "garbage collector") {
		t.Errorf("chunk 0 Content missing expected text: %q", chunks[0].Content)
	}
}

// TestChunkClaudeSession verifies the Claude Code schema: the top-level
// "conversation" array with type/text fields, and the "human" to "user"
// role mapping.
func TestChunkClaudeSession(t *testing.T) {
	path := filepath.Join("testdata", "claude_session.json")
	chunks, warn, err := ChunkSession(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if warn != nil {
		t.Fatalf("unexpected warning: %+v", warn)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if chunks[0].Role != "user" {
		t.Errorf("chunk 0 Role = %q, want %q", chunks[0].Role, "user")
	}
	if chunks[1].Role != "assistant" {
		t.Errorf("chunk 1 Role = %q, want %q", chunks[1].Role, "assistant")
	}
	if chunks[0].ChunkIndex != 0 || chunks[1].ChunkIndex != 1 {
		t.Errorf("chunk indices not dense: %d, %d", chunks[0].ChunkIndex, chunks[1].ChunkIndex)
	}
	// No timestamps in the fixture: both must fall back to mtime.
	for i, c := range chunks {
		if c.Timestamp.IsZero() {
			t.Errorf("chunk %d: Timestamp should fall back to mtime, got zero", i)
		}
	}
}

// TestChunkMalformed verifies the malformed fixture returns
// (nil, warning, nil) so the caller can keep indexing the rest of the
// backend.
func TestChunkMalformed(t *testing.T) {
	path := filepath.Join("testdata", "malformed.json")
	chunks, warn, err := ChunkSession(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chunks != nil {
		t.Errorf("expected nil chunks, got %d", len(chunks))
	}
	if warn == nil {
		t.Fatal("expected warning, got nil")
	}
	if warn.SessionPath != path {
		t.Errorf("warning SessionPath = %q, want %q", warn.SessionPath, path)
	}
	if !strings.HasPrefix(warn.Reason, "malformed:") {
		t.Errorf("warning Reason = %q, want prefix %q", warn.Reason, "malformed:")
	}
}

// TestChunkUnknownFormat verifies files the detector cannot classify
// return a warning rather than an error.
func TestChunkUnknownFormat(t *testing.T) {
	path := filepath.Join("testdata", "unknown_format.txt")
	chunks, warn, err := ChunkSession(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chunks != nil {
		t.Errorf("expected nil chunks, got %d", len(chunks))
	}
	if warn == nil {
		t.Fatal("expected warning, got nil")
	}
	if warn.Reason != "unknown format" {
		t.Errorf("warning Reason = %q, want %q", warn.Reason, "unknown format")
	}
}

// TestChunkMissingFile verifies that only true I/O errors surface as a
// non-nil err. A nonexistent path is an I/O error.
func TestChunkMissingFile(t *testing.T) {
	chunks, warn, err := ChunkSession(filepath.Join("testdata", "does_not_exist.json"))
	if err == nil {
		t.Fatal("expected err, got nil")
	}
	if chunks != nil {
		t.Errorf("expected nil chunks, got %d", len(chunks))
	}
	if warn != nil {
		t.Errorf("expected nil warning, got %+v", warn)
	}
}

// TestSplitLongMessageReconstructs feeds a 3000-word synthetic message
// through the splitter and verifies three things: at least two chunks
// are produced, every chunk stays within the token budget, and the
// concatenation reconstructs the original text after whitespace
// normalization.
func TestSplitLongMessageReconstructs(t *testing.T) {
	const wordCount = 3000
	words := make([]string, 0, wordCount)
	var b strings.Builder
	for i := 0; i < wordCount; i++ {
		if i > 0 {
			b.WriteByte(' ')
		}
		w := wordFor(i)
		words = append(words, w)
		b.WriteString(w)
	}
	original := b.String()
	pieces := splitContent(original)
	if len(pieces) < 2 {
		t.Fatalf("expected multiple chunks for %d words, got %d", wordCount, len(pieces))
	}
	for i, piece := range pieces {
		got := len(strings.Fields(piece))
		if got > tokenBudget {
			t.Errorf("piece %d has %d tokens, over budget %d", i, got, tokenBudget)
		}
	}
	var recombined strings.Builder
	for i, piece := range pieces {
		if i > 0 {
			recombined.WriteByte(' ')
		}
		recombined.WriteString(piece)
	}
	if normalizeWS(recombined.String()) != normalizeWS(original) {
		t.Error("reconstructed text differs from original after whitespace normalization")
	}
}

// TestSplitSingleLongToken verifies that a single word longer than the
// rune cap is split into multiple pieces so the tokenizer can always
// ingest every chunk.
func TestSplitSingleLongToken(t *testing.T) {
	word := strings.Repeat("a", singleWordRuneCap*3+7)
	pieces := splitContent(word)
	if len(pieces) < 3 {
		t.Fatalf("expected at least 3 pieces for oversized word, got %d", len(pieces))
	}
	for i, piece := range pieces {
		if runeLen(piece) > singleWordRuneCap {
			t.Errorf("piece %d exceeds rune cap: %d runes", i, runeLen(piece))
		}
	}
	var joined strings.Builder
	for _, p := range pieces {
		joined.WriteString(p)
	}
	if joined.String() != word {
		t.Error("concatenated pieces do not reconstruct the original word")
	}
}

// TestEmptyMessageProducesOneChunk verifies the contract that every
// message emits at least one chunk, so that ChunkIndex stays dense and
// the indexer can still record the session.
func TestEmptyMessageProducesOneChunk(t *testing.T) {
	messages := []rawMessage{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: ""},
		{Role: "user", Content: "third"},
	}
	chunks := buildChunks("/tmp/fake.json", time.Unix(1000, 0).UTC(), messages)
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}
	if chunks[1].Content != "" {
		t.Errorf("empty-message chunk Content = %q, want empty", chunks[1].Content)
	}
	for i, c := range chunks {
		if c.ChunkIndex != i {
			t.Errorf("chunk %d: ChunkIndex = %d, want %d", i, c.ChunkIndex, i)
		}
	}
}

// TestNormalizeRole exercises every documented mapping plus a couple
// of unknowns that should collapse to the empty string.
func TestNormalizeRole(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"user", "user"},
		{"USER", "user"},
		{"human", "user"},
		{"assistant", "assistant"},
		{"model", "assistant"},
		{"AI", "assistant"},
		{"system", "system"},
		{"", ""},
		{"tool", ""},
		{" Unknown ", ""},
	}
	for _, tc := range cases {
		if got := normalizeRole(tc.in); got != tc.want {
			t.Errorf("normalizeRole(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestResolveTimestampPreferences verifies the preference order: RFC
// 3339 string, Unix epoch number, then fallback. An unparseable string
// also falls back.
func TestResolveTimestampPreferences(t *testing.T) {
	fallback := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

	t.Run("RFC3339 string wins", func(t *testing.T) {
		got := resolveTimestamp("2025-06-01T12:34:56Z", fallback)
		want, _ := time.Parse(time.RFC3339, "2025-06-01T12:34:56Z")
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
	t.Run("Unix epoch float wins", func(t *testing.T) {
		got := resolveTimestamp(float64(1700000000), fallback)
		if got.Unix() != 1700000000 {
			t.Errorf("got unix %d, want 1700000000", got.Unix())
		}
	})
	t.Run("nil falls back", func(t *testing.T) {
		got := resolveTimestamp(nil, fallback)
		if !got.Equal(fallback) {
			t.Errorf("got %v, want fallback %v", got, fallback)
		}
	})
	t.Run("unparseable string falls back", func(t *testing.T) {
		got := resolveTimestamp("not a time", fallback)
		if !got.Equal(fallback) {
			t.Errorf("got %v, want fallback %v", got, fallback)
		}
	})
}

// wordFor produces a deterministic unique token for position i so the
// reconstruction test can compare input against output exactly.
func wordFor(i int) string {
	return "w" + itoa(i)
}

// itoa is a tiny unsigned int-to-string helper so the test file does
// not pull in strconv. Base 36 keeps tokens short and well within the
// tokenizer's rune budget.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	var buf [16]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = digits[i%36]
		i /= 36
	}
	return string(buf[pos:])
}

// normalizeWS collapses runs of whitespace into a single space and
// trims the ends so the reconstruction test tolerates an extra space
// that the joiner adds between pieces.
func normalizeWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
