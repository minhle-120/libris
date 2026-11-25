#!/usr/bin/env bash
# The Windows half of scripts/dist/tools-linux.sh: pdftotext and pdftoppm with everything they
# need, into one folder that travels beside libris.exe. Run under an MSYS2 shell (the GitHub
# runner ships one), where the toolchain's own package manager supplies the binaries.
#
#   scripts/dist/tools-windows.sh <out-dir>
#
# This leg is the one the bundle exists for. On Linux the machine usually has poppler already
# and libris prefers it; on Windows "has poppler" almost always means an MSYS2 or Git for
# Windows install whose pdftotext cannot read the C:\ path libris writes it and cannot start
# without the DLL folder it was built beside. So the download's copy is the one that gets used,
# and it has to be complete on its own.
#
# Unlike the Linux bundle, this one carries poppler's data folder — the CMaps and CID tables a
# Chinese or Japanese text layer is decoded with — because on Windows poppler computes that
# path from where its own library sits rather than from a path compiled into it. Put
# libpoppler in the bundle's folder and it looks for <that folder>\share\poppler, which is
# exactly where this script puts it. The same rule gives the bundle its fonts folder. The two
# platforms disagree about a relocation rule inside somebody else's library, so the two
# bundles differ, and the difference belongs in the open rather than in a bug report.
set -euo pipefail

out=${1:?usage: tools-windows.sh "<out-dir>"}

# The programs and the data folder, from the toolchain's package manager. --needed makes this
# a no-op when the runner already has them, so the leg stays one command either way.
if command -v pacman >/dev/null 2>&1; then
  pacman -S --noconfirm --needed \
    mingw-w64-ucrt-x86_64-poppler \
    mingw-w64-ucrt-x86_64-poppler-data \
    mingw-w64-ucrt-x86_64-fontconfig >/dev/null
fi

# The toolchain whose binaries we take. A runner may carry more than one; ucrt is the one
# whose C runtime ships as its own DLL rather than depending on the msys POSIX layer.
prefix=${MINGW_PREFIX:-/ucrt64}
[ -d "$prefix" ] || {
  echo "tools-windows: no $prefix — run this in an MSYS2 shell" >&2
  exit 1
}

mkdir -p "$out"

for prog in pdftotext pdftoppm; do
  src="$prefix/bin/$prog.exe"
  [ -f "$src" ] || {
    echo "tools-windows: $prog is not installed ($src)" >&2
    exit 1
  }
  cp "$src" "$out/"
done

# Every DLL those programs resolve to, plus the ones those DLLs resolve to. ldd under MSYS2
# reports the resolved paths, so one pass over the copies and one over their dependencies
# covers it. What is left out is the operating system's own: anything under Windows, and the
# msys POSIX layer, which is tied to the MSYS2 install it came from and is the single most
# common way a copied Windows program dies on somebody else's machine.
copy_deps() {
  local binary=$1 lib
  ldd "$binary" | awk '{ if ($3 ~ /^\//) print $3; else if ($1 ~ /^\//) print $1 }' |
    sort -u |
    while read -r lib; do
      case "$(cygpath -w "$lib" 2>/dev/null || echo "$lib")" in
      [A-Za-z]:\\Windows\\* | [A-Za-z]:\\WINDOWS\\*) continue ;;
      esac
      case "$lib" in
      */msys-2.0.dll)
        echo "tools-windows: $(basename "$binary") needs msys-2.0.dll, which cannot be copied; the bundle would not start" >&2
        exit 1
        ;;
      esac
      [ -e "$out/$(basename "$lib")" ] || cp -L "$lib" "$out/"
    done
}

for prog in "$out"/*.exe; do copy_deps "$prog"; done
for dll in "$out"/*.dll; do copy_deps "$dll"; done

# poppler's data: the folder the library looks for beside itself, as described above.
if [ -d "$prefix/share/poppler" ]; then
  mkdir -p "$out/share"
  rm -rf "$out/share/poppler"
  cp -r "$prefix/share/poppler" "$out/share/poppler"
else
  echo "tools-windows: no $prefix/share/poppler — CJK text will extract to blanks" >&2
fi

echo "tools: $(cd "$out" && ls -1 | tr '\n' ' ')"
echo "dlls:  $(cd "$out" && ls -1 *.dll 2>/dev/null | wc -l), total $(du -sh "$out" | cut -f1)"
