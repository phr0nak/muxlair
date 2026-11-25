# muxlair

[![CI](https://github.com/phr0nak/muxlair/actions/workflows/ci.yml/badge.svg)](https://github.com/phr0nak/muxlair/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go version](https://img.shields.io/github/go-mod/go-version/phr0nak/muxlair?filename=go.mod)](go.mod)
[![Latest tag](https://img.shields.io/github/v/tag/phr0nak/muxlair?sort=semver&style=flat)](https://github.com/phr0nak/muxlair/tags)

![muxlair listing coding-agent sessions grouped by folder, with a details pane](docs/screenshot.png)

A terminal dashboard for your local AI coding-agent sessions. `muxlair` lists your Claude Code, Codex, OpenCode, Pi and Antigravity sessions, grouped by the folder they ran in. From there you can jump to a running session, reopen an old one or rename it, with the keyboard or the mouse. It runs inside [`tmux`](https://github.com/tmux/tmux), [`cmux`](https://cmux.com) or [`rex`](https://www.superlogical.com/).

### Why "muxlair"?

The name joins two words:

- **mux**, from *multiplexer*, as in `tmux`: many terminal sessions managed through one interface.
- **lair**, a hidden base or den: a dedicated place where your AI agents live and work.

Together, a home base for your coding-agent sessions.

## Quick start

1. [Install](#install) `muxlair`.
2. Open a `tmux`, `cmux` or `rex` terminal, and run `muxlair` in it.
3. Move to a session with the arrow keys and press `Enter`: `muxlair` jumps to it if it's open, or reopens it if it's closed.
4. Press `?` for every key, and `s` for settings.

## Supported agents

| Agent | Command |
|---|---|
| [Claude Code](https://docs.anthropic.com/en/docs/claude-code) | `claude` |
| [Codex CLI](https://github.com/openai/codex) | `codex` |
| [OpenCode](https://opencode.ai) | `opencode` |
| [Pi](https://github.com/earendil-works/pi) | `pi` |
| [Antigravity CLI](https://antigravity.google) | `agy` |

Not every feature works the same for every agent. See [Agent support](#agent-support).

## Features

- **One list for every agent**, grouped by folder, newest first. Each row shows the session's status, time since its last activity, agent and name. Each agent has its own color.
- **Live status:**

  | Status | Meaning |
  |---|---|
  | `busy` | The agent is working. |
  | `idle` | Open and waiting for you. |
  | `exited` | Closed. It can be reopened. |
  | `dead` | Codex can no longer reopen it as its session file is gone. |
  | `in use?` | An unrecognized agent process may have this file open, preventing `muxlair` from opening or renaming it. |

- **Jump or reopen** with `Enter`, in the terminal `muxlair` runs in:

  | `muxlair` runs in | `Enter` on an open session | `Enter` on a closed session |
  |---|---|---|
  | `tmux` | Jumps to its pane | Opens it in a new window |
  | `cmux` | Jumps to its tab | Opens it in a new workspace |
  | `rex` | Jumps to its tab | Opens it in a new tab |

- **Rename** with `r`. The name changes in the agent too, and in the session's terminal title (its `tmux` window, `cmux` tab or `rex` tab, when it doesn't share it with other terminals). A rename made inside the agent shows up in `muxlair` right away. Some agents limit this; see [Agent support](#agent-support).
- **Git** column, between `AGENT` and `SESSION`: the branch the session ran on, as the agent recorded it, else the folder's branch now. It hides when the list is narrow.

  | `GIT` shows | Meaning |
  |---|---|
  | `main` | The session ran on that branch, in the repository's main checkout. |
  | `↳ feat/login` | It ran on that branch in a linked [git worktree](https://git-scm.com/docs/git-worktree): a separate folder made with `git worktree add`. The details pane shows the main checkout it belongs to. |
  | `detached@1a2b3c4` | No branch was checked out, only that commit (a rebase, or a checked-out tag or commit). `detached` alone when the agent didn't record the commit. |
  | `—` | The folder isn't in a git repository. |

  The details pane also shows the folder's branch now, when it differs from the session's.
- **Details pane** with the session's ID, branch, folder, file, start time, last activity, model, tokens and cost, as far as the agent records them.
- **Conversation peek**: your last prompt and the agent's answer, in the details pane and in full with `p`. See [Conversation peek](#conversation-peek).
- **Mouse**: scroll, click, double-click to open, drag the divider to resize the details pane, and drag across details text to copy it.
- **Settings** in the app (`s`) or in `~/.muxlair/config.json`, applied immediately.

## Requirements

- [Go](https://go.dev/dl/) 1.27.1 or newer, to build `muxlair`.
- `tmux`, `cmux` or `rex`, to run `muxlair` in.
- The agents you use, installed and on your `PATH`.
- To copy text: `pbcopy`, `wl-copy`, `xclip` or `xsel`, or a terminal with clipboard access (OSC 52). Inside `tmux`, OSC 52 needs `set -g set-clipboard on`.

## Install

With Go:

```sh
go install github.com/phr0nak/muxlair/cmd/muxlair@latest
```

The binary goes to `$(go env GOBIN)` if set, otherwise `$(go env GOPATH)/bin` (usually `~/go/bin`). That folder must be on your `PATH`.

Or build from source:

```sh
git clone https://github.com/phr0nak/muxlair
cd muxlair
go build -o muxlair ./cmd/muxlair
mkdir -p ~/.local/bin && cp muxlair ~/.local/bin/
```

If `muxlair` isn't found afterwards, add `~/.local/bin` to your `$PATH` in your shell's startup file (such as `~/.zshrc` or `~/.bashrc`), then open a new terminal:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

## Keys

Press `?` in `muxlair` to see them at any time.

| Key | Action |
|---|---|
| `↑` `↓` or `j` `k` | Move |
| `Enter` or double-click | Jump to the session, or reopen it |
| `←` `→` (or `l`) | Collapse / expand a folder |
| `Space` or `Tab` | Toggle a folder |
| `h` | Hide / show exited sessions |
| `r` | Rename |
| `p` | Show the latest exchange in full |
| `/` | Filter. `Enter` keeps the filter, `Esc` clears it |
| `i` | Show / hide the details pane |
| `<` `>` | Resize the details pane |
| `R` | Refresh |
| `s` | Settings |
| `?` | All keys |
| `q` or `Ctrl+C` | Quit |

## Command line

| Command | What it does |
|---|---|
| `muxlair` | Starts the dashboard. |
| `muxlair list [agent]` | Prints the sessions as a table and exits: every agent's, or only one agent's (`claude`, `codex`, `opencode`, `pi` or `agy`). |
| `muxlair --version` (`-v`) | Prints the version. |
| `muxlair --help` (`-h`) | Prints the usage. |

Anything else exits with status 2 and an error message.

`muxlair list` prints one line per session, newest first:

| Column | Meaning |
|---|---|
| `STATUS` | `busy`, `idle` or `exited`. |
| `ID` | The agent's session ID. |
| `UPDATED` | Last activity, `YYYY-MM-DD HH:MM`. |
| `AGENT` | `claude`, `codex`, `opencode`, `pi` or `agy`. |
| `GIT` | The session's git branch, as in the dashboard's `GIT` column (`↳` for a linked worktree, `—` outside git). |
| `TTY` | The session's terminal, empty when it isn't running. |
| `MAYBELIVE` | `true` when the session shows `in use?`. |
| `CWD` | The folder the session ran in. |
| `NAME` | The session name. |

## Configuration

Press `s` to open Settings: move with `↑` `↓`, change a value with `Space` or `←` `→`, then choose **Save** (or press `Ctrl+S`). `Esc` closes without saving.

Settings are saved in `~/.muxlair/config.json`. You can also edit that file; `muxlair` applies it as soon as it's saved. A setting you leave out keeps its default. If the file has a mistake, `muxlair` keeps its current settings and shows the error in the bottom bar.

```json
{
  "mouse": true,
  "color_tabs": true,
  "details": true,
  "conversation_peek": true
}
```

| Setting | Default | What it does |
|---|---|---|
| `mouse` | `true` | Mouse support. |
| `color_tabs` | `true` | Tints the window (`tmux`) or workspace (`cmux`) that `muxlair` opens with the agent's color. `rex` tabs have no color. |
| `details` | `true` | Shows the details pane at startup. It needs a window at least 90 columns wide. |
| `conversation_peek` | `true` | Shows the latest exchange. When `false`, `muxlair` reads no conversation text at all. |

## Conversation peek

When there is room, the details pane shows the selected session's **Latest exchange**: your last prompt and the agent's answer, cut to fit. `p` shows both in full (up to 20,000 characters); scroll with `↑` `↓` or the wheel, and drag across text to copy it.

It is the real text from the agent's own files, never a summary. Control characters are removed before it is drawn.

| Agent | Read from | Left out |
|---|---|---|
| Claude Code | The session transcript | Tool calls and output, reasoning, subagents, slash commands |
| Codex | The session file | Tool calls and output, reasoning |
| OpenCode | OpenCode's database | Tool output, reasoning |
| Pi | The session file (the branch pi shows) | Tool calls, thinking |
| Antigravity | The conversation log | Tool steps |

An answer that stopped early (interrupted, or at a tool call) is marked as such, and a busy session shows "working…".

**Privacy:** the text is on your screen for anyone who can see it. Set `conversation_peek` to `false` to turn it off.

## What `muxlair` reads and changes

`muxlair` makes no network connections. It reads each agent's files and changes a session only when you rename it.

| Agent | Reads | Rename a closed session | Rename an open session |
|---|---|---|---|
| Claude Code | `~/.claude` | Appends the record `/rename` writes | Types `/rename <name>` |
| Codex | `~/.codex` or `$CODEX_HOME` | Codex's app server | Types `/rename <name>`, else the app server |
| OpenCode | `~/.local/share/opencode/opencode.db`, or under `$XDG_DATA_HOME` | OpenCode's server (`opencode api`) | OpenCode's server |
| Pi | `~/.pi/agent/sessions`, or where `pi`'s settings put them | Pi itself (`pi --mode rpc`; no model is called) | Not possible: use `/name` in `pi` |
| Antigravity | `~/.gemini/antigravity-cli` | Not possible: open it first | Types `/rename <name>` |

- **Writes:** `muxlair` writes to one agent's files only: it appends a rename record to a Claude Code transcript. Every other change is made by the agent itself.
- **Typing a command** into a session's terminal happens only when the session is `idle` and its input box is empty, so nothing you're typing gets sent.
- **Reopening** a session starts the agent itself in a new terminal. The agents are separate programs and do what they normally do.
- **Other commands** `muxlair` runs: `tmux`, `cmux` or `rex` to find, open and rename terminals, and `ps` and `lsof` to see which sessions are running.
- **Its own file** is `~/.muxlair/config.json`.

## Troubleshooting

- **`Enter` selects a `rex` tab but the `rex` app doesn't switch to it.** Turn on Remote Control in the `rex` app's Settings, under Rex Server.
- **A session shows `in use?`.** An agent process that `muxlair` can't match to a session runs in that folder, for example one in its session picker. Close it, or wait for it to finish.
- **Every Codex session shows `in use?`.** `muxlair` needs `lsof` to match Codex processes to sessions. Install `lsof`.
- **A Codex session shows `dead`.** Its session file is gone, so Codex can't reopen it.

## Known limitations

### Agent support

✓ supported · ◐ partly · ✗ not supported

| Feature | Claude Code | Codex | OpenCode | Pi | Antigravity |
|---|---|---|---|---|---|
| List sessions | ✓ | ✓ | ✓ | ✓ | ✓ |
| Reopen a closed session | ✓ | ✓ | ✓ | ◐ ¹ | ✓ |
| Jump to an open session | ✓ | ✓ | ◐ ² | ✗ ¹ | ✓ |
| Busy / idle status | ✓ | ✓ | ◐ ² | ✗ ¹ | ✓ |
| Rename a closed session | ✓ | ✓ | ✓ | ✓ | ✗ ³ |
| Rename an open session | ✓ | ✓ | ✓ | ✗ ¹ | ✓ |
| Model and tokens | ✓ | ◐ ⁴ | ✓ | ✓ | ✓ |
| Cost | ✓ | ✗ | ✓ | ✓ | ✗ |
| Conversation peek | ✓ | ✓ | ✓ | ✓ | ✓ |
| Git branch of the session | ✓ | ✓ | ◐ ⁵ | ◐ ⁵ | ◐ ⁵ |

1. A running `pi` doesn't say which session it has open, so every `pi` session in its folder shows `in use?` until it exits.
2. Jumping and `idle` need an `opencode` session started as `opencode -s <id>`, as `muxlair` starts it; `busy` comes from `opencode`'s database. A plain `opencode` isn't matched, so `Enter` opens a second view, which `opencode` keeps in sync.
3. `agy` can't rename a closed conversation from outside. `r` asks you to open it first.
4. `codex` records a token total, not the split by kind.
5. The agent doesn't record the branch, so `muxlair` shows the folder's branch now, which can differ from the one the session ran on.

### Other limitations

- **Platforms.** Developed and tested on macOS. Linux is partially tested only. Windows is not supported.
- **Old `agy` conversations without a local log** show no model, tokens or latest exchange.
- **`rex` tabs have no color**, so `color_tabs` only applies in `tmux` and `cmux`.
- **A Codex you start yourself can look open for a minute after you close it.** Codex's shared background server keeps a closed session loaded for 60 s (its `thread_unload_delay_secs` setting). Sessions `muxlair` reopens run without that server (`codex resume --no-daemon`), so they show `exited` right away.

## License

`muxlair` is released under the [MIT License](LICENSE).

`muxlair` is an independent project. It is not affiliated with, endorsed by or sponsored by Anthropic, OpenAI, Google, or the makers of OpenCode or Pi. Claude, Claude Code, Codex, OpenCode, Pi, Antigravity and other product names are trademarks of their respective owners.
