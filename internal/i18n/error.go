package i18n

import "errors"

// Error is a failure that knows what it wants to say in any language.
//
// An error made with fmt.Errorf carries a finished sentence, and a finished
// sentence is in one language. These carry the message id and its arguments
// instead, and are rendered when somebody is about to read them — by which
// point the request has said which language that should be.
//
// Error() renders in the default language, so a wrapped one still reads
// properly in the journal and in %w chains.
type Error struct {
	ID   string
	Args []any
	// Wrapped is the underlying failure, kept so errors.Is and errors.As still
	// work through this. It is never shown: it is in Go's language, not the
	// reader's.
	Wrapped error
}

// Errf makes a translatable error.
func Errf(id string, args ...any) *Error { return &Error{ID: id, Args: args} }

// Wrap makes one that keeps the cause.
func Wrap(err error, id string, args ...any) *Error {
	return &Error{ID: id, Args: args, Wrapped: err}
}

func (e *Error) Error() string { return T(Default, e.ID, e.Args...) }

// Render writes the error in one language, with any Msg arguments in it.
func (e *Error) Render(l Lang) string { return T(l, e.ID, e.Args...) }

func (e *Error) Unwrap() error { return e.Wrapped }

// Translate renders an error for a reader.
//
// Anything that is not translatable comes back as it is. That is the honest
// answer for a failure from the operating system or the network: it was never
// written for this reader, and inventing a translation of "connection refused"
// would hide which one it was.
func Translate(l Lang, err error) string {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Render(l)
	}
	return err.Error()
}

// Msg marks an argument that is itself a message id.
//
// "%s has to be between %g and %g %s" takes the name of a setting and its unit,
// and both of those are catalogue entries too. Passing them as plain strings
// printed the ids onto the screen; wrapping them says look these up as well,
// in whatever language the sentence around them is being rendered in.
type Msg string

// resolve renders any Msg arguments in the same language as the message that
// holds them.
func resolve(l Lang, args []any) []any {
	out := make([]any, len(args))
	for i, a := range args {
		if m, ok := a.(Msg); ok {
			out[i] = T(l, string(m))
			continue
		}
		out[i] = a
	}
	return out
}
