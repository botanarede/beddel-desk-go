package embedding

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

// EmbeddingDim is the output dimensionality of
// sentence-transformers/all-MiniLM-L6-v2. It is exported because
// storage code needs to pass it to sqlite-vec when declaring the
// vector column.
const EmbeddingDim = 384

// ErrEmbedderClosed is returned from Embed and EmbedBatch when the
// embedder has already been closed.
var ErrEmbedderClosed = errors.New("embedding: embedder is closed")

// Embedder produces L2-normalized float32 vectors from raw text using
// a local ONNX model. Implementations are safe for concurrent use.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
	Dim() int
	Close() error
}

// onnxRuntimeState tracks the process-wide ONNX Runtime
// initialization state. onnxruntime_go.InitializeEnvironment must be
// called exactly once per process, so we capture the shared library
// path of the first successful call and reject later calls pointing at
// a different path. The sync.Once guarantees single initialization;
// the sync.Mutex serializes reads of the path so the compare-and-error
// branch below is race-free.
type onnxRuntimeState struct {
	once    sync.Once
	mu      sync.Mutex
	err     error
	libPath string
}

var onnxRuntime = &onnxRuntimeState{}

// initOnnxRuntime ensures the ONNX runtime is initialized against
// libPath. The first caller wins: later callers with the same path
// succeed, later callers with a different path get a clear error.
// This matches the contract in Story 7 constraint #3.
func initOnnxRuntime(libPath string) error {
	onnxRuntime.mu.Lock()
	defer onnxRuntime.mu.Unlock()

	onnxRuntime.once.Do(func() {
		ort.SetSharedLibraryPath(libPath)
		if err := ort.InitializeEnvironment(); err != nil {
			onnxRuntime.err = fmt.Errorf(
				"embedding: initialize onnx runtime at %q: %w",
				libPath, err)
			return
		}
		onnxRuntime.libPath = libPath
	})

	if onnxRuntime.err != nil {
		return onnxRuntime.err
	}
	if onnxRuntime.libPath != libPath {
		return fmt.Errorf(
			"embedding: onnx runtime already initialized with a different shared library (have %q, requested %q)",
			onnxRuntime.libPath, libPath)
	}
	return nil
}

// onnxEmbedder is the production Embedder. It wraps a
// DynamicAdvancedSession because the session inputs vary in sequence
// length call by call, and DynamicAdvancedSession accepts fresh input
// tensors on every Run.
type onnxEmbedder struct {
	mu sync.Mutex

	session *ort.DynamicAdvancedSession
	tok     Tokenizer

	closed bool

	// inputNames / outputName match the names exported by the
	// all-MiniLM-L6-v2 ONNX model. They are stored for diagnostics and
	// are fixed at session creation time.
	inputNames []string
	outputName string
}

// NewEmbedder constructs an Embedder that runs the given ONNX model
// through the ONNX runtime shared library at runtimeLibPath and
// delegates tokenization to tok.
//
// The first successful call also initializes the ONNX runtime for the
// whole process; see initOnnxRuntime for the exact semantics.
func NewEmbedder(runtimeLibPath, modelPath string, tok Tokenizer) (Embedder, error) {
	if runtimeLibPath == "" {
		return nil, errors.New("embedding: runtime library path must be provided")
	}
	if modelPath == "" {
		return nil, errors.New("embedding: model path must be provided")
	}
	if tok == nil {
		return nil, errors.New("embedding: tokenizer must be provided")
	}

	if err := initOnnxRuntime(runtimeLibPath); err != nil {
		return nil, err
	}

	inputNames := []string{"input_ids", "attention_mask", "token_type_ids"}
	outputName := "last_hidden_state"

	session, err := ort.NewDynamicAdvancedSession(modelPath, inputNames, []string{outputName}, nil)
	if err != nil {
		return nil, fmt.Errorf(
			"embedding: load onnx model %q with runtime %q: %w",
			modelPath, runtimeLibPath, err)
	}

	return &onnxEmbedder{
		session:    session,
		tok:        tok,
		inputNames: inputNames,
		outputName: outputName,
	}, nil
}

