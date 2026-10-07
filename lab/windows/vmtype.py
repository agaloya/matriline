#!/usr/bin/env python3
"""vmtype.py <monitor-socket> <text> - type text into a VM through QEMU's monitor (sendkey),
for when the VM has no SSH yet (e.g. OpenSSH failed to install on Windows 10). Special
words in the text: {enter} {win} {tab} {esc} {winr} (Win+R) {ctrlshiftenter} (run elevated
from the Run box). Stdlib only."""
import socket
import sys
import time

KEYS = {' ': 'spc', '\n': 'ret', '-': 'minus', '=': 'equal', '[': 'bracket_left', ']': 'bracket_right',
        ';': 'semicolon', "'": 'apostrophe', ',': 'comma', '.': 'dot', '/': 'slash', '\\': 'backslash',
        '`': 'grave_accent'}
SHIFTED = {'!': '1', '@': '2', '#': '3', '$': '4', '%': '5', '^': '6', '&': '7', '*': '8', '(': '9', ')': '0',
           '_': 'minus', '+': 'equal', '{': 'bracket_left', '}': 'bracket_right', ':': 'semicolon',
           '"': 'apostrophe', '<': 'comma', '>': 'dot', '?': 'slash', '|': 'backslash', '~': 'grave_accent'}
SPECIAL = {'{enter}': 'ret', '{win}': 'meta_l', '{tab}': 'tab', '{esc}': 'esc', '{winr}': 'meta_l-r',
           '{ctrlshiftenter}': 'ctrl-shift-ret'}


def key_of(c):
    if c.isalpha():
        return ('shift-' + c.lower()) if c.isupper() else c
    if c.isdigit():
        return c
    if c in KEYS:
        return KEYS[c]
    if c in SHIFTED:
        return 'shift-' + SHIFTED[c]
    raise SystemExit('cannot type %r' % c)


def main():
    mon, text = sys.argv[1], sys.argv[2]
    s = socket.socket(socket.AF_UNIX)
    s.connect(mon)
    s.recv(4096)
    i = 0
    while i < len(text):
        for word, key in SPECIAL.items():
            if text.startswith(word, i):
                k, i = key, i + len(word)
                break
        else:
            k, i = key_of(text[i]), i + 1
        s.sendall(('sendkey %s\n' % k).encode())
        time.sleep(0.04)
        try:
            s.settimeout(0.01)
            s.recv(65536)
        except OSError:
            pass
    s.close()


if __name__ == '__main__':
    main()
