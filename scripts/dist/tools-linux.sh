#!/usr/bin/env bash
# Gathers the programs libris runs to read a PDF — pdftotext and pdftoppm — together with
# everything those programs need, into one folder that travels beside the libris executable.
#
# It runs inside the base image named by the caller, not on the machine that calls it, and
# that image is the whole portability promise: the C library of the image the tools were
# gathered in is the oldest C library the result runs on. Everything else the programs ask
# for is copied, so nothing else is asked of the machine they land on. A machine whose C
# library is older than the base image keeps the PATH lookup and says so.
#
#   scripts/dist/tools-linux.sh <out-dir>
#   docker run --rm -v "$PWD":/w "$IMAGE" bash /w/scripts/dist/tools-linux.sh /w/dist/tools
#
# The fonts come along because a page raster is only as good as the fonts it renders with.
# Asked of the machine, they are sometimes absent and always different; asked of the bundle,
# the same PDF makes the same pixels everywhere libris runs. libris names this folder to a
# copied program through FONTCONFIG_FILE — there is no other way for a relocated program to
# find them, and a machine with no fontconfig installed has no configuration to discover.
#
# What is deliberately NOT here is poppler's own data folder, the CMaps and CID tables a
# Chinese or Japanese text layer is decoded with. Its location is compiled into libpoppler as
# an absolute path (/usr/share/poppler) on this platform, so a copied library looks there and
# nowhere the download can reach; a folder beside the executable would be dead weight that
# looks like support. The cost is narrow and worth naming: on a machine with no poppler of its
# own, a CJK text layer extracts to blanks while its page still rasterises. Closing that means
# building poppler from source with a data path libris can point at, which is not a step this
# pipeline takes yet.
set -euo pipefail

out=${1:?usage: tools-linux.sh "<out-dir>"}

export DEBIAN_FRONTEND=noninteractive
apt-get update -qq >/dev/null
apt-get install -y -qq --no-install-recommends \
  poppler-utils fonts-dejavu-core fontconfig >/dev/null

mkdir -p "$out/lib" "$out/fonts" "$out/fonts-conf"

# The programs. -L follows a symlink, because the file that gets copied has to be the
# program itself: a dangling symlink into /usr/bin is a folder that cannot travel.
for prog in pdftotext pdftoppm; do
  cp -L "$(command -v "$prog")" "$out/$prog"
done

# Every library those programs resolve to, except the ones that come from the machine's C
# library. libstdc++ and libgcc are copied on purpose: they belong to the compiler that made
# these binaries, not to the machine they run on, and a version skew between them and the
# binaries is a failure nobody can read.
copy_deps() {
  local binary=$1 lib
  # ldd prints resolved paths in the third column, and the loader itself in the first.
  ldd "$binary" | awk '{ if ($3 ~ /^\//) print $3; else if ($1 ~ /^\//) print $1 }' |
    sort -u |
    while read -r lib; do
      case "$lib" in
      *libc.so* | *libm.so* | *libdl.so* | *librt.so* | *libpthread.so* |
        *libnsl.so* | *libutil.so* | *libresolv.so* | *libanl.so* |
        *libBrokenLocale* | *ld-linux* | *linux-vdso* | *ld-musl*) continue ;;
      esac
      [ -e "$out/lib/$(basename "$lib")" ] || cp -L "$lib" "$out/lib/"
    done
}

for prog in pdftotext pdftoppm; do copy_deps "$out/$prog"; done

# Fonts, and a fontconfig file that looks only at the bundle — so a machine with no fonts
# and a machine with four hundred of them render the same page. The paths are relative,
# because this whole folder moves to wherever the download was unpacked.
cp /usr/share/fonts/truetype/dejavu/DejaVuSans.ttf \
  /usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf \
  /usr/share/fonts/truetype/dejavu/DejaVuSerif.ttf \
  /usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf "$out/fonts/"

cat >"$out/fonts-conf/fonts.conf" <<'CONF'
<?xml version="1.0"?>
<!DOCTYPE fontconfig SYSTEM "fonts.dtd">
<fontconfig>
  <dir prefix="relative">../fonts</dir>
</fontconfig>
CONF
# No <cachedir> of its own: a cache belongs wherever fontconfig puts one by
# default, and the install folder may well be somewhere this program is not
# allowed to write.

# The license texts of everything copied. These are GPL and LGPL works being redistributed by
# the archive, so the folder that holds them has to travel with it rather than be described in
# a release note and left absent.
licenses="$out/share/licenses"
mkdir -p "$licenses"
for pkg in poppler-utils poppler-data libpoppler13 fontconfig libfontconfig1 \
           libfreetype6 libpng16-16 zlib1g libbrotli1 libbz2-1.0 libexpat1 \
           libpcre2-8-0 libcairo2 libpixman-1-0 libjpeg-turbo8 libopenjp2-7 \
           liblcms2-2 libnspr4 libnss3 libglib2.0-0 libgraphite2-3 \
           libharfbuzz0b libdeflate0; do
  [ -d "/usr/share/licenses/$pkg" ] || continue
  cp -r "/usr/share/licenses/$pkg" "$licenses/"
done
# Ubuntu puts some of these in the copyright of the library package itself, which is a file
# rather than a folder; the ones that matter are copied by name above, and an empty licenses
# folder would be worse than a partial one, so this says which happened.
if [ -z "$(ls -A "$licenses" 2>/dev/null)" ]; then
  echo "tools-linux: no license texts found — the archive must not ship without them" >&2
  exit 1
fi

# What the folder holds, so a release log says what shipped rather than implying it.
echo "tools: $(cd "$out" && ls -1 | tr '\n' ' ')"
echo "libs:  $(cd "$out/lib" && ls -1 | wc -l) files, $(du -sh "$out" | cut -f1)"