// Dim returns the embedding dimensionality (384 for all-MiniLM-L6-v2).
func (e *onnxEmbedder) Dim() int { return EmbeddingDim }

// Close releases the ONNX session. It does NOT close the tokenizer,
// because the tokenizer has a separate lifecycle owned by the caller.
func (e *onnxEmbedder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	if e.session != nil {
		if err := e.session.Destroy(); err != nil {
			return fmt.Errorf("embedding: destroy session: %w", err)
		}
		e.session = nil
	}
	return nil
}

// Embed encodes text and returns a single L2-normalized 384-dim vector.
func (e *onnxEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	vectors, err := e.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 {
		return nil, fmt.Errorf(
			"embedding: expected 1 vector from EmbedBatch, got %d",
			len(vectors))
	}
	return vectors[0], nil
}

// EmbedBatch tokenizes every text, runs the ONNX model once with a
// padded batch, and returns one L2-normalized 384-dim vector per
// input. The returned slice is in the same order as texts.
func (e *onnxEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.session == nil {
		return nil, ErrEmbedderClosed
	}

	if len(texts) == 0 {
		return [][]float32{}, nil
	}

	// Tokenize every input up front so we know the batch shape before
	// allocating tensors.
	batchIDs := make([][]int64, len(texts))
	batchMasks := make([][]int64, len(texts))
	maxLen := 1 // Avoid a zero dimension, which the runtime would reject.
	for i, text := range texts {
		ids, mask, err := e.tok.Tokenize(text)
		if err != nil {
			return nil, fmt.Errorf(
				"embedding: tokenize batch item %d: %w", i, err)
		}
		batchIDs[i] = ids
		batchMasks[i] = mask
		if len(ids) > maxLen {
			maxLen = len(ids)
		}
	}

	// Check context again after tokenization, which can be the
	// dominant cost for long inputs.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	batchSize := int64(len(texts))
	seqLen := int64(maxLen)
	flatIDs, flatMask, flatTypeIDs := packBatch(batchIDs, batchMasks, int(seqLen))

	shape := ort.NewShape(batchSize, seqLen)

	inputIDs, err := ort.NewTensor(shape, flatIDs)
	if err != nil {
		return nil, fmt.Errorf("embedding: allocate input_ids tensor: %w", err)
	}
	defer inputIDs.Destroy()

	attentionMask, err := ort.NewTensor(shape, flatMask)
	if err != nil {
		return nil, fmt.Errorf("embedding: allocate attention_mask tensor: %w", err)
	}
	defer attentionMask.Destroy()

	typeIDs, err := ort.NewTensor(shape, flatTypeIDs)
	if err != nil {
		return nil, fmt.Errorf("embedding: allocate token_type_ids tensor: %w", err)
	}
	defer typeIDs.Destroy()

	outputs := []ort.Value{nil}
	inputs := []ort.Value{inputIDs, attentionMask, typeIDs}

	if err := e.session.Run(inputs, outputs); err != nil {
		return nil, fmt.Errorf("embedding: run onnx session: %w", err)
	}
	if outputs[0] == nil {
		return nil, errors.New("embedding: onnx session returned nil output")
	}
	defer func() {
		// Destroy the automatically-allocated output tensor; the
		// DynamicAdvancedSession contract hands ownership to us.
		if d, ok := outputs[0].(interface{ Destroy() error }); ok {
			_ = d.Destroy()
		}
	}()

	outTensor, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		return nil, fmt.Errorf(
			"embedding: unexpected output tensor type %T, want *Tensor[float32]",
			outputs[0])
	}

	outShape := outTensor.GetShape()
	if len(outShape) != 3 {
		return nil, fmt.Errorf(
			"embedding: expected [batch, seq, hidden] output, got shape %v",
			outShape)
	}
	if outShape[0] != batchSize || outShape[1] != seqLen || outShape[2] != EmbeddingDim {
		return nil, fmt.Errorf(
			"embedding: unexpected output shape %v, want [%d %d %d]",
			outShape, batchSize, seqLen, EmbeddingDim)
	}

	rawOutput := outTensor.GetData()
	return poolAndNormalizeBatch(rawOutput, flatMask, int(batchSize), int(seqLen), EmbeddingDim), nil
}

