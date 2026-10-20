package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Jeecabs/interragate/internal/demo"
)

// commandDocs describes each command for shell completion, in the order
// the usage lists them.
var commandDocs = []struct{ name, about string }{
	{"plan", "run plan here and explain it"},
	{"show", "explain a saved plan"},
	{"check", "findings only; exits 2 when any reach --fail-on"},
	{"report", "write the interactive HTML report"},
	{"explain", "everything about one resource"},
	{"watch", "live report that plans again on every save"},
	{"diff", "what changed between two plans"},
	{"apply", "plan, explain, and apply that plan once confirmed"},
	{"review", "go through the changes that need a look"},
	{"all", "plan every root below a directory, worst first"},
	{"open", "open the last plan's report without planning again"},
	{"demo", "explain a sample plan, no Terraform needed"},
	{"doctor", "check that interragate can plan here"},
	{"init", "write a starter .interragate.yaml"},
	{"rules", "list the rules"},
	{"completion", "print a shell completion script"},
	{"version", "print the version"},
}

// flagDocs describes each flag for shell completion. value names what the
// flag takes, empty for a switch; words are its fixed choices, if any.
var flagDocs = []struct {
	short, long, value, about string
	words                     []string
}{
	{"-C", "--dir", "dir", "Terraform working directory", nil},
	{"", "--bin", "binary", "terraform or tofu binary", []string{"terraform", "tofu"}},
	{"-f", "--format", "format", "output format", []string{"text", "markdown", "json", "html", "github", "sarif", "mermaid"}},
	{"-o", "--out", "file", "write the output to a file", nil},
	{"", "--open", "", "also open the HTML report", nil},
	{"", "--all", "", "include cosmetic and unchanged resources", nil},
	{"", "--diff", "", "show attribute diffs under each change", nil},
	{"", "--fail-on", "severity", "lowest severity that fails check", []string{"critical", "high", "medium", "low", "none"}},
	{"", "--markdown", "file", "also write the Markdown summary", nil},
	{"", "--json", "file", "also write the JSON report", nil},
	{"", "--html", "file", "also write the HTML report", nil},
	{"", "--config", "file", ".interragate.yaml to use", nil},
	{"", "--save-plan", "file", "keep the binary plan", nil},
	{"", "--base", "ref", "git ref to find your edits since", []string{"HEAD", "none"}},
	{"", "--scan", "scanners", "also run configuration scanners", []string{"trivy", "checkov", "tflint", "none"}},
	{"", "--no-source", "", "do not read .tf files", nil},
	{"", "--no-init", "", "never run init", nil},
	{"", "--no-open", "", "do not open the report", nil},
	{"", "--no-notify", "", "no desktop notifications from watch", nil},
	{"", "--auto-approve", "", "apply without asking", nil},
	{"", "--no-color", "", "plain text", nil},
	{"-v", "--verbose", "", "show terraform's output while planning", nil},
}

func completionCmd(o *options, stdout io.Writer) error {
	if len(o.args) != 1 {
		return fmt.Errorf("interragate completion needs a shell: bash, zsh or fish")
	}
	switch o.args[0] {
	case "bash":
		_, err := io.WriteString(stdout, bashCompletion())
		return err
	case "zsh":
		_, err := io.WriteString(stdout, zshCompletion())
		return err
	case "fish":
		_, err := io.WriteString(stdout, fishCompletion())
		return err
	}
	return fmt.Errorf("no completion for %s; interragate completes bash, zsh and fish", o.args[0])
}

func commandNames() string {
	var out []string
	for _, c := range commandDocs {
		out = append(out, c.name)
	}
	return strings.Join(out, " ")
}

func sampleList() string {
	var out []string
	for _, s := range demo.Samples {
		out = append(out, s.Name)
	}
	return strings.Join(out, " ")
}

