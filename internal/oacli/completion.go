// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package oacli

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// completionTimeout bounds the whole server round trip of a dynamic completion:
// a slow or unreachable server must not freeze the shell.
const completionTimeout = 2 * time.Second

// completionPageSize and completionMaxPages bound how much of a list a
// completion fetches.
const (
	completionPageSize = 100
	completionMaxPages = 5
)

// A completer suggests values for one positional argument or one flag value.
// args are the positionals typed before it (a cleanup policy id needs the
// repository), toComplete what the user has typed of it so far.
type completer = cobra.CompletionFunc

// enum completes from a fixed list.
func enum(values ...string) completer {
	return func(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return filterPrefix(values, toComplete), cobra.ShellCompDirectiveNoFileComp
	}
}

// files completes file and directory names (the shell's own completion).
func files(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return nil, cobra.ShellCompDirectiveDefault
}

// nothing suggests nothing, and no file names either.
func nothing(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return nil, cobra.ShellCompDirectiveNoFileComp
}

// positional completes the positional arguments of a command in order: the nth
// completer serves the nth argument, later arguments get no suggestion.
func positional(comps ...completer) cobra.CompletionFunc {
	return positionalFrom(comps, false)
}

// positionalRepeat is positional where the last completer serves every
// argument from its position on (upload <repo> <file>...).
func positionalRepeat(comps ...completer) cobra.CompletionFunc {
	return positionalFrom(comps, true)
}

func positionalFrom(comps []completer, repeat bool) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		n := len(args)

		switch {
		case n < len(comps):
			return comps[n](cmd, args, toComplete)
		case repeat && len(comps) > 0:
			return comps[len(comps)-1](cmd, args, toComplete)
		}

		return nothing(cmd, args, toComplete)
	}
}

// flagValues registers a completer for a flag of cmd; a flag that does not
// exist is a programming error.
func flagValues(cmd *cobra.Command, name string, c completer) {
	mustRegister(
		cmd.RegisterFlagCompletionFunc(name, c),
	)
}

// flagEnum registers a fixed list of values for a flag of cmd.
func flagEnum(cmd *cobra.Command, name string, values ...string) {
	flagValues(cmd, name, enum(values...))
}

// flagFiles registers file completion for a flag of cmd.
func flagFiles(cmd *cobra.Command, name string) {
	flagValues(cmd, name, files)
}

func filterPrefix(values []string, prefix string) []string {
	var out []string

	for _, v := range values {
		if strings.HasPrefix(v, prefix) {
			out = append(out, v)
		}
	}

	return out
}

// listNames completes a name (the field of each item) from a list endpoint of
// the configured server, e.g. listNames("/api/v1/repositories", "repositories",
// "name"). The request is made with the user's saved credentials and a short
// timeout; any failure (no token, unreachable server, bad response) yields no
// suggestion, silently. It never prompts and never prints anything.
func (a *app) listNames(path, listKey, field string) completer {
	return a.listNamesAt(func([]string) string { return path }, listKey, field)
}

// listNamesAt is listNames where the endpoint depends on the positionals
// already typed (cleanup policies of the repository named first).
func (a *app) listNamesAt(path func(args []string) string, listKey, field string) completer {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return filterPrefix(a.fetchNames(cmd, path(args), listKey, field), toComplete), cobra.ShellCompDirectiveNoFileComp
	}
}

// repoNames completes repository names.
func (a *app) repoNames() completer {
	return a.listNames("/api/v1/repositories", "repositories", "name")
}

// completionClient builds the client of a completion, or nil without
// credentials. It resolves server and token like preRun but never fails.
func (a *app) completionClient(cmd *cobra.Command) *Client {
	cfg, err := loadConfig()
	if err != nil {
		return nil
	}

	server := a.resolve(cmd, "server", a.server, "OA_SERVER", cfg.Server)
	token := a.resolve(cmd, "token", a.token, "OA_TOKEN", cfg.Token)

	if token == "" {
		return nil
	}

	return newClient(server, token)
}