// packBatch concatenates per-row ids and masks into three
// (batch*seqLen)-sized flat slices, padding every row with zeros on
// the right to seqLen. The token_type_ids output is always zero
// because all-MiniLM-L6-v2 is a single-sequence model.
func packBatch(ids, masks [][]int64, seqLen int) (flatIDs, flatMask, flatType []int64) {
	batchSize := len(ids)
	total := batchSize * seqLen
	flatIDs = make([]int64, total)
	flatMask = make([]int64, total)
	flatType = make([]int64, total)
	for i := 0; i < batchSize; i++ {
		rowIDs := ids[i]
		rowMask := masks[i]
		base := i * seqLen
		n := len(rowIDs)
		if n > seqLen {
			n = seqLen
		}
		for j := 0; j < n; j++ {
			flatIDs[base+j] = rowIDs[j]
			if j < len(rowMask) {
				flatMask[base+j] = rowMask[j]
			}
		}
		// token_type_ids stays zero; remainder of the row (padding)
		// also stays zero, which matches the expected attention mask
		// of 0 for padding tokens.
	}
	return flatIDs, flatMask, flatType
}

// poolAndNormalizeBatch applies mean pooling over the sequence
// dimension, weighted by the attention mask, and L2-normalizes the
// resulting vector for every row in the batch. It is a pure function
// so it can be exercised without loading ONNX.
func poolAndNormalizeBatch(hidden []float32, mask []int64, batchSize, seqLen, hiddenDim int) [][]float32 {
	out := make([][]float32, batchSize)
	for i := 0; i < batchSize; i++ {
		rowHidden := hidden[i*seqLen*hiddenDim : (i+1)*seqLen*hiddenDim]
		rowMask := mask[i*seqLen : (i+1)*seqLen]
		out[i] = meanPool(rowHidden, rowMask, seqLen, hiddenDim)
		l2Normalize(out[i])
	}
	return out
}

// meanPool averages the hidden states over the sequence dimension,
// weighting each position by its attention mask. Positions with a
// zero mask are ignored. If the mask is all zeros (which can happen
// for empty input where the tokenizer still emits [CLS] and [SEP]
// but the caller passed a zero mask), the function falls back to a
// uniform average so the caller still gets a well-formed vector.
func meanPool(hidden []float32, mask []int64, seqLen, hiddenDim int) []float32 {
	out := make([]float32, hiddenDim)
	var count float64
	for s := 0; s < seqLen; s++ {
		if mask[s] == 0 {
			continue
		}
		count++
		base := s * hiddenDim
		for d := 0; d < hiddenDim; d++ {
			out[d] += hidden[base+d]
		}
	}
	if count == 0 {
		// All-zero mask: average over every position to guarantee a
		// valid (though semantically meaningless) vector rather than
		// returning NaNs.
		for s := 0; s < seqLen; s++ {
			base := s * hiddenDim
			for d := 0; d < hiddenDim; d++ {
				out[d] += hidden[base+d]
			}
		}
		count = float64(seqLen)
	}
	if count == 0 {
		return out
	}
	inv := float32(1.0 / count)
	for d := 0; d < hiddenDim; d++ {
		out[d] *= inv
	}
	return out
}

// l2Normalize divides v by its L2 norm in place. A zero vector is
// left untouched so that we never produce NaN or Inf values.
func l2Normalize(v []float32) {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return
	}
	inv := float32(1.0 / math.Sqrt(sum))
	for i := range v {
		v[i] *= inv
	}
}
