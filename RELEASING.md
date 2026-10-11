# Publish the SDKs

This repository owns all four SDKs. The root is npm-installable; Python installs from `python/`; Go uses the root module; Java builds from `java/` through the root Maven reactor. GitHub installs need neither npm nor PyPI credentials:

```bash
npm install github:Johnndelembi/universal-sms-sdk
python -m pip install 'universal-sms-sdk @ git+https://github.com/Johnndelembi/universal-sms-sdk.git#subdirectory=python'
go get github.com/Johnndelembi/universal-sms-sdk@main
```

These commands follow main. Pin a commit or release tag for reproducible production installs. They require Git, and an account with repository access if the repository is private.

Java users install via [JitPack](java/README.md), which builds public GitHub commits/tags on demand and resolves Jackson transitively. No Maven Central account is needed. A local `mvn install` is the documented alternative.

## Tagged GitHub release

Update `version` in root `package.json`, `python/pyproject.toml`, root `pom.xml`, `java/pom.xml` and `java/smoke/pom.xml` together. Go uses the Git tag as its version; keep the root module path unchanged for v0/v1 releases. Commit the tested change, then:

```bash
git tag v0.1.0
git push origin main v0.1.0
```

`.github/workflows/sdks.yml` tests supported runtimes, checks matching package/tag versions, builds the npm tarball, Python wheel/source distribution and Java JAR/source JAR/POMs, checks Go with the race detector, smoke-tests installed npm/Python/Java packages, and attaches them to a GitHub Release. GitHub Actions must be enabled and permitted to create releases. No provider, npm or PyPI credentials are required.

Consumers can install the release assets:

```bash
npm install https://github.com/Johnndelembi/universal-sms-sdk/releases/download/v0.1.0/universal-sms-sdk-0.1.0.tgz
python -m pip install https://github.com/Johnndelembi/universal-sms-sdk/releases/download/v0.1.0/universal_sms_sdk-0.1.0-py3-none-any.whl
```

Standard `vX.Y.Z` tags let Go users run `go get github.com/Johnndelembi/universal-sms-sdk@v0.1.0`. Java consumers use that same tag in their JitPack dependency. Legacy `sdk-vX.Y.Z` tags are also accepted by CI, npm, pip and JitPack, but are not standard Go semantic versions. The Java release JAR is a library, not a standalone executable; use Maven/Gradle to resolve Jackson and the parent POM.

The workflow uploads build artifacts on branch pushes and PRs too. A push to main does not automatically create a tag or publish to a package registry. Verify each provider with an authorized account before claiming live compatibility; tests use local fake HTTP responses.

## Optional npm and PyPI publication

To enable `npm install universal-sms-sdk` and `pip install universal-sms-sdk`, secure the name in both registries first. Availability/ownership has not been established and no registry upload has been performed. If the name is taken, update manifests, examples, release asset paths and package smoke tests together; use an npm scope if needed.

After checking the exact release files, log into your registry accounts and publish deliberately:

```bash
npm publish dist/universal-sms-sdk-0.1.0.tgz --access public
python -m pip install twine
python -m twine check dist/*.whl dist/*.tar.gz
python -m twine upload dist/*.whl dist/*.tar.gz
```

MIT licensed; see [LICENSE](LICENSE).

## Maven Central

The local Java coordinates (`io.github.johnndelembi:universal-sms-sdk-java`) are not published to Maven Central. JitPack and source builds are the supported installation paths. Central publication requires namespace ownership, signing and registry credentials; it is separate from pushing this repository.