func (a *app) fetchNames(cmd *cobra.Command, path, listKey, field string) []string {
	client := a.completionClient(cmd)
	if client == nil {
		return nil
	}

	deadline := time.Now().Add(completionTimeout)
	sep := "?"

	if strings.Contains(path, "?") {
		sep = "&"
	}

	seen := map[string]bool{}

	var names []string

	for page := 1; page <= completionMaxPages; page++ {
		client.httpClient.Timeout = time.Until(deadline)
		if client.httpClient.Timeout <= 0 {
			break
		}

		items, ok := completionPage(client, path+sep+"limit="+strconv.Itoa(completionPageSize)+"&page="+strconv.Itoa(page), listKey)
		if !ok {
			return names
		}

		fresh := 0

		for _, it := range items {
			if s, isStr := it[field].(string); isStr && !seen[s] {
				seen[s] = true
				names = append(names, s)
				fresh++
			}
		}

		if len(items) < completionPageSize || fresh == 0 {
			break
		}
	}

	return names
}

// completionPage GETs one page of a list; ok is false on any failure.
func completionPage(client *Client, path, listKey string) ([]map[string]any, bool) {
	resp, err := client.do("GET", path, nil, "")
	if err != nil {
		return nil, false
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, false
	}

	items, err := listItems(body, listKey)

	return items, err == nil
}

// ─── oa completion ───────────────────────────────────────────────────────────

func newCompletionCmd() *cobra.Command {
	noDesc := false

	shell := func(name, short, long string, gen func(cmd *cobra.Command, w io.Writer, desc bool) error) *cobra.Command {
		c := &cobra.Command{
			Use:   name,
			Short: short,
			Long:  long,
			Args:  usageArgs(0, 0),
			RunE: func(cmd *cobra.Command, _ []string) error {
				return gen(cmd, cmd.OutOrStdout(), !noDesc)
			},
		}
		c.Flags().BoolVar(&noDesc, "no-descriptions", false, "Complete names without descriptions")

		return c
	}

	root := func(cmd *cobra.Command) *cobra.Command { return cmd.Root() }

	return parentCmd("completion", "Generate the shell completion script",
		shell("bash", "Generate the bash completion script",
			"Generate the bash completion script. It needs the bash-completion package.\n\n"+
				"Load it in the current shell:\n\n  source <(oa completion bash)\n\n"+
				"Install it for every session:\n\n"+
				"  oa completion bash > ~/.local/share/bash-completion/completions/oa   # one user\n"+
				"  oa completion bash | sudo tee /etc/bash_completion.d/oa              # all users (Linux)\n"+
				"  oa completion bash > \"$(brew --prefix)/etc/bash_completion.d/oa\"     # macOS with Homebrew\n\n"+
				"Start a new shell for it to take effect.",
			func(cmd *cobra.Command, w io.Writer, desc bool) error {
				return root(cmd).GenBashCompletionV2(w, desc)
			}),
		shell("zsh", "Generate the zsh completion script",
			"Generate the zsh completion script.\n\n"+
				"If completion is not enabled in your zsh yet, enable it once:\n\n"+
				"  echo \"autoload -U compinit; compinit\" >> ~/.zshrc\n\n"+
				"Load it in the current shell:\n\n  source <(oa completion zsh)\n\n"+
				"Install it for every session:\n\n"+
				"  oa completion zsh > \"${fpath[1]}/_oa\"                                  # Linux\n"+
				"  oa completion zsh > \"$(brew --prefix)/share/zsh/site-functions/_oa\"    # macOS with Homebrew\n\n"+
				"Start a new shell for it to take effect.",
			func(cmd *cobra.Command, w io.Writer, desc bool) error {
				if desc {
					return root(cmd).GenZshCompletion(w)
				}

				return root(cmd).GenZshCompletionNoDesc(w)
			}),
		shell("fish", "Generate the fish completion script",
			"Generate the fish completion script.\n\n"+
				"Load it in the current shell:\n\n  oa completion fish | source\n\n"+
				"Install it for every session:\n\n  oa completion fish > ~/.config/fish/completions/oa.fish\n\n"+
				"Start a new shell for it to take effect.",
			func(cmd *cobra.Command, w io.Writer, desc bool) error {
				return root(cmd).GenFishCompletion(w, desc)
			}),
		shell("powershell", "Generate the PowerShell completion script",
			"Generate the PowerShell completion script.\n\n"+
				"Load it in the current session:\n\n  oa completion powershell | Out-String | Invoke-Expression\n\n"+
				"Install it for every session by adding that line to your PowerShell profile ($PROFILE).",
			func(cmd *cobra.Command, w io.Writer, desc bool) error {
				if desc {
					return root(cmd).GenPowerShellCompletionWithDesc(w)
				}

				return root(cmd).GenPowerShellCompletion(w)
			}),
	)
}
