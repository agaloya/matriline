#!/usr/bin/env bash
# build.sh - build the three programs into ~/.local/bin (or the directory given), from any
# working directory, on Linux or macOS:   ./build.sh   or   matriline/build.sh ~/bin
#   --nosecurity   build without the security functions (trusted machines only; INSTALL.md)
# Windows: build.ps1. Release binaries with published checksums: tools/release.sh.
set -euo pipefail
cd "$(dirname "$0")/src"
tags=() out=$HOME/.local/bin
for a in "$@"; do
	case $a in
	--nosecurity) tags=(-tags nosecurity) ;;
	-*) echo "usage: build.sh [--nosecurity] [directory]" >&2; exit 2 ;;
	*) out=$a ;;
	esac
done
mkdir -p "$out"
command -v go >/dev/null || { echo "Go is not installed: see INSTALL.md, section 1, step 1 (Go 1.27 or newer; https://go.dev/dl/)" >&2; exit 1; }
for p in server client relay; do
	go build ${tags[@]+"${tags[@]}"} -o "$out/matriline-$p" "./$p"
	echo "built $out/matriline-$p$([[ ${#tags[@]} -gt 0 ]] && echo ' (no-security build)')"
done
case ":$PATH:" in *":$out:"*) ;; *) echo "note: $out is not in your PATH; add it, or call the programs with their full path" ;; esac
