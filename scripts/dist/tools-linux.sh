#!/usr/bin/env bash
# Gathers the programs libris runs — pdftotext and pdftoppm to read a PDF, ffmpeg to read a
# recording — together with everything those programs need, into one folder that travels beside
# the libris executable.
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
  poppler-utils ffmpeg fonts-dejavu-core fontconfig >/dev/null

mkdir -p "$out/lib" "$out/fonts" "$out/fonts-conf"

# The programs. -L follows a symlink, because the file that gets copied has to be the
# program itself: a dangling symlink into /usr/bin is a folder that cannot travel.
# ffmpeg from the same package manager as poppler rather than from a static build downloaded
# off a third-party site. It is the same promise the rest of this folder makes — the image's
# glibc is the oldest one the result starts on — with no release pipeline depending on somebody
# else's archive staying where it was, and no checksum to notice going stale. The cost is that
# a distribution builds ffmpeg with everything it ships support for, so the library closure is
# tens of megabytes of codecs libris never asks for: it decodes what a person recorded, and
# today that means PCM, Vorbis, Opus, FLAC and AAC. A build cut to those decoders is the way to
# shrink it, and it is a build step this pipeline does not take yet.
for prog in pdftotext pdftoppm ffmpeg; do
  cp -L "$(command -v "$prog")" "$out/$prog"
done

# Every library those programs resolve to, except the ones that come from the machine's C
# library. libstdc++ and libgcc are copied on purpose: they belong to the compiler that made
# these binaries, not to the machine they run on, and a version skew between them and the
# binaries is a failure nobody can read.
copy_deps() {
  local binary=$1 lib
  # ldd prints resolved paths in the third column, and the loader itself in the first. The
  # C library's own parts are filtered out here rather than by a later grep, so a run whose
  # closure is entirely C-library-shaped is an empty folder and not an exit code 1.
  ldd "$binary" | awk -v skip='(^|/)(libc|libm|libdl|librt|libpthread|libnsl|libutil|libresolv|libanl|libBrokenLocale|ld-linux|linux-vdso|ld-musl)[.-]' '
    {
      lib = ($3 ~ /^\//) ? $3 : (($1 ~ /^\//) ? $1 : "")
      if (lib != "" && lib !~ skip) print lib
    }
  ' | sort -u |
    while read -r lib; do
      [ -e "$out/lib/$(basename "$lib")" ] || cp -L "$lib" "$out/lib/"
    done
}

for prog in pdftotext pdftoppm ffmpeg; do copy_deps "$out/$prog"; done

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

# What the folder holds, so a release log says what shipped rather than implying it.
echo "tools: $(cd "$out" && ls -1 | tr '\n' ' ')"
echo "libs:  $(cd "$out/lib" && ls -1 | wc -l) files, $(du -sh "$out" | cut -f1)"
