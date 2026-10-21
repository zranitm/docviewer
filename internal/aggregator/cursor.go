package aggregator

// State <-> opaque token. This is the only file that knows the token's
// format (HLD section 7): base64url of JSON with v, query and streams.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"docviewer/internal/aggregator/source"
	"docviewer/internal/model"
)

// cursorVersion changes only when the meaning of an existing field changes.
const cursorVersion = 1

// keyParts is the sort key's layout: [date, id] for newest first.
const keyParts = 2

// cursorToken is the JSON inside the base64url string.
type cursorToken struct {
	V       int                     `json:"v"`
	Query   cursorQuery             `json:"query"`
	Streams map[string]cursorStream `json:"streams"`
}

type cursorQuery struct {
	VIN string `json:"vin"` // new Query fields join here, omitempty, with a default
}

type cursorStream struct {
	State Phase    `json:"state"`         // written as is; decodeCursor checks Phase.Valid
	Key   []string `json:"key,omitempty"` // [date, id] for newest first
}

var cursorEncoding = base64.RawURLEncoding

// encodeCursor turns a paging State into the token clients hold.
func encodeCursor(s State) string {
	tok := cursorToken{V: cursorVersion, Query: toCursorQuery(s.Query), Streams: make(map[string]cursorStream, len(s.Sources))}
	for name, ss := range s.Sources {
		cs := cursorStream{State: ss.Phase}
		if ss.Phase == PhaseAfter && ss.At != nil {
			cs.Key = []string{ss.At.IssuedAt.UTC().Format(time.RFC3339Nano), ss.At.ID}
		}
		tok.Streams[string(name)] = cs
	}
	b, err := json.Marshal(tok)
	if err != nil {
		panic(fmt.Sprintf("cursor: marshal: %v", err)) // only strings and a map of structs: cannot fail
	}
	return cursorEncoding.EncodeToString(b)
}

func toCursorQuery(q Query) cursorQuery { return cursorQuery{VIN: string(q.VIN)} }

// decodeCursor is strict: bad encoding, an unknown version, any unknown field,
// an invalid phase or a malformed key is ErrInvalidCursor, so a token written
// by newer code is refused rather than half-read. Its query must equal want,
// and every source in sources must be present (ErrCursorOutdated); unknown
// sources are dropped.
func decodeCursor(raw string, want Query, sources []model.Source) (State, error) {
	tok, err := parseCursor(raw)
	if err != nil {
		return State{}, fmt.Errorf("%w: %w", ErrInvalidCursor, err)
	}
	if tok.V != cursorVersion {
		return State{}, fmt.Errorf("%w: version %d", ErrInvalidCursor, tok.V)
	}
	if tok.Query != toCursorQuery(want) {
		return State{}, fmt.Errorf("%w: query mismatch", ErrInvalidCursor)
	}
	if tok.Streams == nil {
		return State{}, fmt.Errorf("%w: no streams", ErrInvalidCursor)
	}
	st := State{Query: want, Sources: make(map[model.Source]SourceState, len(sources))}
	for _, name := range sources {
		cs, ok := tok.Streams[string(name)]
		if !ok {
			return State{}, fmt.Errorf("%w: no entry for source %q", ErrCursorOutdated, name)
		}
		ss, err := toSourceState(cs)
		if err != nil {
			return State{}, fmt.Errorf("%w: source %q: %w", ErrInvalidCursor, name, err)
		}
		st.Sources[name] = ss
	}
	return st, nil
}

// parseCursor decodes the encoding and the JSON, refusing unknown fields and
// trailing data.
func parseCursor(raw string) (cursorToken, error) {
	var tok cursorToken
	b, err := cursorEncoding.DecodeString(raw)
	if err != nil {
		return tok, errors.New("not base64url")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&tok); err != nil {
		return tok, fmt.Errorf("not a cursor document: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return tok, errors.New("trailing data")
	}
	return tok, nil
}

// toSourceState checks one stream: a valid phase, and a [date, id] key
// exactly when the phase is after.
func toSourceState(cs cursorStream) (SourceState, error) {
	if !cs.State.Valid() {
		return SourceState{}, fmt.Errorf("unknown state %q", cs.State)
	}
	if cs.State != PhaseAfter {
		if len(cs.Key) != 0 {
			return SourceState{}, fmt.Errorf("state %q carries a key", cs.State)
		}
		return SourceState{Phase: cs.State}, nil
	}
	if len(cs.Key) != keyParts {
		return SourceState{}, fmt.Errorf("key has %d parts, want %d", len(cs.Key), keyParts)
	}
	issuedAt, err := time.Parse(time.RFC3339Nano, cs.Key[0])
	if err != nil {
		return SourceState{}, errors.New("key date does not parse")
	}
	if cs.Key[1] == "" {
		return SourceState{}, errors.New("key id is empty")
	}
	return SourceState{Phase: PhaseAfter, At: &source.Position{IssuedAt: issuedAt, ID: cs.Key[1]}}, nil
}
