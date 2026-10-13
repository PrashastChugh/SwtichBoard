//go:build !darwin

package main

// On Linux and Windows, Switchboard can focus and type into tmux and WezTerm panes.

func userIsLooking(env map[string]string) bool { return false }

func showTerminal(env map[string]string) { go tmuxSelect(env) }

func canSendText(env map[string]string) bool { return hasTmux(env) || hasWezterm(env) }

func sendText(env map[string]string, text string) error {
	switch {
	case hasTmux(env):
		return tmuxSend(env, text)
	case hasWezterm(env):
		return weztermSend(env, text)
	}
	return errNoTyping
}
