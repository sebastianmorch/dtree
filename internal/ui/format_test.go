package ui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/sebastianmorch/dtree/internal/tree"
)

func TestShortImage(t *testing.T) {
	tests := map[string]string{
		"594489062793.dkr.ecr.eu-central-1.amazonaws.com/test-hub:branch_master": "test-hub:branch_master",
		"quay.io/soketi/soketi:1.6.1-16-alpine":                                  "soketi/soketi:1.6.1-16-alpine",
		"localhost/app:dev":                                                      "app:dev",
		"localhost:5000/team/app":                                                "team/app",
		"docker.io/library/redis:7":                                              "redis:7",
		"redis:6.2-alpine":                                                       "redis:6.2-alpine",
		"moby/buildkit":                                                          "moby/buildkit",
		"ghcr.io/org/app:1.0@sha256:abcdef":                                      "org/app:1.0",
		"app@sha256:abcdef":                                                      "app@sha256:abcdef",
		"1991bd789d71":                                                           "1991bd789d71",
	}
	for in, want := range tests {
		if got := shortImage(in); got != want {
			t.Errorf("shortImage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShortStatus(t *testing.T) {
	tests := []struct {
		state, status, want, icon string
	}{
		{"running", "Up 16 hours", "16h", ""},
		{"running", "Up 16 hours (healthy)", "16h", "✅"},
		{"running", "Up 2 minutes (unhealthy)", "2m", "❌"},
		{"running", "Up 5 seconds (health: starting)", "5s", "⏳"},
		{"running", "Up About a minute", "1m", ""},
		{"running", "Up About an hour", "1h", ""},
		{"running", "Up Less than a second", "<1s", ""},
		{"running", "Up 3 days", "3d", ""},
		{"running", "Up 2 weeks", "2w", ""},
		{"running", "Up 4 months", "4mo", ""},
		{"running", "Up 2 years", "2y", ""},
		{"exited", "Exited (1) 5 minutes ago", "exited 5m", ""},
		{"exited", "Exited (137) About an hour ago", "exited 1h", ""},
		{"exited", "Exited", "exited", ""},
		{"paused", "Up 16 hours (Paused)", "paused", ""},
		{"restarting", "Restarting (1) 3 seconds ago", "restarting", ""},
		{"created", "Created", "created", ""},
	}
	for _, tt := range tests {
		c := tree.Container{State: tt.state, Status: tt.status}
		if got := shortStatus(c); got != tt.want {
			t.Errorf("shortStatus(%q) = %q, want %q", tt.status, got, tt.want)
		}
		if got := healthIcon(c); got != tt.icon {
			t.Errorf("healthIcon(%q) = %q, want %q", tt.status, got, tt.icon)
		}
	}
}

func TestWrapValue(t *testing.T) {
	tests := []struct {
		in    string
		width int
		want  []string
	}{
		{"short", 20, []string{"short"}},
		{"", 20, []string{""}},
		{
			"594489062793.dkr.ecr.eu-central-1.amazonaws.com/test-authorization-service:branch_master", 50,
			[]string{"594489062793.dkr.ecr.eu-central-1.amazonaws.com/", "test-authorization-service:branch_master"},
		},
		{
			"594489062793.dkr.ecr.eu-central-1.amazonaws.com/test-hub:x", 44,
			[]string{"594489062793.dkr.ecr.eu-central-1.amazonaws.", "com/test-hub:x"},
		},
		{
			"80->80, 443->443, 6006->6006, 8081-8083->8081-8083", 30,
			[]string{"80->80, 443->443, 6006->6006,", "8081-8083->8081-8083"},
		},
		{"abcdefghij", 4, []string{"abcd", "efgh", "ij"}},
	}
	for _, tt := range tests {
		got := wrapValue(tt.in, tt.width)
		for _, l := range got {
			if ansi.StringWidth(l) > tt.width {
				t.Errorf("wrapValue(%q, %d): line %q too wide", tt.in, tt.width, l)
			}
		}
		if strings.Join(got, "") != strings.ReplaceAll(tt.in, ", ", ",") && strings.Join(got, " ") != tt.in {
			t.Errorf("wrapValue(%q, %d) lost text: %q", tt.in, tt.width, got)
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("wrapValue(%q, %d) = %q, want %q", tt.in, tt.width, got, tt.want)
		}
	}
}

func TestWrapValueBreaksAtSpaces(t *testing.T) {
	got := wrapValue("tracking-code-creator-worker-local · 51f41892ba62", 44)
	want := []string{"tracking-code-creator-worker-local ·", "51f41892ba62"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestVolumeList(t *testing.T) {
	anon := strings.Repeat("6a8ff64b", 8)
	if got := volumeList([]string{anon, "local_composer", strings.Repeat("ab", 32)}); got != "local_composer, +2 anonymous" {
		t.Errorf("got %q", got)
	}
	if got := volumeList(nil); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestPortList(t *testing.T) {
	if got := portList([]int{80, 443, 6001, 6006, 8081, 8082, 8083, 33062}); got != "80, 443, 6001, 6006, 8081-8083, 33062" {
		t.Errorf("got %q", got)
	}
}

func TestImageTextKeepsText(t *testing.T) {
	for _, s := range []string{"acme/shop-api:1.4.2  ", "postgres:16", "moby/buildkit", "localhost:5000/x", "img@sha256:abc", "acme/very-long-na…", ""} {
		for _, sel := range []bool{false, true} {
			if got := ansi.Strip(imageText(s, painter{selected: sel})); got != s {
				t.Errorf("imageText(%q) = %q", s, got)
			}
		}
	}
}
