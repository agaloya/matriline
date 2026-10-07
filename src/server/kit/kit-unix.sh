#!/bin/sh
# Matriline helper kit for Linux and macOS, all in this one file: it sets this computer up
# as a Matriline helper and leaves it running as a service. Install ORCA first (INSTALL.md,
# section 2), then run, where this file is:
#
#     sh @@NAME@@-setup.sh
#
# It holds a one-time credential (used up on the first connection) and the client program
# for Linux (x86-64, arm64, riscv64) and macOS (Apple Silicon, Intel), after the last line
# of the script. Made by "matriline-server kit" (INSTALL.md 3.9).

# ======================= settings the admin may edit =======================
KIT_LANGUAGE='@@LANGUAGE@@'       # en, es, fr, pt or ar; empty = this computer's
WORKDIR="$HOME/matriline/cli"     # the client's working folder (settings, key, calculations)
BINDIR="$HOME/.local/bin"         # where the program goes
# Written into client.conf (same format; every option is explained in that file).
SETTINGS='
@@SETTINGS@@
'
# ===========================================================================

CRED='@@CRED@@'
set -eu
say() { printf '%s\n' "$*"; }
fail() { say "ERROR: $*"; exit 1; }
# base64 decodes with -d, except older macOS (-D)
b64d() { if printf 'YQ==' | base64 -d >/dev/null 2>&1; then base64 -d; else base64 -D; fi; }
user=${USER:-$(id -un)}
self=$0
sys=$(uname -s | tr 'A-Z' 'a-z')
arch=$(uname -m)
[ "$arch" = aarch64 ] && arch=arm64
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
printf '%s' "$CRED" | b64d >"$tmp/helper.cred" || fail "cannot decode the credential"
sed '1,/^__MATRILINE_PAYLOAD__$/d' "$self" | b64d | tar xzf - -C "$tmp" || fail "cannot unpack the program (is this file complete?)"

# run again after an interruption (a closed terminal or SSH session): it carries on, but
# only with a client of this kit's server; another server's client is left alone
again=""
if [ -e "$WORKDIR/client.conf" ]; then
	kitserver=$(grep '^server_id' "$tmp/helper.cred" || true)
	if [ ! -e "$WORKDIR/credential.conf" ]; then # interrupted inside init
		cp "$tmp/helper.cred" "$WORKDIR/credential.conf"
		chmod 600 "$WORKDIR/credential.conf"
	elif [ "$(grep '^server_id' "$WORKDIR/credential.conf" || true)" != "$kitserver" ]; then
		fail "$WORKDIR already holds a client of another server: set WORKDIR at the top of this file to another folder"
	fi
	again=yes
fi
bin="$tmp/matriline-client-$sys-$arch"
[ -f "$bin" ] || fail "this kit has no program for $sys-$arch (ask the admin)"

# the program in a fixed place (~/.local/bin), runnable; on macOS without the download mark
mkdir -p "$BINDIR"
client="$BINDIR/matriline-client"
cp "$bin" "$client.new" # then renamed: copying over a running program fails ("text file busy")
chmod 755 "$client.new"
mv -f "$client.new" "$client"
[ "$sys" = darwin ] && xattr -d com.apple.quarantine "$client" 2>/dev/null || true

if [ -n "$again" ]; then
	say "== $WORKDIR is already set up: carrying on with the service and the check"
else
	printf '%s\n' "$SETTINGS" >"$tmp/settings.conf"
	lang=""
	[ -n "$KIT_LANGUAGE" ] && lang="--language $KIT_LANGUAGE"
	say "== setting up $WORKDIR"
	# shellcheck disable=SC2086
	"$client" init "$WORKDIR" --credential "$tmp/helper.cred" --settings "$tmp/settings.conf" $lang
fi
cd "$WORKDIR"
# the service first: if the terminal or the SSH session closes during the (slow) check
# below, the client already runs and comes back by itself
if [ -n "${MATRILINE_KIT_NO_SERVICE:-}" ]; then # lab tests: everything but the service
	say "(MATRILINE_KIT_NO_SERVICE set: no service installed)"
else
	say "== installing the service"
	"$client" service install || fail "the service could not be installed (see above); try again: cd $WORKDIR && $client service install"
	if [ "$sys" = linux ]; then
		loginctl enable-linger "$user" 2>/dev/null && say "it keeps running while you are logged out" ||
			say "note: run 'loginctl enable-linger $user' (maybe with sudo) so it keeps running while you are logged out"
	fi
fi
say "== checking ORCA, the sandbox and the server (the first time a minute or two per ORCA installation)"
"$client" doctor || say "WARNING: fix what doctor says above (ORCA: INSTALL.md section 2); the client retries by itself, or restart it: $client service install"
if [ -z "${MATRILINE_KIT_NO_SERVICE:-}" ]; then
	# doctor only sees that the server answers; the service must also be let in (its key)
	say "== waiting for the service to connect to the server (up to 5 minutes)"
	conn="$WORKDIR/state/connection.json"
	i=0
	until grep -q '"connected":true' "$conn" 2>/dev/null; do
		i=$((i + 1))
		[ "$i" -gt 100 ] && break
		sleep 3
	done
	if [ "$i" -gt 100 ]; then
		err=$(sed -n 's/.*"last_error":"\([^"]*\)".*/\1/p' "$conn" 2>/dev/null)
		say "ERROR: the service has not connected to the server${err:+: $err}"
		say "If it says 'unknown client' or 'credential', ask the admin for a new kit; otherwise see"
		say "$WORKDIR/state/client.log, fix it and run this file again."
		exit 1
	fi
	say "connected"
fi
case ":$PATH:" in
*":$BINDIR:"*) ;;
*)
	rc="$HOME/.profile"
	case "${SHELL:-}" in */zsh) rc="$HOME/.zshrc" ;; */bash) rc="$HOME/.bashrc" ;; esac
	say ""
	say "note: $BINDIR is not in your PATH, so type the full path ($client), or add it once:"
	say "  echo 'export PATH=\"$BINDIR:\$PATH\"' >> $rc     (then open a new terminal)"
	;;
esac
say ""
say "Done: this computer is a Matriline helper. It starts by itself and works in the background."
say "While it computes on mains power, it keeps this computer from going to sleep by itself"
say "(the screen may still turn off); keep_awake = false in client.conf turns that off."
say "  status:  cd $WORKDIR && $client status"
say "  pause:   $client pause 2h    (or: pause --now, resume)"
say "  stop:    $client service remove"
say "Its settings are in $WORKDIR/client.conf; its log in $WORKDIR/state/client.log."
say "You may delete this file now."
exit 0
__MATRILINE_PAYLOAD__
