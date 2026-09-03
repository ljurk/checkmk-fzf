# checkmk-fzf

A Go CLI built with [Cobra](https://github.com/spf13/cobra) for querying Checkmk
service status and interactively browsing hosts with `fzf`. It is a Go
reimplementation of the adjacent Python project.

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

The interactive picker also requires
[`fzf`](https://github.com/junegunn/fzf) to be installed and available in
`PATH`.

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

Interactively browse hosts and preview their services:

```sh
./checkmk-fzf fzf
```

Press `Ctrl-O` to open the selected host in Checkmk without closing the picker.
Press Enter to open it and exit. Filter to critical services with `--crit`, or
to aliases from `config.yml` with `--my`:

```sh
./checkmk-fzf fzf --crit --my
./checkmk-fzf overview --my
```
