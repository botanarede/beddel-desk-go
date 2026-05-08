// Package indexer turns local agent session files into small,
// deterministically identified chunks that the semantic search pipeline
// can embed and index. The chunker is pure CPU: it never touches the
// network, the embedder, or any database, and every chunk carries the
// source path and chunk index required by the traceability rule in
// docs/prd/02-epic-v2-semantic-search.md (functional requirement 12).
package indexer

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// tokenBudget is the approximate upper bound on whitespace-separated
// tokens per chunk. It is a ceiling, not a precise count: the tokenizer
// used by the embedder (see internal/embedding.MaxSequenceLength = 256
// real tokens) performs the authoritative truncation before embedding.
// We pick a larger number here so the final truncation is usually a
// no-op for well-shaped messages while the chunker still guarantees
// termination on pathological input.
const tokenBudget = 400

// singleWordRuneCap bounds a single whitespace token by rune count so
// that a pasted blob (for example a huge base64 payload) cannot produce
// a chunk the tokenizer cannot ingest. The factor of four is a
// conservative average of characters per WordPiece token in English.
const singleWordRuneCap = tokenBudget * 4

// Chunk is the unit the indexer feeds to the embedder. Every chunk
// carries enough traceability for a search result to be opened back to
// its source file and message role.
type Chunk struct {
	SessionPath string
	ChunkIndex  int
	Role        string // "user" | "assistant" | "system" | ""
	Content     string
	Timestamp   time.Time
}

// ChunkWarning describes a non-fatal parse problem. A non-nil warning
// does not imply an error: the caller should keep indexing the rest of
// the backend and surface the warning to the UI.
type ChunkWarning struct {
	SessionPath string
	Reason      string
}

// rawMessage is the internal, format-agnostic representation produced
// by every per-format parser. The shared chunking pass consumes it.
type rawMessage struct {
	Role      string
	Content   string
	TimeValue any // RFC 3339 string, JSON number (float64), or nil.
}

// ChunkSession parses a single session file and returns its chunks in
// order. A non-nil warning does NOT imply an error; malformed files
// return (nil, warning, nil) so callers can keep indexing the rest of
// the backend. Only I/O errors (os.Open / os.Stat / io.ReadAll) surface
// as a non-nil err.
func ChunkSession(path string) ([]Chunk, *ChunkWarning, error) {
	data, mtime, err := readAll(path)
	if err != nil {
		return nil, nil, err
	}
	messages, warn := detectAndParse(path, data)
	if warn != nil {
		return nil, warn, nil
	}
	return buildChunks(path, mtime, messages), nil, nil
}

// readAll slurps the whole file and returns its bytes plus the mtime
// used as the timestamp fallback.
func readAll(path string) ([]byte, time.Time, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, time.Time{}, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, time.Time{}, err
	}
	return data, info.ModTime(), nil
}

// detectAndParse picks the right per-format parser based on the file
// extension and a light content peek. Returns a non-nil warning for
// unknown or malformed files; the caller never sees messages and a
// warning together.
func detectAndParse(path string, data []byte) ([]rawMessage, *ChunkWarning) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".jsonl":
		return parseGemini(path, data)
	case ".json":
		// Try a whole-file JSON parse first. Fall back to JSONL only
		// when the whole-file parse fails AND the content looks like
		// one JSON object per line (some Gemini sessions ship with a
		// .json extension by mistake).
		messages, warn := parseJSONDocument(path, data)
		if warn == nil {
			return messages, nil
		}
		if looksLikeJSONL(data) {
			return parseGemini(path, data)
		}
		return nil, warn
	default:
		if looksLikeJSONL(data) {
			return parseGemini(path, data)
		}
		return nil, &ChunkWarning{SessionPath: path, Reason: "unknown format"}
	}
}

// looksLikeJSONL is true when the content has at least two non-blank
// lines that each start with a JSON object's opening brace. We do not
// attempt a full parse here; the Gemini parser will emit a malformed
// warning if any single line fails to decode.
func looksLikeJSONL(data []byte) bool {
	lines := strings.Split(string(data), "\n")
	objectLines := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "{") {
			return false
		}
		objectLines++
		if objectLines >= 2 {
			return true
		}
	}
	return false
}

// geminiLine captures the shape of a single Gemini CLI session line.
// Gemini writes either `{"role": ..., "parts": [{"text": ...}, ...]}`
// or the flatter `{"role": ..., "text": ...}` depending on version.
type geminiLine struct {
	Role       string       `json:"role"`
	Parts      []geminiPart `json:"parts"`
	Text       string       `json:"text"`
	Timestamp  any          `json:"timestamp"`
	CreateTime any          `json:"create_time"`
}

