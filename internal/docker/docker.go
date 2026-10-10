// Package docker wraps the Docker Engine SDK behind a small interface the UI
// depends on, so the UI can be tested with a fake.
package docker

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/sebastianmorch/dtree/internal/tree"
)

const (
	labelProject     = "com.docker.compose.project"
	labelService     = "com.docker.compose.service"
	labelWorkingDir  = "com.docker.compose.project.working_dir"
	labelConfigFiles = "com.docker.compose.project.config_files"
)

// LogLine is one line of container output.
type LogLine struct {
	Text   string
	Stderr bool
}

type LogOptions struct {
	Tail       int
	Follow     bool
	Timestamps bool
}

// ExecResult is how a command started with Exec ended.
type ExecResult struct {
	ExitCode int
	Err      error // set when the command's outcome is unknown
}

// ExecOptions describes a command for Exec. Width and Height size its
// terminal.
type ExecOptions struct {
	Command       string
	Width, Height int
}

// ExecSession is a running Exec command.
type ExecSession struct {
	// Output streams the raw terminal output, escape sequences included,
	// and closes when the command ends; then Result receives exactly one
	// value.
	Output <-chan []byte
	Result <-chan ExecResult
	// Input types p into the command's terminal. Calls must not overlap.
	Input func(p []byte) error
	// Interrupt types Ctrl+C into the command's terminal, which signals the
	// command and everything it started. With force it kills them instead,
	// for commands that ignore Ctrl+C.
	Interrupt func(force bool) error
}

// Client is everything the UI needs from Docker. Interactive shells are not
// part of it: the UI runs the docker CLI directly so it gets proper TTY
// handling.
type Client interface {
	List(ctx context.Context, all bool) ([]tree.Container, error)
	// Events emits a value whenever a container changes. Bursts are
	// coalesced. The error channel reports a broken stream.
	Events(ctx context.Context) (<-chan struct{}, <-chan error)
	// Logs streams log lines until the stream ends or ctx is cancelled, then
	// closes the channel.
	Logs(ctx context.Context, id string, opts LogOptions) (<-chan LogLine, error)
	// Inspect returns the raw JSON of docker inspect.
	Inspect(ctx context.Context, id string) ([]byte, error)
	// Exec runs a command with sh -c in a terminal in the container and
	// streams its output. Docker cannot kill an exec'd process, so it is
	// stopped through its terminal with Interrupt; cancelling ctx only stops
	// the stream.
	Exec(ctx context.Context, id string, opts ExecOptions) (*ExecSession, error)
}

// SDK is the Client backed by the Docker Engine API.
type SDK struct {
	cli *client.Client
}