func bashCompletion() string {
	var all, valued, dirs, files []string
	var choices strings.Builder
	for _, f := range flagDocs {
		names := []string{f.long}
		if f.short != "" {
			names = append([]string{f.short}, names...)
		}
		all = append(all, names...)
		if f.value == "" {
			continue
		}
		valued = append(valued, names...)
		switch {
		case f.words != nil:
			fmt.Fprintf(&choices, "    %s) COMPREPLY=($(compgen -W \"%s\" -- \"$cur\")); return ;;\n", strings.Join(names, "|"), strings.Join(f.words, " "))
		case f.value == "dir":
			dirs = append(dirs, names...)
		default:
			files = append(files, names...)
		}
	}
	return strings.NewReplacer(
		"{{choices}}", choices.String(),
		"{{dirs}}", strings.Join(dirs, "|"),
		"{{files}}", strings.Join(files, "|"),
		"{{flags}}", strings.Join(all, " "),
		"{{valued}}", strings.Join(valued, "|"),
		"{{commands}}", commandNames(),
		"{{samples}}", sampleList(),
	).Replace(`# bash completion for interragate. Load it in this shell with
#   source <(interragate completion bash)
# or in every shell by adding that line to ~/.bashrc.

# _interragate_unquote sets REPLY to a word as the shell will pass it, with
# quotes and backslashes resolved and a leading ~/ expanded.
_interragate_unquote() {
  local w=$1 out="" c q="" i
  for ((i = 0; i < ${#w}; i++)); do
    c=${w:i:1}
    if [[ $q == "'" ]]; then
      if [[ $c == "'" ]]; then q=""; else out+=$c; fi
    elif [[ $c == "\\" ]]; then
      ((i++))
      c=${w:i:1}
      # Within double quotes, a backslash escapes only these.
      if [[ $q == '"' && $c != '"' && $c != "\\" && $c != '$' ]]; then out+="\\"; fi
      out+=$c
    elif [[ $q == '"' ]]; then
      if [[ $c == '"' ]]; then q=""; else out+=$c; fi
    elif [[ $c == "'" || $c == '"' ]]; then
      q=$c
    else
      out+=$c
    fi
  done
  if [[ ${w:0:2} == "~/" ]]; then out=$HOME/${out:2}; fi
  REPLY=$out
}

_interragate() {
  local cur=${COMP_WORDS[COMP_CWORD]} prev=${COMP_WORDS[COMP_CWORD-1]}
  # bash splits --flag=value into --flag, = and value.
  if [[ $cur == "=" ]]; then
    cur=""
  elif [[ $prev == "=" ]] && ((COMP_CWORD > 1)); then
    prev=${COMP_WORDS[COMP_CWORD-2]}
  fi
  case $prev in
{{choices}}    {{dirs}})
      # bash's own directory completion keeps names with spaces whole.
      compopt -o dirnames +o default 2>/dev/null
      return ;;
    {{files}}) return ;;
  esac
  if [[ $cur == -* ]]; then
    COMPREPLY=($(compgen -W "{{flags}}" -- "$cur"))
    return
  fi
  local cmd="" dir="" i w
  local -a pos=()
  for ((i = 1; i < COMP_CWORD; i++)); do
    w=${COMP_WORDS[i]}
    case $w in
      --) return ;;
      {{valued}})
        # Skip the value, given after a space or an =.
        if [[ ${COMP_WORDS[i+1]} == "=" ]]; then ((i++)); fi
        ((i++))
        if [[ $w == -C || $w == --dir ]]; then dir=${COMP_WORDS[i]}; fi ;;
      -*) ;;
      =) ((i++)) ;;
      *) if [[ -z $cmd ]]; then cmd=$w; else pos+=("$w"); fi ;;
    esac
  done
  case $cmd in
    "") COMPREPLY=($(compgen -W "{{commands}}" -- "$cur")) ;;
    explain)
      # The plan comes first, then a resource address from it.
      if ((${#pos[@]} == 1)); then
        local plan want a quote=1
        _interragate_unquote "${pos[0]}"; plan=$REPLY
        _interragate_unquote "$cur"; want=$REPLY
        if [[ -n $dir ]]; then
          _interragate_unquote "$dir"
          set -- -C "$REPLY" "$plan"
        else
          set -- "$plan"
        fi
        # Addresses hold [ and ", which need quoting. bash quotes them as
        # it quotes file names, to suit the word being completed: escaped,
        # or inside the quotes it opened. Without compopt (bash 3), they
        # are escaped here.
        compopt -o filenames +o default 2>/dev/null && quote=0
        COMPREPLY=()
        while IFS= read -r a; do
          [[ -n $a && $a == "$want"* ]] || continue
          if ((quote)); then COMPREPLY+=("$(printf '%q' "$a")"); else COMPREPLY+=("$a"); fi
        done < <(interragate __addresses "$@" 2>/dev/null)
      fi ;;
    demo) ((${#pos[@]} == 0)) && COMPREPLY=($(compgen -W "{{samples}}" -- "$cur")) ;;
    completion) ((${#pos[@]} == 0)) && COMPREPLY=($(compgen -W "bash zsh fish" -- "$cur")) ;;
  esac
}
complete -o default -F _interragate interragate
`)
}

