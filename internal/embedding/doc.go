// Package embedding turns a raw string into a 384-dimensional, L2-normalized
// []float32 suitable for use with sqlite-vec. It wires two external pieces
// together:
//
//   - a WordPiece tokenizer, loaded from a Hugging Face-style
//     tokenizer.json via github.com/sugarme/tokenizer, and
//   - the Microsoft ONNX Runtime, loaded as a shared library and driven
//     through github.com/yalue/onnxruntime_go, running the
//     sentence-transformers/all-MiniLM-L6-v2 ONNX model.
//
// The package deliberately exposes only two small interfaces, Tokenizer
// and Embedder, so the rest of the codebase can depend on behaviour
// rather than on a specific runtime. None of the upstream library types
// leak across the package boundary, and neither InitializeEnvironment
// nor SetSharedLibraryPath is called at package init() time: the first
// successful NewEmbedder call does both, guarded by sync.Once, so that
// callers who never use the embedder pay zero runtime cost.
//
// Concurrency
//
// A Tokenizer returned by NewTokenizer is safe for concurrent use. A
// sugarme Encoding is computed synchronously from the input text with
// no shared mutable state beyond the tokenizer configuration, and the
// wrapper does not mutate that configuration after construction.
//
// The ONNX Runtime session wrapped by Embedder is serialized with an
// internal sync.Mutex. onnxruntime_go's AdvancedSession and
// DynamicAdvancedSession are not documented as safe for concurrent
// Run() calls against the same session, so Embed and EmbedBatch grab
// the mutex before invoking the runtime. Callers may therefore call
// Embed / EmbedBatch from any number of goroutines; calls are queued
// rather than executed in parallel.
//
// Lifetime
//
// Callers own both the Tokenizer and the Embedder. Close() on either
// type releases the underlying native resources. After Close(),
// subsequent Embed/EmbedBatch calls return ErrEmbedderClosed, and
// Tokenize calls return ErrTokenizerClosed. Double-close is a no-op.
//
// Numerical contract
//
// Every vector returned by Embed or EmbedBatch has exactly Dim()
// elements (384 for all-MiniLM-L6-v2) and satisfies
// abs(sum(v[i]*v[i]) - 1.0) <= 1e-4 for nonzero inputs. Empty input
// yields a valid, all-zero-ish vector (the tokenizer still emits
// [CLS] and [SEP]) without panicking.
package embedding
