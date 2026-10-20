#!/usr/bin/env bash
# Build interragate from this checkout and put it on your PATH.
#
# Usage: scripts/install.sh [DIR]
#
# Without DIR it uses the first of ~/.local/bin, ~/bin and Go's own install
# directory that is already on PATH, and ~/.local/bin when none is. Go's
# directory comes last because version managers (mise, asdf) often keep it
# off PATH.
set -euo pipefail

# A relative DIR is relative to where you ran this, not to the checkout it
# builds in.
dir=${1:-}
if [[ -n $dir && $dir != /* ]]; then
  dir=$PWD/$dir
fi

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)

on_path() { case ":$PATH:" in *":$1:"*) return 0 ;; esac; return 1; }

# Any Go from 1.21 on builds interragate: it fetches the toolchain go.mod
# asks for. Without one, mise can supply the version mise.toml pins.
go=(go)
if ! command -v go >/dev/null 2>&1; then
  if command -v mise >/dev/null 2>&1; then
    go=(mise exec -- go)
  else
    echo "interragate is built with Go, and there is no go on your PATH." >&2
    echo "Install Go 1.21 or later (https://go.dev/dl/, or your package manager), then run this again." >&2
    exit 1
  fi
fi

if [[ -z $dir ]]; then
  gobin=$("${go[@]}" env GOBIN)
  gobin=${gobin:-$("${go[@]}" env GOPATH)/bin}
  for d in "$HOME/.local/bin" "$HOME/bin" "$gobin"; do
    if on_path "$d"; then
      dir=$d
      break
    fi
  done
  dir=${dir:-$HOME/.local/bin}
fi

mkdir -p "$dir"
"${go[@]}" build -ldflags "-s -w -X main.version=$version" -o "$dir/interragate" ./cmd/interragate
echo "Installed $dir/interragate ($version)."
if ! on_path "$dir"; then
  echo
  echo "$dir is not on your PATH yet. Add this line to your shell's startup file:"
  echo "  export PATH=\"$dir:\$PATH\""
fi
echo
case ${SHELL##*/} in
  bash) echo "Tab completion: add 'source <(interragate completion bash)' to ~/.bashrc." ;;
  zsh) echo "Tab completion: add 'source <(interragate completion zsh)' to ~/.zshrc." ;;
  fish) echo "Tab completion: interragate completion fish > ~/.config/fish/completions/interragate.fish" ;;
esac
echo "Try it: interragate demo"
