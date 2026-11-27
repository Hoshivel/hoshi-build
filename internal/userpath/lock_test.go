package userpath

import (
	"errors"
	"reflect"
	"testing"
)

type callbackStore struct {
	read  func() (pathValue, error)
	write func(pathValue) error
}

func (s callbackStore) Read() (pathValue, error)    { return s.read() }
func (s callbackStore) Write(value pathValue) error { return s.write(value) }

func TestManagerWritesUnderLockAndNotifiesAfterRelease(t *testing.T) {
	var events []string
	value := pathValue{true, stringValue, `C:\Other`}
	store := callbackStore{
		read:  func() (pathValue, error) { events = append(events, "read"); return value, nil },
		write: func(next pathValue) error { events = append(events, "write"); value = next; return nil },
	}
	m, err := newManager(`C:\hoshi`, store, nil, func() error { events = append(events, "notify"); return nil })
	if err != nil {
		t.Fatal(err)
	}
	m.acquire = func() (func() error, error) {
		events = append(events, "acquire")
		return func() error { events = append(events, "release"); return nil }, nil
	}
	if _, err := m.Add(); err != nil {
		t.Fatal(err)
	}
	want := []string{"acquire", "read", "read", "write", "release", "notify"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("operation order=%v, want %v", events, want)
	}
}

func TestManagerDoesNotReadOrWriteWhenLockCannotBeAcquired(t *testing.T) {
	store := &memoryStore{}
	m := managerFor(t, `C:\hoshi`, store)
	lockError := errors.New("lock timed out")
	m.acquire = func() (func() error, error) { return nil, lockError }
	_, err := m.Add()
	if !errors.Is(err, lockError) || store.reads != 0 || store.writes != 0 {
		t.Fatalf("error=%v reads=%d writes=%d", err, store.reads, store.writes)
	}
}

func TestManagerReleasesOnNoOpReadAndWriteFailure(t *testing.T) {
	for _, scenario := range []string{"no-op", "read failure", "write failure"} {
		t.Run(scenario, func(t *testing.T) {
			store := &memoryStore{value: pathValue{true, stringValue, `C:\hoshi`}}
			if scenario == "read failure" {
				store.readErr = errors.New("read denied")
			}
			if scenario == "write failure" {
				store.value.Raw = `C:\Other`
				store.writeErr = errors.New("write denied")
			}
			m := managerFor(t, `C:\hoshi`, store)
			releases := 0
			m.acquire = func() (func() error, error) {
				return func() error { releases++; return nil }, nil
			}
			_, err := m.Add()
			if releases != 1 || (err != nil) != (scenario != "no-op") {
				t.Fatalf("releases=%d error=%v", releases, err)
			}
		})
	}
}

func TestManagerPreservesBothWriteAndReleaseErrors(t *testing.T) {
	writeError, releaseError := errors.New("write denied"), errors.New("release failed")
	store := &memoryStore{writeErr: writeError}
	m := managerFor(t, `C:\hoshi`, store)
	m.acquire = func() (func() error, error) { return func() error { return releaseError }, nil }
	_, err := m.Add()
	if !errors.Is(err, writeError) || !errors.Is(err, releaseError) {
		t.Fatalf("joined error lost a cause: %v", err)
	}
}

func TestManagerReleasesTheLockWhenStorePanics(t *testing.T) {
	panicValue := "unexpected store panic"
	released := false
	m, err := newManager(`C:\hoshi`, callbackStore{read: func() (pathValue, error) { panic(panicValue) }}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.acquire = func() (func() error, error) { return func() error { released = true; return nil }, nil }
	defer func() {
		if got := recover(); got != panicValue || !released {
			t.Errorf("panic=%v released=%v", got, released)
		}
	}()
	_, _ = m.Add()
	t.Fatal("store panic was swallowed")
}
