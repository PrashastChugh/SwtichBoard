# Switchboard (Go)

One place for every Claude Code session you have open. When a session needs permission or asks a question, a card pops up on screen with the buttons to answer it, so you never have to hunt through terminals.

- **Floating widget**: one lamp per session (🟢 working, 🟠 needs you). Always on top, every desktop, over full-screen apps. Drag it anywhere.
- **Permission cards**: the project, the exact command or change, and **Allow**, **Deny**, **Open in terminal**.
- **Question cards**: Claude's multiple-choice questions with each option as a button.
- **Web dashboard**: everything above plus context usage, rules, an activity log and a **Compact** button per session.
- **Rules**: "always allow `npm test*` in this project", with protection against chained commands.
- One small program, no dependencies. Hooks run in about 3 ms.

## Build and install (macOS)

You need Go and Apple's command line tools (you already have the tools if `python3` works).

```bash
brew install go          # once
cd ~/switchboard-go
./build.sh               # builds ./switchboard
./switchboard install    # adds hooks to ~/.claude/settings.json (backs it up first)
./switchboard start      # widget, cards and ☎︎ menu bar icon appear
```

`install` copies the program to `~/.switchboard/bin/switchboard`, so the hooks keep working even if you move or delete this folder. It also removes the hooks of the older Python version automatically.

Start at login: `./switchboard autostart on` (turn off with `autostart off`).

On Linux or Windows, `./build.sh` (or `go build`) works the same; `start` runs the server and opens the web dashboard.

## Commands

| Command | What it does |
| --- | --- |
| `switchboard start` | Start (Mac: native widget and cards; elsewhere: dashboard) |
| `switchboard start --dashboard` | Also open the web dashboard |
| `switchboard start --headless` | Server and dashboard only |
| `switchboard install [--minimal] [--shell-form]` | Add hooks (safe to re-run) |
| `switchboard status` | Hooks installed? Running? |
| `switchboard autostart on` / `off` | Start at login (macOS) |
| `switchboard demo` | Send example sessions to a running Switchboard |
| `switchboard uninstall` | Remove Switchboard's hooks only |

The dashboard is at http://127.0.0.1:47821 while Switchboard runs (or click the widget, or right-click it).

## Compacting from the dashboard

Each session has a **Compact** button, enabled when it's your turn in that session. It types `/compact` (plus anything you ask it to keep) into that session's terminal. Typing into a terminal works with **iTerm2, Apple Terminal, tmux, WezTerm and JetBrains IDEs (IntelliJ, WebStorm, PyCharm, GoLand, etc.)**; for other terminals the button is disabled and you can type `/compact` yourself.

The Context column shows how full each session is, highlighted from 70%. Compact at a natural break (a task finished, a new one about to start), not in the middle of debugging, since the summary can drop small details.

The first time Switchboard types into or switches iTerm2/Terminal tabs, macOS asks whether it may control that app. Allow it. JetBrains IDEs have no scripting dictionary for their terminal tabs, so Compact there activates the IDE and types the keystrokes itself — the first use prompts for **Accessibility** permission, scoped to Switchboard specifically (System Settings → Privacy & Security → Accessibility → allow **switchboard**), separate from the Automation prompt and from any other app or script on your Mac. Since it can't target a specific tab, keep to one Claude Code session per JetBrains window.

## Settings

`~/.switchboard/config.json` (run `install` again after changing `port` or `permission_wait`):

| Key | Default | Meaning |
| --- | --- | --- |
| `port` | `47821` | Local port |
| `permission_wait` | `540` | Seconds to hold a request while the dashboard is open |
| `quick_hold` | `60` | Seconds to hold a request with only the popups, before handing it to the terminal |
| `popups` | `true` | Show Switchboard's own cards (macOS) |
| `widget` | `true` | Show the floating widget (macOS) |
| `skip_when_looking` | `true` | Stay quiet when that session's terminal is already in front (macOS) |
| `os_notifications` | `true` | System notifications when the popups aren't running |
| `notify_on_finish` | `true` | Short card when a session finishes |
| `passthrough_when_closed` | `true` | With nothing to answer from, don't hold requests |
| `context_window` | `200000` | Used for the "% full" figure; set to `1000000` for 1M-context models |

## Safety

- If Switchboard isn't running, the hook exits silently and Claude Code behaves normally.
- Unanswered requests go back to Claude Code's own terminal prompt.
- The server listens on 127.0.0.1 only, checks the Host header, and needs the secret token from `config.json`.
- Claude Code's own deny rules still win over anything Switchboard allows.

## Code map

| File | Role |
| --- | --- |
| `main.go` | Commands |
| `hook.go` | What Claude Code runs on each event |
| `server.go` | Local server: hooks, dashboard API, compact |
| `state.go` | Sessions, queue, activity log, live updates |
| `ui_state.go` | What the native interface shows; its button actions |
| `ui_darwin.go`, `ui_export_darwin.go`, `ui_darwin_impl.h` | Native macOS interface (Objective-C via cgo) |
| `terminal*.go` | Focusing and typing into terminals; notifications |
| `rules.go`, `tokens.go`, `installer.go`, `config.go` | Rules, token counting, install, settings |
| `web/index.html` | Dashboard (built into the program) |
