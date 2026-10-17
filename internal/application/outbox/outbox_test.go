package outbox_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/claudioed/task-interleaving/internal/application/outbox"
)

func TestSendFailureNamesTheRowAndUnwrapsTheCause(t *testing.T) {
	cause := errors.New("broker down")
	f := &outbox.SendFailure{
		RowID:    42,
		Message:  outbox.Message{EventType: "com.warehouse.wes.task-interleaving.stationtrip.InterleavingHintIssued"},
		Attempts: 3,
		Err:      cause,
	}
	msg := f.Error()
	for _, want := range []string{"outbox row 42", "InterleavingHintIssued", "broker down"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("Error() = %q, want it to contain %q", msg, want)
		}
	}
	if !errors.Is(f, cause) {
		t.Fatal("a SendFailure must unwrap to its cause")
	}
	var as *outbox.SendFailure
	if !errors.As(error(f), &as) || as.Attempts != 3 {
		t.Fatalf("errors.As = %+v", as)
	}
}
