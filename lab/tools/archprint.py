#!/usr/bin/env python3
"""archprint.py ARCHIVE [OUT.json] - the Matriline fingerprint of an ORCA archive, read as a
stream: no unpacking (an unpacked ORCA 6.1.1 takes ~17 GB).

The same JSON as 'matriline-client fingerprint <unpacked folder>' (src/common/orca/
fingerprint.go): every regular file's SHA-256 by its path below the archive's top folder,
symbolic links as "link:<target>", the tree hash over sorted "path<TAB>sha256<NEWLINE>"
lines, and the total size. The self-extractor's 'setup' helper, if present, is left out like
the client does. Reads .tar.xz, .tar.bz2, .tar.gz, .tar and .zip.
"""
import hashlib
import json
import os
import sys
import tarfile
import zipfile


def strip_top(names):
    """The common top folder of all members ('' when there is none)."""
    tops = {n.split("/", 1)[0] for n in names if n}
    if len(tops) == 1 and all("/" in n or n.rstrip("/") == next(iter(tops)) for n in names if n):
        return next(iter(tops)) + "/"
    return ""


def sha_stream(f):
    h = hashlib.sha256()
    for b in iter(lambda: f.read(1 << 20), b""):
        h.update(b)
    return h.hexdigest()


def from_tar(path):
    raw = {}  # member name -> ("file", sha, size) | ("link", target) | ("hard", target)
    with tarfile.open(path, "r|*") as t:
        for m in t:
            name = m.name.lstrip("./") if m.name.startswith("./") else m.name
            if m.isreg():
                raw[name] = ("file", sha_stream(t.extractfile(m)), m.size)
            elif m.issym():
                raw[name] = ("link", m.linkname)
            elif m.islnk():
                raw[name] = ("hard", m.linkname)
    return raw


def from_zip(path):
    raw = {}
    with zipfile.ZipFile(path) as z:
        for i in z.infolist():
            if i.is_dir():
                continue
            mode = (i.external_attr >> 16) & 0o170000
            if mode == 0o120000:
                raw[i.filename] = ("link", z.read(i).decode())
            else:
                with z.open(i) as f:
                    raw[i.filename] = ("file", sha_stream(f), i.file_size)
    return raw


def main():
    if len(sys.argv) not in (2, 3):
        sys.exit(__doc__)
    path = sys.argv[1]
    raw = from_zip(path) if path.endswith(".zip") else from_tar(path)
    top = strip_top(list(raw))
    files, total = {}, 0
    for name, v in raw.items():
        rel = name[len(top):] if top else name
        if not rel or rel == "setup":
            continue
        if v[0] == "file":
            files[rel], total = v[1], total + v[2]
        elif v[0] == "link":
            files[rel] = "link:" + v[1]
        else:  # a hard link has the content of its target
            t = raw.get(v[1])
            if t and t[0] == "file":
                files[rel], total = t[1], total + t[2]
    h = hashlib.sha256()
    for k in sorted(files):
        h.update(f"{k}\t{files[k]}\n".encode())
    fp = {"root": top.rstrip("/") or os.path.basename(path), "tree_hash": h.hexdigest(),
          "files": dict(sorted(files.items())), "bytes": total}
    out = json.dumps(fp, indent=2)
    if len(sys.argv) == 3:
        with open(sys.argv[2], "w") as f:
            f.write(out + "\n")
    else:
        print(out)
    print(f"{path}: {len(files)} files, {total >> 20} MB, tree {fp['tree_hash']}", file=sys.stderr)


if __name__ == "__main__":
    main()
