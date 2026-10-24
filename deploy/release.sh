#!/bin/sh
# release.sh VERSION [DIST] - builds Vaglio's release archives, the same bytes wherever they are built from the same
# commit with the same Go: static binaries (no cgo, -trimpath, the version and commit stamped in), each in an archive
# whose file times, owners and order are fixed (the commit's time), and SHA256SUMS over them. CI's release job builds
# them twice and compares, then attests where they came from (GitHub's build provenance, signed through Sigstore).
set -eu
version=${1:?usage: release.sh VERSION [DIST]}
dist=${2:-dist}
platforms=${PLATFORMS:-"linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"}
epoch=${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct)}
# A release is built from the tagged commit exactly: any change or untracked file would be stamped into every binary
# as "+dirty" (vcs.modified), and the archives would match no clean build of the tag.
if [ -n "$(git status --porcelain)" ]; then
  echo "release.sh: the working tree is not clean (git status): build from the tagged commit as checked out" >&2
  exit 1
fi
rm -rf "$dist" && mkdir -p "$dist"
work=$(mktemp -d) && trap 'rm -rf "$work"' EXIT
for p in $platforms; do
  os=${p%/*} arch=${p#*/}
  name=vaglio-$version-$os-$arch
  mkdir -p "$work/$name"
  exe=vaglio; [ "$os" = windows ] && exe=vaglio.exe
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -buildvcs=true \
    -ldflags "-s -w -X main.version=$version" -o "$work/$name/$exe" ./cmd/vaglio
  cp LICENSE.md NOTICE README.md "$work/$name/"
  cp -R licenses examples "$work/$name/"
  if [ "$os" = windows ]; then fmt=zip; else fmt=tar.gz; fi
  ${PYTHON:-python3} - "$work" "$name" "$dist/$name.$fmt" "$epoch" <<'PY'
import gzip, io, os, sys, tarfile, time, zipfile
work, name, out, epoch = sys.argv[1], sys.argv[2], sys.argv[3], int(sys.argv[4])
root = os.path.join(work, name)
files = []
for d, dirs, fs in os.walk(root):
    dirs.sort()
    for f in sorted(fs):
        files.append(os.path.relpath(os.path.join(d, f), work))
files.sort()
def mode(rel):
    return 0o755 if rel.endswith(("/vaglio", "/vaglio.exe")) else 0o644
if out.endswith(".zip"):
    stamp = time.gmtime(max(epoch, 315532800))[:6]  # zip times start in 1980
    with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as z:
        for rel in files:
            zi = zipfile.ZipInfo(rel.replace(os.sep, "/"), stamp)
            zi.external_attr = (0o100000 | mode(rel)) << 16
            zi.compress_type = zipfile.ZIP_DEFLATED
            with open(os.path.join(work, rel), "rb") as fh:
                z.writestr(zi, fh.read())
else:
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w", format=tarfile.PAX_FORMAT) as t:
        for rel in files:
            ti = t.gettarinfo(os.path.join(work, rel), arcname=rel.replace(os.sep, "/"))
            ti.mtime, ti.uid, ti.gid, ti.uname, ti.gname, ti.mode = epoch, 0, 0, "root", "root", mode(rel)
            ti.pax_headers = {}
            with open(os.path.join(work, rel), "rb") as fh:
                t.addfile(ti, fh)
    with open(out, "wb") as fh, gzip.GzipFile(fileobj=fh, mode="wb", mtime=0, filename="") as g:
        g.write(buf.getvalue())
PY
done
(cd "$dist" && if command -v sha256sum >/dev/null; then sha256sum vaglio-*; else shasum -a 256 vaglio-*; fi > SHA256SUMS)
cat "$dist/SHA256SUMS"
# Every binary carries a clean build stamp, or the release is refused.
for p in $platforms; do
  os=${p%/*} arch=${p#*/}
  exe=vaglio; [ "$os" = windows ] && exe=vaglio.exe
  if go version -m "$work/vaglio-$version-$os-$arch/$exe" | grep -q 'vcs.modified=true'; then
    echo "release.sh: the $os/$arch binary was built from a modified tree" >&2
    exit 1
  fi
done