type geminiPart struct {
	Text string `json:"text"`
}

// parseGemini decodes a JSONL file one line at a time. A malformed line
// aborts the whole file with a warning that names the offending line
// number so the user can locate the problem quickly.
func parseGemini(path string, data []byte) ([]rawMessage, *ChunkWarning) {
	lines := strings.Split(string(data), "\n")
	var messages []rawMessage
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var gl geminiLine
		if err := json.Unmarshal([]byte(trimmed), &gl); err != nil {
			return nil, &ChunkWarning{
				SessionPath: path,
				Reason:      fmt.Sprintf("malformed: line %d: %s", i+1, err.Error()),
			}
		}
		content := gl.Text
		if len(gl.Parts) > 0 {
			var b strings.Builder
			for j, p := range gl.Parts {
				if j > 0 {
					b.WriteString("\n")
				}
				b.WriteString(p.Text)
			}
			content = b.String()
		}
		ts := gl.Timestamp
		if ts == nil {
			ts = gl.CreateTime
		}
		messages = append(messages, rawMessage{
			Role:      gl.Role,
			Content:   content,
			TimeValue: ts,
		})
	}
	return messages, nil
}

// parseJSONDocument parses a whole-file JSON document and dispatches to
// the Kiro or Claude schema based on its shape. A failed parse is
// reported as a malformed warning; the caller may still try JSONL
// detection when the file extension is ambiguous.
func parseJSONDocument(path string, data []byte) ([]rawMessage, *ChunkWarning) {
	var top any
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, &ChunkWarning{
			SessionPath: path,
			Reason:      "malformed: " + err.Error(),
		}
	}
	switch v := top.(type) {
	case []any:
		// Top-level array of Kiro-style messages.
		return parseKiroArray(path, data)
	case map[string]any:
		if _, hasConv := v["conversation"]; hasConv {
			return parseClaude(path, data)
		}
		if msgs, hasMsgs := v["messages"]; hasMsgs {
			if arr, ok := msgs.([]any); ok && isClaudeStyle(arr) {
				return parseClaude(path, data)
			}
			return parseKiro(path, data)
		}
		return nil, &ChunkWarning{SessionPath: path, Reason: "unknown format"}
	default:
		return nil, &ChunkWarning{SessionPath: path, Reason: "unknown format"}
	}
}

// isClaudeStyle inspects the first element of a messages array. Kiro
// uses role/content; Claude Code uses type/text. If the element has
// "type" but no "role" field we treat the container as a Claude file.
func isClaudeStyle(arr []any) bool {
	if len(arr) == 0 {
		return false
	}
	m, ok := arr[0].(map[string]any)
	if !ok {
		return false
	}
	_, hasRole := m["role"]
	_, hasType := m["type"]
	return hasType && !hasRole
}

// kiroMessage captures the Kiro session message schema. Unknown fields
// are ignored by encoding/json; a message that lacks both timestamp
// fields falls back to the file mtime during the chunking pass.
type kiroMessage struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Timestamp any    `json:"timestamp"`
	CreatedAt any    `json:"created_at"`
}

type kiroFile struct {
	Messages []kiroMessage `json:"messages"`
}

func parseKiro(path string, data []byte) ([]rawMessage, *ChunkWarning) {
	var file kiroFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, &ChunkWarning{SessionPath: path, Reason: "malformed: " + err.Error()}
	}
	return kiroMessagesToRaw(file.Messages), nil
}

func parseKiroArray(path string, data []byte) ([]rawMessage, *ChunkWarning) {
	var arr []kiroMessage
	if err := json.Unmarshal(data, &arr); err != nil {
		return nil, &ChunkWarning{SessionPath: path, Reason: "malformed: " + err.Error()}
	}
	return kiroMessagesToRaw(arr), nil
}

func kiroMessagesToRaw(msgs []kiroMessage) []rawMessage {
	out := make([]rawMessage, 0, len(msgs))
	for _, m := range msgs {
		ts := m.Timestamp
		if ts == nil {
			ts = m.CreatedAt
		}
		out = append(out, rawMessage{
			Role:      m.Role,
			Content:   m.Content,
			TimeValue: ts,
		})
	}
	return out
}

// claudeMessage is tolerant: Claude Code ships with type/text but some
// exporters use role/content. We accept either pair.
type claudeMessage struct {
	Type      string `json:"type"`
	Role      string `json:"role"`
	Text      string `json:"text"`
	Content   string `json:"content"`
	Timestamp any    `json:"timestamp"`
	CreatedAt any    `json:"created_at"`
}

type claudeFile struct {
	Conversation []claudeMessage `json:"conversation"`
	Messages     []claudeMessage `json:"messages"`
}

