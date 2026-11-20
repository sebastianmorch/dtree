package ui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/sebastianmorch/dtree/internal/tree"
)

func TestStopAndStartContainer(t *testing.T) {
	m, f := setup(t)
	press(t, m, "a", "j", "S") // all containers, api
	if want := []string{"stop a1"}; !slices.Equal(calls(f), want) {
		t.Fatalf("calls %v, want %v", calls(f), want)
	}
	s := screen(m)
	if !strings.Contains(s, "› stopped api") || !strings.Contains(s, "1/2 running") {
		t.Errorf("api should be shown stopped:\n%s", s)
	}
	if c := m.tree.SelectedContainer(); c == nil || c.ID != "a1" {
		t.Fatalf("cursor should stay on api, got %+v", c)
	}
	press(t, m, "S")
	if want := []string{"start a1", "stop a1"}; !slices.Equal(calls(f), want) {
		t.Errorf("calls %v, want %v", calls(f), want)
	}
	if s := screen(m); !strings.Contains(s, "› started api") || !strings.Contains(s, "2/2 running") {
		t.Errorf("api should be running again:\n%s", s)
	}
}

func TestStopHintsAtShowAll(t *testing.T) {
	m, _ := setup(t)
	press(t, m, "j", "S")
	s := screen(m)
	if !strings.Contains(s, "stopped api · press a to show stopped containers") {
		t.Errorf("missing hint:\n%s", s)
	}
	if strings.Contains(s, "shop-api-1") || strings.Contains(s, "  ● api") {
		t.Errorf("a stopped container should leave the running list:\n%s", s)
	}
}

func TestRestartContainer(t *testing.T) {
	m, f := setup(t)
	press(t, m, "j", "R")
	if want := []string{"restart a1"}; !slices.Equal(calls(f), want) {
		t.Errorf("calls %v, want %v", calls(f), want)
	}
	if s := screen(m); !strings.Contains(s, "› restarted api") {
		t.Errorf("missing status:\n%s", s)
	}
}

// calls returns the fake's lifecycle calls sorted, since a project's
// containers change state in parallel.
func calls(f *fakeClient) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Sorted(slices.Values(f.lifecycle))
}

func TestStopAndStartProject(t *testing.T) {
	m, f := setup(t)
	f.running[1].State, f.running[1].Status = "exited", "Exited (0) 1 hour ago"
	f.all = []tree.Container{f.running[1]}
	f.running = slices.Delete(f.running, 1, 2)
	press(t, m, "a", "S") // the cursor is on the shop project; only api runs
	if want := []string{"stop a1"}; !slices.Equal(calls(f), want) {
		t.Fatalf("calls %v, want %v", calls(f), want)
	}
	if s := screen(m); !strings.Contains(s, "› stopped shop (1 container)") || !strings.Contains(s, "0/2 running") {
		t.Errorf("shop should be stopped:\n%s", s)
	}
	f.lifecycle = nil
	press(t, m, "S")
	if want := []string{"start a1", "start a2"}; !slices.Equal(calls(f), want) {
		t.Errorf("calls %v, want %v", calls(f), want)
	}
	if s := screen(m); !strings.Contains(s, "› started shop (2 containers)") || !strings.Contains(s, "2/2 running") {
		t.Errorf("shop should be running:\n%s", s)
	}
}

func TestRestartProject(t *testing.T) {
	m, f := setup(t)
	press(t, m, "R")
	if want := []string{"restart a1", "restart a2"}; !slices.Equal(calls(f), want) {
		t.Errorf("calls %v, want %v", calls(f), want)
	}
	if s := screen(m); !strings.Contains(s, "› restarted shop (2 containers)") {
		t.Errorf("missing status:\n%s", s)
	}
}

func TestProjectProgressAndBusyContainers(t *testing.T) {
	m, f := setup(t)
	f.lifecycleHold = make(chan struct{})
	press(t, m, "j", "R") // restart api alone
	press(t, m, "k", "S") // then stop shop: only db is left to stop
	if s := screen(m); !strings.Contains(s, "▾ shop          restarting…") {
		t.Errorf("project row should show progress:\n%s", s)
	}
	press(t, m, "S")
	if s := screen(m); !strings.Contains(s, "shop is busy") {
		t.Errorf("a fully busy project should be refused:\n%s", s)
	}
	close(f.lifecycleHold)
	settle(t, m)
	if want := []string{"restart a1", "stop a2"}; !slices.Equal(calls(f), want) {
		t.Errorf("calls %v, want %v", calls(f), want)
	}
}

func TestStandaloneIsNotAProject(t *testing.T) {
	m, f := setup(t)
	press(t, m, "G", "k") // the (standalone) row
	for _, key := range []string{"S", "R"} {
		press(t, m, key)
		if s := screen(m); !strings.Contains(s, "standalone containers are not a project") {
			t.Errorf("%s on (standalone): missing status:\n%s", key, s)
		}
	}
	if len(calls(f)) > 0 {
		t.Errorf("unexpected calls %v", f.lifecycle)
	}
}

func TestProjectPartialFailure(t *testing.T) {
	m, f := setup(t)
	f.lifecycleErr = errors.New("permission denied")
	press(t, m, "S")
	if s := screen(m); !strings.Contains(s, "✗ could not stop 2 of 2 in shop · ") {
		t.Errorf("missing error:\n%s", s)
	}
}

func TestLifecycleShowsProgress(t *testing.T) {
	m, f := setup(t)
	f.lifecycleHold = make(chan struct{})
	press(t, m, "j", "S")
	s := screen(m)
	if !strings.Contains(s, "stopping…") {
		t.Errorf("row should show the stop in progress:\n%s", s)
	}
	press(t, m, "R")
	if s := screen(m); !strings.Contains(s, "api is already stopping…") {
		t.Errorf("a second action should be refused:\n%s", s)
	}
	close(f.lifecycleHold)
	settle(t, m)
	if want := []string{"stop a1"}; !slices.Equal(calls(f), want) {
		t.Errorf("calls %v, want %v", calls(f), want)
	}
	if s := screen(m); strings.Contains(s, "stopping…") || !strings.Contains(s, "stopped api") {
		t.Errorf("progress should be replaced by the result:\n%s", s)
	}
}

func TestLifecycleError(t *testing.T) {
	m, f := setup(t)
	f.lifecycleErr = errors.New("permission denied")
	press(t, m, "j", "S")
	s := screen(m)
	if !strings.Contains(s, "✗ could not stop api: permission denied") {
		t.Errorf("missing error:\n%s", s)
	}
	if strings.Contains(s, "stopping…") {
		t.Errorf("progress should be cleared:\n%s", s)
	}
}