// New connects to the daemon selected by DOCKER_HOST or, failing that, the
// current docker CLI context (Docker Desktop, OrbStack, Colima, ...).
func New(ctx context.Context) (*SDK, error) {
	opts := []client.Opt{client.FromEnv, client.WithAPIVersionNegotiation()}
	if os.Getenv("DOCKER_HOST") == "" {
		if host := contextHost(ctx); host != "" {
			opts = append(opts, client.WithHost(host))
		}
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := cli.Ping(pctx); err != nil {
		cli.Close()
		return nil, fmt.Errorf("cannot connect to Docker at %s: %w", cli.DaemonHost(), err)
	}
	return &SDK{cli: cli}, nil
}

func (s *SDK) Close() error { return s.cli.Close() }

// contextHost asks the docker CLI for the endpoint of the current context.
// TLS-secured contexts are not supported; those need DOCKER_HOST and
// DOCKER_CERT_PATH set explicitly.
func contextHost(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (s *SDK) List(ctx context.Context, all bool) ([]tree.Container, error) {
	cs, err := s.cli.ContainerList(ctx, container.ListOptions{All: all})
	if err != nil {
		return nil, err
	}
	out := make([]tree.Container, 0, len(cs))
	for _, c := range cs {
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		out = append(out, tree.Container{
			ID:      c.ID,
			Name:    name,
			Project: c.Labels[labelProject],
			Service: c.Labels[labelService],
			Image:   shortImage(c.Image),
			State:   string(c.State),
			Status:  c.Status,
			Ports:   FormatPorts(c.Ports),

			WorkingDir:  c.Labels[labelWorkingDir],
			ConfigFiles: splitList(c.Labels[labelConfigFiles]),
			Networks:    networks(c),
			Volumes:     volumes(c),
			HostPorts:   hostPorts(c.Ports),
		})
	}
	return out, nil
}

func splitList(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func networks(c container.Summary) []string {
	if c.NetworkSettings == nil {
		return nil
	}
	var out []string
	for name := range c.NetworkSettings.Networks {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func volumes(c container.Summary) []string {
	var out []string
	for _, m := range c.Mounts {
		if m.Type == mount.TypeVolume && m.Name != "" {
			out = append(out, m.Name)
		}
	}
	sort.Strings(out)
	return out
}

func hostPorts(ports []container.Port) []int {
	seen := map[int]bool{}
	var out []int
	for _, p := range ports {
		if p.PublicPort != 0 && !seen[int(p.PublicPort)] {
			seen[int(p.PublicPort)] = true
			out = append(out, int(p.PublicPort))
		}
	}
	sort.Ints(out)
	return out
}

// shortImage shortens untagged image references ("sha256:<64 hex>").
func shortImage(image string) string {
	if h, ok := strings.CutPrefix(image, "sha256:"); ok && len(h) > 12 {
		return h[:12]
	}
	return image
}

// FormatPorts renders ports compactly: "8080->80, 5432, 53/udp". The host IP
// is omitted when bound to all interfaces, IPv4/IPv6 duplicates collapse, and
// consecutive ports merge into ranges ("8081-8083->8081-8083").
func FormatPorts(ports []container.Port) string {
	type mapping struct {
		host        string // "" for all interfaces, else "IP:" or "[IPv6]:"
		proto       string
		priv, pub   int // pub is 0 when not published
		privN, pubN int // last port of a merged range
	}
	seen := map[mapping]bool{}
	var ms []mapping
	for _, p := range ports {
		m := mapping{proto: p.Type, priv: int(p.PrivatePort), pub: int(p.PublicPort)}
		if m.proto == "" {
			m.proto = "tcp"
		}
		if m.pub != 0 {
			switch {
			case p.IP == "" || p.IP == "0.0.0.0" || p.IP == "::":
			case strings.Contains(p.IP, ":"):
				m.host = "[" + p.IP + "]:"
			default:
				m.host = p.IP + ":"
			}
		}
		if !seen[m] {
			seen[m] = true
			ms = append(ms, m)
		}
	}
	sort.Slice(ms, func(i, j int) bool {
		a, b := ms[i], ms[j]
		if a.priv != b.priv {
			return a.priv < b.priv
		}
		if a.pub != b.pub {
			return a.pub < b.pub
		}
		if a.host != b.host {
			return a.host < b.host
		}
		return a.proto < b.proto
	})

	var merged []mapping
	for _, m := range ms {
		m.privN, m.pubN = m.priv, m.pub
		for i := len(merged) - 1; i >= 0; i-- {
			last := &merged[i]
			if last.host == m.host && last.proto == m.proto && last.privN+1 == m.priv &&
				((last.pub == 0 && m.pub == 0) || (last.pub != 0 && last.pubN+1 == m.pub)) {
				last.privN, last.pubN = m.priv, m.pub
				m.priv = -1
				break
			}
		}
		if m.priv >= 0 {
			merged = append(merged, m)
		}
	}

	portRange := func(from, to int) string {
		if from == to {
			return strconv.Itoa(from)
		}
		return strconv.Itoa(from) + "-" + strconv.Itoa(to)
	}
	parts := make([]string, 0, len(merged))
	for _, m := range merged {
		s := portRange(m.priv, m.privN)
		if m.proto != "tcp" {
			s += "/" + m.proto
		}
		if m.pub != 0 {
			s = m.host + portRange(m.pub, m.pubN) + "->" + s
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}

func (s *SDK) Events(ctx context.Context) (<-chan struct{}, <-chan error) {
	f := filters.NewArgs(filters.Arg("type", string(events.ContainerEventType)))
	for _, e := range []string{"create", "start", "restart", "die", "stop", "destroy", "pause", "unpause", "rename", "health_status"} {
		f.Add("event", e)
	}
	msgs, errs := s.cli.Events(ctx, events.ListOptions{Filters: f})
	out := make(chan struct{}, 1)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-msgs:
				if !ok {
					return
				}
				select {
				case out <- struct{}{}:
				default: // a notification is already pending
				}
			}
		}
	}()
	return out, errs
}

func (s *SDK) Logs(ctx context.Context, id string, opts LogOptions) (<-chan LogLine, error) {
	info, err := s.cli.ContainerInspect(ctx, id)
	if err != nil {
		return nil, err
	}
	rc, err := s.cli.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     opts.Follow,
		Timestamps: opts.Timestamps,
		Tail:       strconv.Itoa(opts.Tail),
	})
	if err != nil {
		return nil, err
	}
	ch := make(chan LogLine, 512)
	go func() {
		defer close(ch)
		defer rc.Close()
		stdout := &lineWriter{ctx: ctx, ch: ch}
		stderr := &lineWriter{ctx: ctx, ch: ch, stderr: true}
		var err error
		if info.Config != nil && info.Config.Tty {
			// TTY containers have a single raw stream; stdout and stderr are mixed.
			_, err = io.Copy(stdout, rc)
		} else {
			_, err = stdcopy.StdCopy(stdout, stderr, rc)
		}
		stdout.Flush()
		stderr.Flush()
		if err != nil && ctx.Err() == nil {
			select {
			case ch <- LogLine{Text: "error reading logs: " + err.Error(), Stderr: true}:
			case <-ctx.Done():
			}
		}
	}()
	return ch, nil
}

func (s *SDK) Inspect(ctx context.Context, id string) ([]byte, error) {
	_, raw, err := s.cli.ContainerInspectWithRaw(ctx, id, false)
	return raw, err
}

func (s *SDK) Exec(ctx context.Context, id string, opts ExecOptions) (*ExecSession, error) {
	size := &[2]uint{uint(max(1, opts.Height)), uint(max(1, opts.Width))}
	created, err := s.cli.ContainerExecCreate(ctx, id, container.ExecOptions{
		// The shell reports its PID, which leads the command's process group,
		// then becomes the command.
		Cmd:          []string{"sh", "-c", `echo "$$"; exec sh -c "$1"`, "sh", opts.Command},
		Env:          []string{"PAGER=cat"}, // a pager would wait for keys that never come
		Tty:          true,
		ConsoleSize:  size,
		AttachStdin:  true, // the terminal's input carries Ctrl+C
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return nil, err
	}
	resp, err := s.cli.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{Tty: true, ConsoleSize: size})
	if err != nil {
		return nil, err
	}
	output := make(chan []byte, 64)
	result := make(chan ExecResult, 1)
	var pgid atomic.Int64
	go func() {
		defer resp.Close()
		stop := context.AfterFunc(ctx, resp.Close) // unblocks the read on cancel
		defer stop()
		r := bufio.NewReader(resp.Reader)
		first, err := r.ReadString('\n')
		if pid, perr := strconv.Atoi(strings.TrimSpace(first)); perr == nil {
			pgid.Store(int64(pid))
		}
		// A terminal has a single stream; stdout and stderr are mixed.
		if err == nil {
			_, err = io.Copy(&chunkWriter{ctx: ctx, ch: output}, r)
		}
		close(output)
		if err != nil {
			result <- ExecResult{ExitCode: -1, Err: err}
			return
		}
		result <- s.execExit(ctx, created.ID)
	}()
	interrupt := func(force bool) error {
		if !force {
			_, err := io.WriteString(resp.Conn, "\x03")
			return err
		}
		pid := pgid.Load()
		if pid == 0 {
			return fmt.Errorf("process ID not known yet")
		}
		return s.kill(id, pid)
	}
	input := func(p []byte) error {
		_, err := resp.Conn.Write(p)
		return err
	}
	return &ExecSession{Output: output, Result: result, Input: input, Interrupt: interrupt}, nil
}

// kill sends SIGKILL to a process group in the container through a second
// exec, since the Engine API has no way to signal an exec'd process.
func (s *SDK) kill(id string, pgid int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	created, err := s.cli.ContainerExecCreate(ctx, id, container.ExecOptions{
		Cmd: []string{"kill", "-KILL", "--", "-" + strconv.FormatInt(pgid, 10)},
	})
	if err != nil {
		return err
	}
	// kill's exit code is not checked: busybox reports 1 even when the
	// group was killed. A command that survives can be killed again.
	return s.cli.ContainerExecStart(ctx, created.ID, container.ExecStartOptions{})
}

// execExit reads the exit code of a finished exec. The daemon can report the
// exec as running for a moment after its output stream has closed.
func (s *SDK) execExit(ctx context.Context, execID string) ExecResult {
	for range 20 {
		info, err := s.cli.ContainerExecInspect(ctx, execID)
		if err != nil {
			return ExecResult{ExitCode: -1, Err: err}
		}
		if !info.Running {
			return ExecResult{ExitCode: info.ExitCode}
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			return ExecResult{ExitCode: -1, Err: ctx.Err()}
		}
	}
	return ExecResult{ExitCode: -1, Err: fmt.Errorf("exit code not available")}
}

// chunkWriter sends copies of what is written on ch.
type chunkWriter struct {
	ctx context.Context
	ch  chan<- []byte
}

func (w *chunkWriter) Write(p []byte) (int, error) {
	select {
	case w.ch <- append([]byte(nil), p...):
		return len(p), nil
	case <-w.ctx.Done():
		return 0, w.ctx.Err()
	}
}

// lineWriter splits a byte stream into lines and sends them on ch. stdcopy
// writes frames sequentially, so stdout/stderr ordering is preserved.
type lineWriter struct {
	ctx    context.Context
	ch     chan<- LogLine
	stderr bool
	buf    []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSuffix(string(w.buf[:i]), "\r")
		w.buf = w.buf[i+1:]
		if err := w.emit(line); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Flush emits a trailing line that had no newline.
func (w *lineWriter) Flush() {
	if len(w.buf) > 0 {
		_ = w.emit(strings.TrimSuffix(string(w.buf), "\r"))
		w.buf = nil
	}
}

func (w *lineWriter) emit(text string) error {
	select {
	case w.ch <- LogLine{Text: text, Stderr: w.stderr}:
		return nil
	case <-w.ctx.Done():
		return w.ctx.Err()
	}
}
