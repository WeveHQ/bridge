# Verify a Bridge release archive

Select a release from [GitHub Releases](https://github.com/WeveHQ/bridge/releases).
Download the archive for your operating system and architecture and its matching
`.sigstore.json` signature bundle. Linux archive names use `x86_64` for amd64
and `arm64` for arm64. Check the assets on the selected release; this source
tree's documentation may describe features absent from older releases.

Bridge's release workflow signs archives using Cosign and GitHub Actions OIDC.
Verification must constrain both the signer identity and the issuer. With
Cosign installed, replace `<version>` with the selected version without its
leading `v` and run this from the download directory:

```bash
bridge_version='<version>'
archive="weve-bridge_${bridge_version}_linux_x86_64.tar.gz"

cosign verify-blob \
  --bundle "${archive}.sigstore.json" \
  --certificate-identity "https://github.com/WeveHQ/bridge/.github/workflows/release.yml@refs/tags/v${bridge_version}" \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  "$archive"
```

Choose the appropriate archive suffix for your platform. Extract and run the
binary only after verification succeeds:

```bash
tar -xzf "$archive"
```

Record the release version alongside your deployment configuration, then
continue with the [binary installation instructions](../README.md#binary).

The signer identity above comes from this repository's
[release workflow](../.github/workflows/release.yml). If verification fails,
confirm the release, archive, bundle, and expected workflow identity rather than
bypassing the identity check. See [Sigstore's verification documentation](https://docs.sigstore.dev/cosign/verifying/verify/)
for installation prerequisites and verification behavior.

Release automation also generates SHA-256 checksums, SBOMs, and provenance
artifacts. Archive signature verification establishes the archive's signature
and expected signer; it does not by itself verify the separate SLSA provenance.
