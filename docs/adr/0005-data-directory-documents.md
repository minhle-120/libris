# 5. The data directory defaults to Documents/libris

Date: 2026-11-11
Status: accepted, implemented (`cmd/libris/datadir*.go`)

## Context

The first default was `os.UserConfigDir()/libris` — `~/.config/libris` on Linux,
`%APPDATA%\libris` on Windows. That is where application *state* goes, and it is
wrong for what this directory actually holds: the user's kept originals (§2 rule
3), their page rasters, and the database that cites them. It is the only copy of
their corpus. It sits beside caches and settings, in a tree file managers hide by
default, so a person who wants to back it up, email a source to someone, or just
look at what the app has eaten cannot find it.

The directory is also large and long-lived — 85 MB of originals after four PDFs —
which is not what a config directory is for.

## Decision

**The default `-data` is `libris` inside the user's Documents directory**, on
every platform. `-data` still overrides it outright, and nothing in libris moves
data between locations.

Where "the Documents directory" comes from, in order:

| Platform | Source |
|---|---|
| Windows | `HKCU\…\Explorer\User Shell Folders\Personal`, `%USERPROFILE%` expanded |
| Windows fallback | `%USERPROFILE%\Documents` |
| Linux/BSD | `XDG_DOCUMENTS_DIR` from `~/.config/user-dirs.dirs` |
| Linux/BSD fallback | `~/Documents` |
| No usable home | `os.UserConfigDir()/libris`, then `data` beside the working directory |

Two of those look like ceremony and are not:

- **The registry, not `%USERPROFILE%\Documents`,** because Known Folder
  redirection really moves the folder — into OneDrive, or onto another disk — and
  the corpus has to be findable again in whichever place it is. Read through
  `syscall` rather than a dependency (§3 keeps the dependency list at one).
- **`user-dirs.dirs`, not `~/Documents`,** because on a non-English desktop the
  folder is `~/Dokumente` or `~/文档`, and hardcoding the English name would
  silently create a second, empty folder beside it.

A resolved value is used only if it is absolute, anchored (a volume on Windows —
`filepath.IsAbs` calls `\Users\name\Documents` absolute and it then resolves
against whichever drive the process started on), and neither the filesystem root
nor the home directory itself. Broken desktop configurations do write
`XDG_DOCUMENTS_DIR="/"`.

## Consequences

- A returning user with data in the old location starts with an empty library.
  The binary logs the previous directory at startup when it still holds a
  `libris.db`, and does nothing else. Moving a corpus is a person's decision, not
  a side effect of an upgrade — and a wrong guess here deletes originals, which
  §2 rule 3 forbids.
- Documents is often iCloud- or OneDrive-synced. For a local-first app whose
  originals are the point, a synced folder is arguably the right default, but it
  means a corpus can leave the machine. `-data` is the answer, and the log line
  names the path on every start so it is never a surprise.
- macOS uses `~/Documents` and does **not** handle iCloud's Desktop & Documents
  redirection, which moves that folder under `~/Library/Mobile Documents`. Left
  as a known gap rather than guessed at.
- Nothing else changed: no absolute path crosses the API, and `settings.json`,
  `originals/` and `assets/` keep their layout under whatever directory is chosen.
