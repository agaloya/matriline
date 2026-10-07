# Fingerprints of the official ORCA builds

What `orca.accepted_fingerprints = builtin` accepts: for each official build, the SHA-256
of every file (by its path inside the ORCA folder) and a tree hash over that list. Only
hashes: no part of ORCA is here (docs/ORCA_LICENSE.md). With them, a server needs no ORCA of
its own to accept any official build of its campaign's version.

Made from the downloaded archives with `lab/tools/archprint.py <archive>` (no unpacking),
the same result as `matriline-client fingerprint <unpacked folder>`. The Windows ones come
from the installed folder (the download is an installer), with the AUTOCI zips added as
ORCA's manual says (unzipped into the ORCA folder). A top-level `setup` (the `.run`
installer's helper) is never counted.

| File | Download | Files | Tree hash |
|---|---|---|---|
| orca-6.1.1-linux-x86_64-avx2.json | orca_6_1_1_linux_x86-64_shared_openmpi418_avx2.tar.xz | 3031 | b3d0ee8aff8d9b55... |
| orca-6.1.1-linux-x86_64.json | orca_6_1_1_linux_x86-64_shared_openmpi418.tar.xz | 3031 | 087d462948fb8292... |
| orca-6.1.1-linux-arm64.json | orca_6_1_1_linux_arm64_shared_openmpi418.tar.xz | 3027 | 82f8bc0ea2f2ef3d... |
| orca-6.1.1-linux-riscv64.json | orca_6_1_1_linux_riscv_shared_openmpi418.tar.xz | 3027 | 556c7a3a5cb8547e... |
| orca-6.1.1-macos-arm64.json | orca_6_1_1_macosx_arm64_openmpi411.tar.bz2 | 3026 | aa2740d9a03b6e18... |
| orca-6.1.1-macos-arm64-openblas.json | orca_6_1_1_macosx_arm64_openblas_openmpi411.tar.bz2 | 3026 | 706b249e4f16bb5b... |
| orca-6.1.1-macos-x86_64.json | orca_6_1_1_macosx_intel_openmpi411.tar.bz2 | 3027 | 6b0472906ecfe002... |
| orca-6.1.1-windows-x86_64.json | Orca.6.1.1.Win64_msmpi.zip (installer; Typical and Full install the same files) | 2622 | 2b6c06f26361b3da... |
| orca-6.1.1-windows-x86_64-autoci.json | the same + Orca.6.1.1.Win64_autoci.zip | 2823 | b7c671e918e7cec5... |
| orca-6.1.1-windows-x86_64-autoci-msmpi.json | the same + Orca.6.1.1.Win64_autoci_msmpi.zip | 2823 | 292b81d369344a46... |
| orca-6.1.1-windows-x86_64-autoci-both.json | the same + both AUTOCI zips | 3024 | 2999ba0b450a294d... |

The archprint method was checked against builds hashed earlier on their own systems: Linux
arm64 and both macOS builds give the same tree hashes. The AVX2 Linux build equals the lab's
installed /opt/orca-6.1.1 except for the installer's `setup`.

**Another version or build:** run `matriline-client fingerprint <ORCA folder> > build.json`
on a computer that has it, and list that file in `orca.accepted_fingerprints`. To add it
here for everyone, name it `orca-<version>-<system>.json` (the server picks the files of its
campaign's version by that prefix).
