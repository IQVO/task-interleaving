package http

// replayClass is what the Idempotency-Key middleware does with the refusal of
// a retried request (ADR 0013, fleet rule .claude/rules/fleet/idempotency-and-outbox.md).
// Every problem type declares exactly one on its catalogue entry (errors.go);
// the error writer tells the middleware's capturing writer, and the middleware
// holds no list of slugs and no status range beyond 2xx and 5xx.
//
//   - replayFinal: the answer is a function of the request alone. The refusal
//     is stored under the key and replayed; the handler's writes roll back.
//   - replayOutcome: the command COMMITTED state and then answered an error
//     that must be remembered. The handler's writes commit in the same unit of
//     work as the stored answer. No problem type of this service is one: the
//     only keyed route writes nothing (ADR 0013).
//   - replayRetryable: nothing committed and the answer depends on state that
//     can change. Nothing is stored and the key is released: a retry executes.
//
// When unsure between FINAL and RETRYABLE the class is RETRYABLE. A refusal
// that reaches the middleware without a class (a handler that wrote a 4xx by
// hand) is released like RETRYABLE. A 5xx is never stored whatever its class.
type replayClass string

const (
	replayFinal     replayClass = "FINAL"
	replayOutcome   replayClass = "OUTCOME"
	replayRetryable replayClass = "RETRYABLE"
)

// replayMarker is implemented by the middleware's capturing writer: the error
// writer tells it the class of the problem it is about to write.
type replayMarker interface{ MarkReplay(replayClass) }
