package embedding

import (
	"errors"
	"fmt"
	"sync"

	sgt "github.com/sugarme/tokenizer"
	"github.com/sugarme/tokenizer/pretrained"
)

// MaxSequenceLength is the hard ceiling on the number of tokens (special
// tokens included) emitted by the tokenizer. It matches the training
// regime of sentence-transformers/all-MiniLM-L6-v2.
const MaxSequenceLength = 256

// ErrTokenizerClosed is returned from Tokenize when the tokenizer has
// already been closed. Callers should test with errors.Is so wrapping
// at higher layers remains transparent.
var ErrTokenizerClosed = errors.New("embedding: tokenizer is closed")

// Tokenizer turns a raw string into the two int64 slices the ONNX
// model expects: the WordPiece input ids and the attention mask. The
// returned slices always have the same length, and are never nil when
// the returned error is nil.
type Tokenizer interface {
	Tokenize(text string) (inputIDs, attentionMask []int64, err error)
	Close() error
}

// sugarmeTokenizer wraps a sugarme *tokenizer.Tokenizer and enforces
// the MaxSequenceLength policy explicitly, independently of whatever
// truncation/padding settings tokenizer.json carries. This is a
// defense in depth: the upstream JSON for some BERT-family models
// omits truncation, and we do not want the embedder to receive a
// sequence longer than the ONNX model was exported for.
type sugarmeTokenizer struct {
	mu     sync.RWMutex
	inner  *sugarmeInner
	closed bool
}

// sugarmeInner is a tiny indirection layer that keeps the upstream
// types out of the exported API surface and out of the test helpers.
type sugarmeInner struct {
	tk *sgt.Tokenizer
}

// NewTokenizer loads tokenizer.json from the given path and returns a
// Tokenizer that always produces sequences no longer than
// MaxSequenceLength, with [CLS] and [SEP] inserted, right-truncating
// anything in between.
func NewTokenizer(tokenizerJSONPath string) (Tokenizer, error) {
	if tokenizerJSONPath == "" {
		return nil, errors.New("embedding: tokenizer json path must be provided")
	}
	tk, err := pretrained.FromFile(tokenizerJSONPath)
	if err != nil {
		return nil, fmt.Errorf("embedding: load tokenizer %q: %w", tokenizerJSONPath, err)
	}

	// Enforce truncation policy explicitly. sugarme honours tokenizer.json
	// settings when the file contains them, but for WordPiece BERT-family
	// files published without a truncation block the tokenizer would
	// otherwise silently emit sequences longer than the model supports.
	tk.WithTruncation(&sgt.TruncationParams{
		MaxLength: MaxSequenceLength,
		Strategy:  sgt.LongestFirst,
	})

	return &sugarmeTokenizer{inner: &sugarmeInner{tk: tk}}, nil
}

// Tokenize encodes text and returns the input ids and attention mask
// as int64 slices, trimmed to MaxSequenceLength. [CLS] and [SEP] are
// always emitted by sugarme for WordPiece tokenizers loaded via
// pretrained.FromFile because the post-processor is read from the
// tokenizer.json file.
func (t *sugarmeTokenizer) Tokenize(text string) ([]int64, []int64, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.closed || t.inner == nil {
		return nil, nil, ErrTokenizerClosed
	}

	// addSpecialTokens=true causes the BERT post-processor in
	// tokenizer.json to insert [CLS] / [SEP].
	enc, err := t.inner.tk.EncodeSingle(text, true)
	if err != nil {
		return nil, nil, fmt.Errorf("embedding: tokenize: %w", err)
	}
	if enc == nil {
		return nil, nil, errors.New("embedding: tokenizer returned nil encoding")
	}

	ids := enc.Ids
	mask := enc.AttentionMask

	// sugarme does not always populate AttentionMask when padding is
	// disabled; derive it from the id count in that case so downstream
	// callers always see matched-length slices.
	if len(mask) == 0 {
		mask = make([]int, len(ids))
		for i := range mask {
			mask[i] = 1
		}
	}

	// Defensive truncation: if the upstream truncation happens to be a
	// no-op for any reason, we still respect the contract.
	if len(ids) > MaxSequenceLength {
		ids = ids[:MaxSequenceLength]
	}
	if len(mask) > MaxSequenceLength {
		mask = mask[:MaxSequenceLength]
	}

	inputIDs := toInt64Slice(ids)
	attentionMask := toInt64Slice(mask)

	if len(inputIDs) != len(attentionMask) {
		return nil, nil, fmt.Errorf(
			"embedding: tokenizer produced mismatched ids/mask lengths: %d vs %d",
			len(inputIDs), len(attentionMask))
	}
	return inputIDs, attentionMask, nil
}

// Close releases the tokenizer. It is safe to call Close more than
// once; subsequent calls return nil and are no-ops.
func (t *sugarmeTokenizer) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	t.inner = nil
	return nil
}

// toInt64Slice widens a []int returned by sugarme into the []int64 the
// ONNX runtime expects. A nil input returns a nil slice so the caller
// sees a well-formed empty tensor when the tokenizer emits nothing.
func toInt64Slice(in []int) []int64 {
	if in == nil {
		return nil
	}
	out := make([]int64, len(in))
	for i, v := range in {
		out[i] = int64(v)
	}
	return out
}
