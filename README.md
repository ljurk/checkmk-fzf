# checkmk-fzf

A Go CLI and [Bubble Tea](https://github.com/charmbracelet/bubbletea) terminal
UI for querying Checkmk service status and interactively browsing hosts.

## Install

Install the latest version directly from GitHub:

```sh
go install github.com/ljurk/checkmk-fzf@latest
```

Go installs the executable into `$(go env GOPATH)/bin`. Make sure that
directory is included in your `PATH`, then confirm the installation:

```sh
checkmk-fzf --help
```

## Build

Go 1.22 or newer is required.

```sh
go build -o checkmk-fzf .
```

## Configure

Create the application configuration directory and copy the example:

```sh
mkdir -p ~/.config/checkmk-fzf
cp config.example.yml ~/.config/checkmk-fzf/config.yml
```

This is the standard Linux location. On macOS the file is under
`~/Library/Application Support/checkmk-fzf/config.yml`; on Windows it is under
the user's application-data directory.

Edit `config.yml` with the Checkmk URL, site, automation username, and optional
host aliases used by `--my`. The automation secret does not belong in this
file. Save it in the desktop system keyring instead:

```sh
./checkmk-fzf auth login
./checkmk-fzf auth status
```

`auth login` reads the secret without displaying it. On Linux, keyring access
uses the Secret Service interface provided by desktop keyrings such as GNOME
Keyring. Use `./checkmk-fzf auth logout` to remove the credential.

## Use

Show hosts with their numbers of critical and warning services:

```sh
./checkmk-fzf overview
```

Show every service for one host, using its alias from `overview`:

```sh
./checkmk-fzf show HOST_ALIAS
```

With no alias, `show` displays all non-OK services:

```sh
./checkmk-fzf show
```

Launch the interactive terminal UI (also the default with no arguments):

```sh
./checkmk-fzf
./checkmk-fzf tui
```

Use the arrow keys or `j`/`k` to select a host. Press `/` to search, `c` to
toggle critical-only services, `m` to toggle aliases from `config.yml`, `r` to
refresh, and Tab to switch to the color-coded host overview. In the overview,
use all four arrow keys and press Enter to drill into a host. Press `o` to open
it in Checkmk or `q` to quit.

The initial filters can also be selected on the command line. `fzf` remains an
alias for `tui` so existing scripts continue to work:

```sh
./checkmk-fzf tui --crit --my
./checkmk-fzf overview --my
```

## Cache

The first status command fetches all services from Checkmk and stores them in
`services.json` under the operating system's user cache directory. Subsequent
commands load that file immediately and apply their filters locally. Once the
cache is 30 seconds old, the command still uses it immediately. The TUI
refreshes it asynchronously and updates the visible host and service lists;
noninteractive commands start a detached refresh for the next invocation.

The cache is replaced atomically, and concurrent commands share a refresh lock
to avoid sending duplicate requests. Removing `services.json` forces the next
command to wait for a fresh API response.
