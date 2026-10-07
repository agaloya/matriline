# Using Matriline means using ORCA: its license applies

Matriline distributes ORCA calculations; it does not include, ship or download ORCA. Every
use of Matriline is a use of ORCA, so **every use must also comply with the ORCA End User
License Agreement (EULA)**, on the server and on every computer that lends its CPU.
Matriline's own license (AGPL-3.0, see NOTICE) covers only Matriline's code; it gives no
right whatsoever over ORCA.

ORCA is developed by the Max-Planck-Institut für Kohlenforschung and FACCTs GmbH and
licensed by the Max-Planck-Institut through its Studiengesellschaft Kohle mbH (SGK). Read
the EULA itself, accepted when ORCA is downloaded from the ORCA forum
(https://orcaforum.kofo.mpg.de); its text is theirs and is not reproduced here. This page
is a summary of the points that matter for a distributed setup, written for the EULA
version of June 2025. **It is not legal advice, and the EULA prevails over it.** If in
doubt, ask SGK.

## What it means for a Matriline project

1. **Only academic or private use** (EULA §2). ORCA may be used only in academia (schools,
   colleges, universities, Max-Planck institutes; not institutions connected to the
   military) for research and teaching, or for strictly private, non-commercial,
   non-work-related use by one person. Excluded: commercial research and development,
   work with, for or sponsored by a for-profit organization, and making results available
   free of charge to a for-profit organization. A Matriline campaign must be one of these
   uses.
2. **Every computer needs its own licensed ORCA** (§3a, §3i). ORCA must not be passed on,
   lent or made available to others, nor transferred over a network outside the EULA. So
   the server admin never sends ORCA to helpers: each person who lends a computer
   downloads ORCA from the ORCA forum, accepts the EULA as a licensee and installs it.
   Matriline only checks that the installed build is the expected one (fingerprints).
3. **Calculations for others only among licensees** (§3b). Using ORCA in collaborations
   or project work with for-profit, governmental or non-profit organizations that are not
   academia, including contract calculations for third parties, is not allowed, unless
   all partners hold a valid ORCA license. In Matriline the helpers compute for the
   server's project: **the admin and every helper must each hold a valid ORCA license**,
   and the project must be an academic (or private) one.
4. **Results** (§3c-§3g). Data generated with ORCA may be shared with others only for
   academic purposes; publication in a scientific journal is expressly allowed. Data put
   into a database must be non-commercial, follow the EULA and carry a copyright notice
   referring to the EULA and its disclaimer of warranties; databases built from ORCA data
   may not be used commercially or uploaded where commercial use is allowed, and only for
   academic purposes under an open-source license. Software using ORCA data stays under
   the EULA's conditions. Machine learning on ORCA data: only for academic purposes.
5. **Cite ORCA** (§5). When results obtained with ORCA are published, the EULA requires
   citing: "F. Neese: Software Update: The ORCA Program System—Version 6.0 (WIREs Comput
   Mol Sci 2025, 15:e70019. doi: 10.1002/wcms.70019)", plus the papers of specific methods
   as the ORCA manual describes. Cite Matriline too (CITATION.cff).
6. **No modification of ORCA** (§3h). Matriline never modifies, patches or wraps ORCA's
   programs; it runs the licensee's own installation as it is.
7. **Responsibility**. A research group using ORCA must ensure that its members comply,
   and the licensee is the contact for the MPI and SGK (§2). In a Matriline project the
   server admin should make sure every helper is an ORCA licensee and knows these terms
   before issuing a credential.

## What Matriline does about it

- It contains no ORCA code, binaries or documentation, and never transfers ORCA between
  computers. The lab scripts install ORCA only from the user's own downloads.
- The client uses the ORCA installed on its computer by its owner; the server accepts
  results only from the ORCA builds it was told about (orca.accepted_fingerprints), which
  are hashes, not copies, of those builds.
- The server can run without any ORCA: orca.accepted_fingerprints = builtin accepts the
  official builds of the campaign's version by their fingerprints, compiled into the
  program (src/server/fingerprints/). A fingerprint is only the SHA-256 hash of each file
  of a build: it contains no part of ORCA, cannot be turned back into ORCA and cannot be
  used to run it, so shipping it is not distributing the SOFTWARE (our reading; ask
  FACCTs/MPI if in doubt).
- Helpers are told, when they set up the client, that their ORCA must be their own
  licensed copy and that the EULA applies (INSTALL.md).
