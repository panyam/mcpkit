// codec.go — wire-format encoder/decoder for Redis pubsub payloads.
//
// Codec is parameterized over the payload type so each consumer
// fixes its own shape: the events SDK Bus uses Codec[events.Event];
// future per-domain Buses pick their own T. The root stays
// events-free; T is purely a caller-supplied type parameter.
//
// Adopters writing custom codecs (protobuf, msgpack, etc.) implement
// the interface against the specific T they care about.
package redisstore

import "encoding/json"

// Codec is the seam between a typed value of T and the bytes carried
// over Redis pubsub. Implementations MUST be symmetric:
// Decode(Encode(v)) reconstructs a T semantically equivalent to v.
//
// Encode errors abort the publish (the caller returns the error).
// Decode errors are logged and the message dropped — a corrupt wire
// body SHOULD NOT take the subscriber goroutine down.
type Codec[T any] interface {
	Encode(T) ([]byte, error)
	Decode([]byte) (T, error)
}

// JSONCodec is the default Codec — encoding/json over the wire.
// Works for any T whose JSON wire shape is additive across versions.
// Pluggable: implementations for protobuf / msgpack / etc. just need
// to satisfy the Codec[T] interface for the T they target.
type JSONCodec[T any] struct{}

// Encode marshals the value to JSON.
func (JSONCodec[T]) Encode(v T) ([]byte, error) {
	return json.Marshal(v)
}

// Decode unmarshals the bytes into a T. Returns the zero value of T
// on error.
func (JSONCodec[T]) Decode(b []byte) (T, error) {
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return v, err
	}
	return v, nil
}