func zshCompletion() string {
	var cmds, flags strings.Builder
	for _, c := range commandDocs {
		fmt.Fprintf(&cmds, "    '%s:%s'\n", c.name, strings.ReplaceAll(c.about, "'", `'\''`))
	}
	for _, f := range flagDocs {
		about := strings.ReplaceAll(f.about, "'", `'\''`)
		// --format= takes its value after an = or as the next word.
		long := f.long
		if f.value != "" {
			long += "="
		}
		names := "'" + long
		if f.short != "" {
			names = fmt.Sprintf("'(%s %s)'{%s,%s}'", f.short, f.long, f.short, long)
		}
		action := ""
		switch {
		case f.words != nil:
			action = fmt.Sprintf(":%s:(%s)", f.value, strings.Join(f.words, " "))
		case f.value == "dir":
			action = ":directory:_files -/"
		case f.value != "":
			action = ":" + f.value + ":_files"
		}
		fmt.Fprintf(&flags, "    %s[%s]%s'\n", names, about, action)
	}
	return fmt.Sprintf(`#compdef interragate
# zsh completion for interragate. Load it in this shell with
#   source <(interragate completion zsh)
# or save it as _interragate in a directory on $fpath.
_interragate() {
  local -a commands flags
  commands=(
%s  )
  flags=(
%s  )
  local state line
  _arguments -s $flags '1: :->command' '*:: :->args'
  case $state in
    command) _describe -t commands 'interragate command' commands ;;
    args)
      case $line[1] in
        show|check|report|review) _arguments -s $flags '1:plan:_files' ;;
        diff) _arguments -s $flags '1:before:_files' '2:after:_files' ;;
        explain)
          # The plan comes first, then a resource address from it; flags
          # and their values can come before either.
          state=
          _arguments -s $flags '1:plan:_files' '2:address:->address'
          if [[ $state == address ]]; then
            local plan=${(Q)line[1]}
            [[ $plan == '~/'* ]] && plan=$HOME/${plan#'~/'}
            local -a addresses
            addresses=(${(f)"$(interragate __addresses $plan 2>/dev/null)"})
            compadd -a addresses
          fi ;;
        demo) _values 'sample' %s ;;
        completion) _values 'shell' bash zsh fish ;;
        *) _arguments -s $flags ;;
      esac ;;
  esac
}
if [[ $zsh_eval_context[-1] == loadautofunc ]]; then
  _interragate "$@"
else
  compdef _interragate interragate
fi
`, cmds.String(), flags.String(), sampleList())
}

func fishCompletion() string {
	var b strings.Builder
	b.WriteString(`# fish completion for interragate. Load it in this shell with
#   interragate completion fish | source
# or save it as ~/.config/fish/completions/interragate.fish.
complete -c interragate -f
`)
	fmt.Fprintf(&b, "set -l interragate_commands %s\n", commandNames())
	for _, c := range commandDocs {
		fmt.Fprintf(&b, "complete -c interragate -n \"not __fish_seen_subcommand_from $interragate_commands\" -a %s -d '%s'\n", c.name, strings.ReplaceAll(c.about, "'", `\'`))
	}
	for _, f := range flagDocs {
		var parts []string
		if f.short != "" {
			parts = append(parts, "-s "+strings.TrimPrefix(f.short, "-"))
		}
		parts = append(parts, "-l "+strings.TrimPrefix(f.long, "--"))
		switch {
		case f.words != nil:
			parts = append(parts, fmt.Sprintf("-x -a '%s'", strings.Join(f.words, " ")))
		case f.value == "dir":
			parts = append(parts, "-x -a '(__fish_complete_directories)'")
		case f.value != "":
			parts = append(parts, "-r -F")
		}
		fmt.Fprintf(&b, "complete -c interragate %s -d '%s'\n", strings.Join(parts, " "), strings.ReplaceAll(f.about, "'", `\'`))
	}
	var valued []string
	for _, f := range flagDocs {
		if f.value != "" {
			if f.short != "" {
				valued = append(valued, f.short)
			}
			valued = append(valued, f.long)
		}
	}
	b.WriteString(strings.NewReplacer("{{samples}}", sampleList(), "{{valued}}", strings.Join(valued, " ")).Replace(`complete -c interragate -n "__fish_seen_subcommand_from show check report diff review" -F
complete -c interragate -n "__fish_seen_subcommand_from explain" -a '(__interragate_explain)'
complete -c interragate -n "__fish_seen_subcommand_from demo" -x -a '{{samples}}'
complete -c interragate -n "__fish_seen_subcommand_from completion" -x -a 'bash zsh fish'

# explain takes a plan, then a resource address from it. Flags and their
# values can come anywhere; -C also says where a binary plan's
# configuration is.
function __interragate_explain
    set -l valued {{valued}}
    set -l cmd
    set -l pos
    set -l dir
    set -l skip
    for a in (commandline -opc)[2..-1]
        if set -q skip[1]
            test "$skip" = dir; and set dir $a
            set -e skip
            continue
        end
        switch $a
            case '-C' '--dir'
                set skip dir
            case '-*'
                contains -- $a $valued; and set skip value
            case '*'
                if test -z "$cmd"
                    set cmd $a
                else
                    set -a pos $a
                end
        end
    end
    if test (count $pos) -eq 0
        __fish_complete_path (commandline -ct)
    else if test (count $pos) -eq 1
        set -l plan (string replace -r '^~/' "$HOME/" -- $pos[1])
        if set -q dir[1]
            interragate __addresses -C $dir $plan 2>/dev/null
        else
            interragate __addresses $plan 2>/dev/null
        end
    end
end
`))
	return b.String()
}

// addressesCmd prints every resource and instance address a plan changes,
// for completing interragate explain. It stays quiet on errors, because a
// shell is listening, not a person.
func addressesCmd(ctx context.Context, o *options, stdout io.Writer) error {
	if len(o.args) != 1 {
		return nil
	}
	o.noSource = true
	r, err := loadReport(ctx, o, o.args[0])
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, ch := range r.Changes {
		for _, a := range []string{ch.Resource, ch.Address} {
			if a != "" && !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		}
	}
	sort.Strings(out)
	for _, a := range out {
		if _, err := fmt.Fprintln(stdout, a); err != nil {
			return err
		}
	}
	return nil
}
