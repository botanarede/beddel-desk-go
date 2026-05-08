package embedding

import (
	"context"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
)

const floatEpsilon = 1e-5

// TestL2NormalizeProducesUnitVector verifies the pure-function contract
// of the normalizer: sum of squares is 1.0 within a small epsilon.
func TestL2NormalizeProducesUnitVector(t *testing.T) {
	v := []float32{3, 4, 0, 0}
	l2Normalize(v)

	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if math.Abs(sum-1.0) > 1e-6 {
		t.Fatalf("expected unit-norm vector, got sum=%v", sum)
	}
	// Direction is preserved: v[0]/v[1] == 3/4.
	ratio := float64(v[0]) / float64(v[1])
	if math.Abs(ratio-0.75) > 1e-6 {
		t.Fatalf("direction changed: ratio=%v", ratio)
	}
}

// TestL2NormalizeLeavesZeroVectorUntouched guards against NaN/Inf when
// the mean-pool step produces an all-zero vector.
func TestL2NormalizeLeavesZeroVectorUntouched(t *testing.T) {
	v := []float32{0, 0, 0, 0}
	l2Normalize(v)
	for _, x := range v {
		if x != 0 {
			t.Fatalf("zero vector mutated: %v", v)
		}
	}
}

// TestMeanPoolUsesAttentionMask asserts that masked-out positions do
// not contribute to the mean.
func TestMeanPoolUsesAttentionMask(t *testing.T) {
	// Two tokens, hidden dim 3. Token 0 is real, token 1 is padding.
	hidden := []float32{
		1, 2, 3, // token 0
		9, 9, 9, // token 1 (should be ignored)
	}
	mask := []int64{1, 0}
	got := meanPool(hidden, mask, 2, 3)
	want := []float32{1, 2, 3}
	for i := range want {
		if math.Abs(float64(got[i]-want[i])) > floatEpsilon {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// TestMeanPoolAveragesAcrossRealTokens checks the division by the
// count of attended positions.
func TestMeanPoolAveragesAcrossRealTokens(t *testing.T) {
	hidden := []float32{
		1, 0,
		3, 2,
		5, 4,
	}
	mask := []int64{1, 1, 1}
	got := meanPool(hidden, mask, 3, 2)
	want := []float32{3, 2}
	for i := range want {
		if math.Abs(float64(got[i]-want[i])) > floatEpsilon {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// TestMeanPoolHandlesAllZeroMask falls back to a uniform average.
// This mirrors the empty-input case where the tokenizer returns only
// [CLS]/[SEP] but the test-side double passes a zero mask.
func TestMeanPoolHandlesAllZeroMask(t *testing.T) {
	hidden := []float32{
		2, 4,
		6, 8,
	}
	mask := []int64{0, 0}
	got := meanPool(hidden, mask, 2, 2)
	want := []float32{4, 6}
	for i := range want {
		if math.Abs(float64(got[i]-want[i])) > floatEpsilon {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// TestPackBatchPadsShorterRowsWithZeros verifies the layout that the
// ONNX runtime expects: [batch, seq] flat, right-padded.
func TestPackBatchPadsShorterRowsWithZeros(t *testing.T) {
	ids := [][]int64{{11, 12, 13}, {21, 22}}
	masks := [][]int64{{1, 1, 1}, {1, 1}}
	flatIDs, flatMask, flatType := packBatch(ids, masks, 3)

	wantIDs := []int64{11, 12, 13, 21, 22, 0}
	wantMask := []int64{1, 1, 1, 1, 1, 0}
	for i, v := range wantIDs {
		if flatIDs[i] != v {
			t.Fatalf("flatIDs[%d]=%d, want %d", i, flatIDs[i], v)
		}
	}
	for i, v := range wantMask {
		if flatMask[i] != v {
			t.Fatalf("flatMask[%d]=%d, want %d", i, flatMask[i], v)
		}
	}
	for i, v := range flatType {
		if v != 0 {
			t.Fatalf("flatType[%d]=%d, want 0 (model is single-sequence)", i, v)
		}
	}
}

// TestPoolAndNormalizeBatchProducesUnitVectors exercises the full
// batch pipeline end-to-end: mean pooling + L2 normalization per row.
func TestPoolAndNormalizeBatchProducesUnitVectors(t *testing.T) {
	batchSize := 2
	seqLen := 2
	hiddenDim := 4
	// Two different "hidden state" patterns so the two rows point in
	// different directions after normalization.
	hidden := []float32{
		// row 0, token 0
		1, 0, 0, 0,
		// row 0, token 1
		1, 0, 0, 0,
		// row 1, token 0
		0, 3, 0, 4,
		// row 1, token 1 (masked out)
		99, 99, 99, 99,
	}
	mask := []int64{
		1, 1,
		1, 0,
	}
	vectors := poolAndNormalizeBatch(hidden, mask, batchSize, seqLen, hiddenDim)
	if len(vectors) != batchSize {
		t.Fatalf("got %d rows, want %d", len(vectors), batchSize)
	}
	for i, v := range vectors {
		if len(v) != hiddenDim {
			t.Fatalf("row %d has dim %d, want %d", i, len(v), hiddenDim)
		}
		var sum float64
		for _, x := range v {
			sum += float64(x) * float64(x)
		}
		if math.Abs(sum-1.0) > 1e-4 {
			t.Fatalf("row %d is not unit norm: sum=%v", i, sum)
		}
	}

	// Row 0 is [1,0,0,0] after normalization.
	if math.Abs(float64(vectors[0][0])-1.0) > floatEpsilon {
		t.Fatalf("row 0 expected direction [1,0,0,0], got %v", vectors[0])
	}
	// Row 1 is proportional to [0,3,0,4], so after normalization
	// [0, 3/5, 0, 4/5].
	if math.Abs(float64(vectors[1][1])-0.6) > floatEpsilon ||
		math.Abs(float64(vectors[1][3])-0.8) > floatEpsilon {
		t.Fatalf("row 1 direction wrong: %v", vectors[1])
	}
}

// testEmbedder is a small fake Embedder backed by the real pooling
// helpers. It does not call into ONNX, but it respects the batch-order
// contract and the closed-state contract, so ordering-stability tests
// exercise the same math path that the real embedder does.
type testEmbedder struct {
	tok    *fakeTokenizer
	closed bool

	// hiddenFor maps input text -> the flat [seq, hiddenDim] hidden
	// states used in the fake "forward" pass. If a text is absent a
	// default pattern is synthesized from the tokenized id sequence.
	hiddenFor map[string][]float32
}

func (e *testEmbedder) Dim() int { return 4 }

func (e *testEmbedder) Close() error {
	e.closed = true
	return nil
}

func (e *testEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	vectors, err := e.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	return vectors[0], nil
}

func (e *testEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if e.closed {
		return nil, ErrEmbedderClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(texts) == 0 {
		return [][]float32{}, nil
	}

	hiddenDim := e.Dim()
	batchIDs := make([][]int64, len(texts))
	batchMasks := make([][]int64, len(texts))
	maxLen := 1
	for i, txt := range texts {
		ids, mask, err := e.tok.Tokenize(txt)
		if err != nil {
			return nil, err
		}
		batchIDs[i] = ids
		batchMasks[i] = mask
		if len(ids) > maxLen {
			maxLen = len(ids)
		}
	}
	_, flatMask, _ := packBatch(batchIDs, batchMasks, maxLen)

	hidden := make([]float32, len(texts)*maxLen*hiddenDim)
	for i, txt := range texts {
		base := i * maxLen * hiddenDim
		if pattern, ok := e.hiddenFor[txt]; ok {
			copy(hidden[base:base+len(pattern)], pattern)
			continue
		}
		// Default pattern: each token contributes a simple ramp based
		// on its id. This is fully deterministic for a given text.
		for s, id := range batchIDs[i] {
			for d := 0; d < hiddenDim; d++ {
				hidden[base+s*hiddenDim+d] = float32(int(id) + d)
			}
		}
	}
	return poolAndNormalizeBatch(hidden, flatMask, len(texts), maxLen, hiddenDim), nil
}

// TestTestEmbedderBatchMatchesSingle is the ordering-stability test
// required by Story 7: Embed(texts[i]) == EmbedBatch(texts)[i].
func TestTestEmbedderBatchMatchesSingle(t *testing.T) {
	tok := &fakeTokenizer{
		idsFor: map[string][]int64{
			"hello":   {101, 7592, 102},
			"world":   {101, 2088, 102},
			"goodbye": {101, 9119, 102},
		},
	}
	emb := &testEmbedder{tok: tok}

	texts := []string{"hello", "world", "goodbye"}
	ctx := context.Background()

	batch, err := emb.EmbedBatch(ctx, texts)
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(batch) != len(texts) {
		t.Fatalf("got %d vectors, want %d", len(batch), len(texts))
	}
	for i, text := range texts {
		single, err := emb.Embed(ctx, text)
		if err != nil {
			t.Fatalf("Embed[%d]: %v", i, err)
		}
		if len(single) != emb.Dim() {
			t.Fatalf("Embed[%d] dim = %d, want %d", i, len(single), emb.Dim())
		}
		for j := range single {
			if math.Abs(float64(single[j]-batch[i][j])) > floatEpsilon {
				t.Fatalf("mismatch at text %q dim %d: single=%v batch=%v",
					text, j, single[j], batch[i][j])
			}
		}
	}
}

// TestTestEmbedderClosedReturnsSentinel verifies the interface-level
// Close contract.
func TestTestEmbedderClosedReturnsSentinel(t *testing.T) {
	emb := &testEmbedder{tok: &fakeTokenizer{}}
	if err := emb.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err := emb.Embed(context.Background(), "hello")
	if !errors.Is(err, ErrEmbedderClosed) {
		t.Fatalf("expected ErrEmbedderClosed, got %v", err)
	}
	_, err = emb.EmbedBatch(context.Background(), []string{"hello"})
	if !errors.Is(err, ErrEmbedderClosed) {
		t.Fatalf("expected ErrEmbedderClosed from EmbedBatch, got %v", err)
	}
}

// TestOnnxEmbedderClosedReturnsSentinel constructs an onnxEmbedder in
// the closed state to verify the production type's Close contract
// without needing the ONNX runtime on disk.
func TestOnnxEmbedderClosedReturnsSentinel(t *testing.T) {
	e := &onnxEmbedder{closed: true}
	_, err := e.Embed(context.Background(), "hello")
	if !errors.Is(err, ErrEmbedderClosed) {
		t.Fatalf("Embed: expected ErrEmbedderClosed, got %v", err)
	}
	_, err = e.EmbedBatch(context.Background(), []string{"hello"})
	if !errors.Is(err, ErrEmbedderClosed) {
		t.Fatalf("EmbedBatch: expected ErrEmbedderClosed, got %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close after close should be a no-op, got %v", err)
	}
}

// TestOnnxEmbedderRespectsContextCancellation confirms that a
// pre-canceled context short-circuits without dereferencing the (nil)
// session.
func TestOnnxEmbedderRespectsContextCancellation(t *testing.T) {
	e := &onnxEmbedder{} // closed=false but session is nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := e.Embed(ctx, "hello")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// TestDimConstant makes the 384-dim assumption explicit in the test
// suite so a future refactor of the constant trips this check.
func TestDimConstant(t *testing.T) {
	if EmbeddingDim != 384 {
		t.Fatalf("EmbeddingDim=%d, want 384 for all-MiniLM-L6-v2", EmbeddingDim)
	}
}

// TestNewEmbedderValidatesArguments asserts that obvious misuses are
// reported with clear errors before any native library is touched.
func TestNewEmbedderValidatesArguments(t *testing.T) {
	tok := &fakeTokenizer{}

	cases := []struct {
		name    string
		libPath string
		model   string
		tok     Tokenizer
		want    string
	}{
		{"empty runtime", "", "/model.onnx", tok, "runtime library path"},
		{"empty model", "/lib.so", "", tok, "model path"},
		{"nil tokenizer", "/lib.so", "/model.onnx", nil, "tokenizer must be provided"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewEmbedder(tc.libPath, tc.model, tc.tok)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// TestEmbedderE2E runs the full pipeline against a real ONNX runtime
// and model. It is gated on BEDDEL_EMBED_E2E=1 and on two additional
// environment variables so that local developers can point at the
// assets downloaded by the Manager in internal/embedding/download.
func TestEmbedderE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping end-to-end embedder test in short mode")
	}
	if os.Getenv("BEDDEL_EMBED_E2E") != "1" {
		t.Skip("set BEDDEL_EMBED_E2E=1 to run end-to-end embedder tests against ONNX runtime")
	}
	runtimeLib := os.Getenv("BEDDEL_ONNX_RUNTIME_LIB")
	modelPath := os.Getenv("BEDDEL_ONNX_MODEL")
	tokenizerJSON := os.Getenv("BEDDEL_TOKENIZER_JSON")
	if runtimeLib == "" || modelPath == "" || tokenizerJSON == "" {
		t.Skip("set BEDDEL_ONNX_RUNTIME_LIB, BEDDEL_ONNX_MODEL, BEDDEL_TOKENIZER_JSON to run the E2E test")
	}

	tok, err := NewTokenizer(tokenizerJSON)
	if err != nil {
		t.Fatalf("NewTokenizer: %v", err)
	}
	t.Cleanup(func() { _ = tok.Close() })

	emb, err := NewEmbedder(runtimeLib, modelPath, tok)
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}
	t.Cleanup(func() { _ = emb.Close() })

	if emb.Dim() != EmbeddingDim {
		t.Fatalf("Dim()=%d, want %d", emb.Dim(), EmbeddingDim)
	}

	ctx := context.Background()
	texts := []string{"hello", "the quick brown fox", "", "unicode \U0001F600"}
	batch, err := emb.EmbedBatch(ctx, texts)
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(batch) != len(texts) {
		t.Fatalf("got %d vectors, want %d", len(batch), len(texts))
	}
	for i, v := range batch {
		if len(v) != emb.Dim() {
			t.Fatalf("vector %d has dim %d, want %d", i, len(v), emb.Dim())
		}
		var sum float64
		for _, x := range v {
			sum += float64(x) * float64(x)
		}
		if math.Abs(sum-1.0) > 1e-4 {
			t.Fatalf("vector %d is not unit norm: sum=%v", i, sum)
		}
	}

	// Order stability.
	single, err := emb.Embed(ctx, texts[1])
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	for j := range single {
		if math.Abs(float64(single[j]-batch[1][j])) > 1e-5 {
			t.Fatalf("Embed and EmbedBatch disagree at dim %d: %v vs %v",
				j, single[j], batch[1][j])
		}
	}
}
