# dtree

A terminal UI that lists your Docker containers grouped by Compose project.
Expand a project to see its containers, then open their logs, inspect them or
open a shell, all with vim keys or the arrow keys.

- Containers are grouped by the `com.docker.compose.project` label; containers
  outside Compose are listed under **(standalone)**.
- The list updates live from Docker's event stream.
- An info panel below the list shows details for the selected container
  (image, status, ports) or project (directory, compose file, ports, networks,
  volumes).
- Logs and inspect open in a built-in viewer with search, follow and line wrap.
  Inspect output is shown as YAML.
- Quick commands: press `x` on a container, type a shell command and its
  output streams into a popup, with progress bars updating in place. Close the popup and the command keeps running;
  the container's row shows whether it is still running (⟳), succeeded (✓),
  failed (✗) or was stopped (■), and `X` brings the output back. `ctrl+c` in
  the popup stops a running command; press it again to kill a command that
  ignores it. When a command asks for input, press `i` to switch the popup to
  input mode: every key goes to the command until `esc` switches back to
  scrolling. Commands run with `sh -c` in a terminal, but the popup only shows
  their output line by line, so full-screen programs (`vim`, `top`) need `s`.
  Quitting dtree stops commands that are still running.

## Install

Requires Go and the `docker` CLI.

```sh
go install github.com/sebastianmorch/dtree@latest
```

The binary is installed to `$(go env GOPATH)/bin` (usually `~/go/bin`). Make
sure that directory is on your `PATH`:

```sh
export PATH="$HOME/go/bin:$PATH"
```

## Usage

```sh
dtree          # running containers
dtree -a       # all containers, including stopped ones
dtree -f shop  # start filtered to containers or projects matching "shop"
dtree --version
```

The filter from `-f` / `--filter` works like typing it after `/`: press `esc`
to clear it. This makes it easy to keep an alias for a filter you use often:

```sh
alias dshop='dtree -a -f shop'
```

dtree connects to the daemon in `DOCKER_HOST` or, if that is unset, the
current `docker context` (Docker Desktop, OrbStack, Colima, …).

## Keys

Press `?` inside dtree for this list.

### Tree

| Key | Action |
| --- | --- |
| `j` / `↓`, `k` / `↑` | next / previous row |
| `l` / `→` | expand project |
| `h` / `←` | collapse project / go to parent |
| `enter` | toggle project · shell on container |
| `space` | toggle project · logs on container |
| `g` / `G` | top / bottom |
| `/` or `f` | filter by container or project name |
| `esc` | clear filter |
| `L` | logs |
| `i` | inspect |
| `s` | shell (bash, falls back to sh) |
| `x` | run a command (output in a popup; `↑`/`↓` for history) |
| `X` | show the last command's output |
| `a` | toggle running / all containers |
| `r` | refresh |
| `q` / `ctrl+c` | quit |

### Logs, inspect & command output

| Key | Action |
| --- | --- |
| `j` / `k`, `↓` / `↑` | scroll |
| `ctrl+d` / `ctrl+u` | half page down / up |
| `g` / `G` | top / bottom (`G` resumes follow) |
| `/`, `n`, `N` | search, next, previous |
| `f` | toggle follow (logs, output) |
| `t` | toggle timestamps (logs) |
| `r` | refresh (inspect) |
| `w` | toggle line wrap |
| `q` / `esc` | back · close popup |
| `ctrl+c` | stop the running command (popup); again to kill it |
| `i` | type into the running command (popup); `esc` goes back to scrolling |

## Development

```sh
go run .        # run from source
go test ./...
```

## License

[MIT](LICENSE)
