package store

// kind is a store error of a fixed kind (domain.FixedKind): its text is
// written in the code and holds nothing of a request, so a log may show it
// however a caller wraps it with %w, and an operator still sees why storage
// failed (PR #137: every other error is logged without its text). wraps makes
// ErrLost an ErrNotFound.
type kind struct {
	msg   string
	wraps error
}

func (k *kind) Error() string   { return k.msg }
func (k *kind) LogKind() string { return k.msg }
func (k *kind) Unwrap() error   { return k.wraps }
