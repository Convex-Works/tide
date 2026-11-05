# Release

A pushed `v*` tag runs `.github/workflows/release.yml`. It publishes the linux/amd64
and linux/arm64 image to `ghcr.io/convex-works/tide`, and creates the GitHub release
with the static binaries and their `SHA256SUMS`.

Before you tag:

1. Write `docs/releases/<tag>.md`. It becomes the release's notes, and the workflow
   fails without it.
2. Pin the version in the install docs and manifests, and check them with
   `scripts/check-release-pins.sh <version>`. A release tag fails otherwise.
3. Merge both.

A tag with a suffix (`v0.2.0-rc.1`) is a pre-release, which skips the pin check.

GitHub makes a newly pushed container package private. After the first push of a
package, set Convex-Works → Packages → tide → Package settings → visibility to Public,
and check that `docker pull ghcr.io/convex-works/tide:<version>` works logged out.
