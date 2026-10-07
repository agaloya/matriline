#!/usr/bin/env bash
# release.sh - reproducible Matriline binaries: the same commit and target give the same
# bytes (and SHA-256) on any build host, so anyone can rebuild and compare instead of
# trusting whoever sent the binary.
#
#   tools/release.sh [out-dir]          build every target into out-dir (default dist/)
#   tools/release.sh --check SHA256SUMS rebuild here and compare with a published list
#
# What makes it reproducible:
#   - the official Go toolchain pinned below, downloaded by the go command itself
#     (GOTOOLCHAIN); distribution builds differ (e.g. CachyOS ships go1.27.1-X:nodwarf5);
#   - a clean src/ at a commit (refuses uncommitted changes in src/);
#   - CGO_ENABLED=0 (no host C toolchain), -trimpath (no build paths), empty build id,
#     -buildvcs=false (the VCS stamp records whether ANY file of the repo is modified, e.g.
#     docs); the commit is stamped explicitly instead (-X main.commit);
#   - GOAMD64/GOARM64 and GOEXPERIMENT/GOFLAGS fixed so the environment cannot change code.
# Different targets (OS/architecture) are different binaries by nature: compare per target.
# Publishing a release that servers and clients install by themselves (D66):
#   tools/release.sh dist && (cd src && go run ./relsign sign <release-key> ../dist)
#   gh release create v<version> dist/*
set -euo pipefail
GO_TOOLCHAIN=go1.27.1
TARGETS="linux/amd64 linux/arm64 linux/riscv64 darwin/arm64 darwin/amd64 windows/amd64"
PROGS="server client relay"

# portable to macOS (bash 3.2, BSD tools): no readlink -f/-m, no sha256sum there
sha256() { if command -v sha256sum >/dev/null; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }
abs() { (cd "$(dirname "$1")" && printf '%s/%s\n' "$(pwd -P)" "$(basename "$1")"); }
repo=$(cd "$(dirname "$0")/.." && pwd -P)
check=""
if [[ ${1:-} == --check ]]; then # resolve paths before changing directory
	[[ -f ${2:?usage: release.sh --check SHA256SUMS} ]] || { echo "no such file: $2" >&2; exit 1; }
	check=$(abs "$2")
	out=$(mktemp -d)
else
	mkdir -p "${1:-$repo/dist}"
	out=$(cd "${1:-$repo/dist}" && pwd -P)
fi
cd "$repo/src"
if [[ -n $(git status --porcelain -- .) ]]; then
	echo "src/ has uncommitted changes; commit or stash them first:" >&2
	git status --short -- . >&2
	exit 1
fi
# the last commit that changed src/: commits touching only docs do not change binaries
rev=$(git log -1 --format=%H -- . | cut -c1-12)
# a sums file named SHA256SUMS-<commit> belongs to that commit: comparing it with another
# one always "fails", which looked like a tampered binary (found in review)
if [[ -n $check && $(basename "$check") =~ ^SHA256SUMS-([0-9a-f]{12})$ && ${BASH_REMATCH[1]} != "$rev" ]]; then
	echo "$(basename "$check") is for commit ${BASH_REMATCH[1]}, but src/ is at $rev:" >&2
	echo "  git checkout ${BASH_REMATCH[1]} && tools/release.sh --check $check   (then git checkout -)" >&2
	exit 1
fi
export GOTOOLCHAIN=$GO_TOOLCHAIN CGO_ENABLED=0 GOFLAGS= GOEXPERIMENT= GOAMD64=v1 GOARM64=v8.0
echo "toolchain: $(go version)   commit: $rev"
# the version the programs report (src/*/: const agentVersion); one for all of them
ver=$(sed -n 's/^const agentVersion = "matriline-[a-z]*\/\(.*\)"$/\1/p' server/server.go client/agent.go relay/main.go | sort -u)
if [[ $(grep -c "" <<<"$ver") != 1 || -z $ver ]]; then # (BSD wc -l pads its number: seen on macOS)
	echo "server, client and relay report different versions: $(echo $ver)" >&2
	exit 1
fi
# the built-in 3Dmol.js must be the file recorded when it was added (code review)
want3d=$(sed -n 's/^sha256 of 3Dmol-min.js: //p' server/web3d/SOURCE.txt)
if [[ $(sha256 server/web3d/3Dmol-min.js | cut -d' ' -f1) != "$want3d" ]]; then
	echo "server/web3d/3Dmol-min.js differs from the hash in server/web3d/SOURCE.txt" >&2
	exit 1
fi
mkdir -p "$out"
: >"$out/SHA256SUMS"
printf 'version %s\ncommit %s\n' "$ver" "$rev" >"$out/VERSION" # read by relsign
for t in $TARGETS; do
	os=${t%/*} arch=${t#*/}
	for p in $PROGS; do
		[[ $os != linux && $p == relay ]] && continue # relay: Linux only
		[[ $os == darwin && $p == server ]] && continue # server: Linux and Windows
		name="matriline-$p-$os-$arch"
		[[ $os == windows ]] && name+=.exe
		GOOS=$os GOARCH=$arch go build -trimpath -buildvcs=false \
			-ldflags="-buildid= -X main.commit=$rev" -o "$out/$name" "./$p"
		(cd "$out" && sha256 "$name" >>SHA256SUMS)
	done
done
# the licenses go with the programs (3Dmol.js's BSD license asks for its notice in the
# distribution's documentation: docs/THIRD_PARTY.md)
cp "$repo/docs/THIRD_PARTY.md" "$out/"
for f in LICENSE NOTICE docs/NOTICE; do [[ -f $repo/$f ]] && cp "$repo/$f" "$out/$(basename "$f")"; done
if [[ -n $check ]]; then
	if diff <(sort -k2 "$check") <(sort -k2 "$out/SHA256SUMS"); then
		echo "OK: every binary rebuilt here matches $check (commit $rev)"
	else
		echo "MISMATCH (see the lines above): do not trust the published binaries" >&2
		exit 1
	fi
	rm -rf "$out"
else
	echo "built into $out (commit $rev):"
	cat "$out/SHA256SUMS"
fi