func parseClaude(path string, data []byte) ([]rawMessage, *ChunkWarning) {
	var file claudeFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, &ChunkWarning{SessionPath: path, Reason: "malformed: " + err.Error()}
	}
	src := file.Conversation
	if len(src) == 0 {
		src = file.Messages
	}
	out := make([]rawMessage, 0, len(src))
	for _, m := range src {
		role := m.Type
		if role == "" {
			role = m.Role
		}
		content := m.Text
		if content == "" {
			content = m.Content
		}
		ts := m.Timestamp
		if ts == nil {
			ts = m.CreatedAt
		}
		out = append(out, rawMessage{
			Role:      role,
			Content:   content,
			TimeValue: ts,
		})
	}
	return out, nil
}

// buildChunks is the shared chunking pass. It assigns a dense, zero-
// based ChunkIndex across the whole file and normalizes each message's
// role and timestamp. Every message produces at least one chunk so the
// indexer can still record a session whose content is empty.
func buildChunks(path string, mtime time.Time, messages []rawMessage) []Chunk {
	var out []Chunk
	index := 0
	for _, m := range messages {
		role := normalizeRole(m.Role)
		ts := resolveTimestamp(m.TimeValue, mtime)
		for _, piece := range splitContent(m.Content) {
			out = append(out, Chunk{
				SessionPath: path,
				ChunkIndex:  index,
				Role:        role,
				Content:     piece,
				Timestamp:   ts,
			})
			index++
		}
	}
	return out
}

// normalizeRole maps provider-specific role names onto the four values
// the rest of the pipeline consumes. Anything unrecognized collapses to
// the empty string so downstream filters on role behave predictably.
func normalizeRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user", "human":
		return "user"
	case "assistant", "model", "ai":
		return "assistant"
	case "system":
		return "system"
	default:
		return ""
	}
}

// resolveTimestamp walks the preference order described in Story 8:
// message-level RFC 3339 string, message-level Unix epoch number, and
// finally the file mtime. Fractional seconds in a float timestamp are
// preserved.
func resolveTimestamp(val any, fallback time.Time) time.Time {
	switch v := val.(type) {
	case string:
		if v == "" {
			return fallback
		}
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return t
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t
		}
	case float64:
		sec := int64(v)
		frac := int64((v - float64(sec)) * 1e9)
		return time.Unix(sec, frac).UTC()
	case int64:
		return time.Unix(v, 0).UTC()
	case int:
		return time.Unix(int64(v), 0).UTC()
	}
	return fallback
}

// splitContent splits a message body into chunks that each hold at most
// tokenBudget whitespace tokens. An empty or whitespace-only body still
// produces one chunk so the caller can assign a dense ChunkIndex
// without holes. A single pathologically long word is split by rune at
// the singleWordRuneCap boundary to guarantee termination.
func splitContent(content string) []string {
	if strings.TrimSpace(content) == "" {
		return []string{""}
	}
	words := strings.Fields(content)
	if len(words) == 0 {
		return []string{""}
	}

	var chunks []string
	var builder strings.Builder
	wordsInChunk := 0

	flush := func() {
		if builder.Len() > 0 {
			chunks = append(chunks, builder.String())
			builder.Reset()
			wordsInChunk = 0
		}
	}
	appendWord := func(word string) {
		if builder.Len() > 0 {
			builder.WriteByte(' ')
		}
		builder.WriteString(word)
		wordsInChunk++
	}

	for _, word := range words {
		if runeLen(word) > singleWordRuneCap {
			// Pathological single-token overflow. Flush whatever we
			// have, then split this word by rune at the budget
			// boundary and emit each piece as its own chunk.
			flush()
			chunks = append(chunks, splitByRunes(word, singleWordRuneCap)...)
			continue
		}
		if wordsInChunk >= tokenBudget {
			flush()
		}
		appendWord(word)
	}
	flush()
	if len(chunks) == 0 {
		return []string{""}
	}
	return chunks
}

// runeLen counts runes without allocating a slice. strings.Fields has
// already given us individual tokens; we only need a length.
func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// splitByRunes slices s into pieces of at most capRunes runes each.
// The last piece may be shorter. A zero or negative cap returns the
// input untouched, guarding against accidental infinite loops.
func splitByRunes(s string, capRunes int) []string {
	if capRunes <= 0 {
		return []string{s}
	}
	var out []string
	var builder strings.Builder
	count := 0
	for _, r := range s {
		builder.WriteRune(r)
		count++
		if count >= capRunes {
			out = append(out, builder.String())
			builder.Reset()
			count = 0
		}
	}
	if builder.Len() > 0 {
		out = append(out, builder.String())
	}
	return out
}
