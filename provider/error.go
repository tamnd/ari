package provider

// Error classification. The loop (doc 03 section 12) routes a failed turn
// to retry, compaction, fallback, or termination by class, never by
// string matching, so a provider that classifies honestly gets correct
// recovery for free. The dialects map wire errors to these classes; the
// scripted provider scripts them directly for loop tests.

// ErrClass names one recovery route.
type ErrClass string

const (
	// ClassPromptTooLong is the request exceeding the model's context
	// window. The loop answers with one reactive compaction, guarded.
	ClassPromptTooLong ErrClass = "prompt_too_long"

	// ClassOverloaded is the provider shedding load (429, 529). Retried
	// with backoff, then fallback.
	ClassOverloaded ErrClass = "overloaded"

	// ClassTransient is a retryable infrastructure hiccup: a reset
	// connection, a 5xx, a timeout. Same route as overloaded.
	ClassTransient ErrClass = "transient"

	// ClassFatal is everything else: bad request, bad auth, a model that
	// does not exist. Retrying cannot help; the loop terminates.
	ClassFatal ErrClass = "fatal"
)

// Error is a classified provider failure. JSON tags exist because the
// scripted provider stores these in replay fixtures.
type Error struct {
	Class   ErrClass `json:"class"`
	Message string   `json:"message,omitempty"`
	Status  int      `json:"status,omitempty"` // HTTP status when one exists
}

func (e *Error) Error() string {
	if e.Message == "" {
		return string(e.Class)
	}
	return e.Message
}

// Retryable reports whether another attempt could succeed.
func (e *Error) Retryable() bool {
	return e.Class == ClassOverloaded || e.Class == ClassTransient
}
