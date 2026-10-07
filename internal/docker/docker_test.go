package docker

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
)

func TestFormatPorts(t *testing.T) {
	tests := []struct {
		name  string
		ports []container.Port
		want  string
	}{
		{"none", nil, ""},
		{"unpublished", []container.Port{{PrivatePort: 5432, Type: "tcp"}}, "5432"},
		{
			"ipv4 and ipv6 duplicates collapse",
			[]container.Port{
				{IP: "0.0.0.0", PrivatePort: 80, PublicPort: 8080, Type: "tcp"},
				{IP: "::", PrivatePort: 80, PublicPort: 8080, Type: "tcp"},
			},
			"8080->80",
		},
		{
			"sorted, udp and bound IPs shown",
			[]container.Port{
				{PrivatePort: 53, Type: "udp"},
				{IP: "127.0.0.1", PrivatePort: 6379, PublicPort: 6379, Type: "tcp"},
				{IP: "::1", PrivatePort: 22, PublicPort: 2222, Type: "tcp"},
			},
			"[::1]:2222->22, 53/udp, 127.0.0.1:6379->6379",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatPorts(tt.ports); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestShortImage(t *testing.T) {
	if got := shortImage("sha256:0123456789abcdef0123"); got != "0123456789ab" {
		t.Errorf("got %q", got)
	}
	if got := shortImage("postgres:16"); got != "postgres:16" {
		t.Errorf("got %q", got)
	}
}

func TestLineWriterDemuxPreservesOrder(t *testing.T) {
	// Build a multiplexed stream like the daemon sends for non-TTY containers,
	// with a line split across frames and a trailing line without newline.
	var raw bytes.Buffer
	out := stdcopy.NewStdWriter(&raw, stdcopy.Stdout)
	errw := stdcopy.NewStdWriter(&raw, stdcopy.Stderr)
	out.Write([]byte("one\r\ntw"))
	out.Write([]byte("o\n"))
	errw.Write([]byte("boom\n"))
	out.Write([]byte("three"))

	ch := make(chan LogLine, 10)
	ctx := context.Background()
	so := &lineWriter{ctx: ctx, ch: ch}
	se := &lineWriter{ctx: ctx, ch: ch, stderr: true}
	if _, err := stdcopy.StdCopy(so, se, &raw); err != nil {
		t.Fatal(err)
	}
	so.Flush()
	se.Flush()
	close(ch)

	var got []LogLine
	for l := range ch {
		got = append(got, l)
	}
	want := []LogLine{{"one", false}, {"two", false}, {"boom", true}, {"three", false}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
