# Publish the SDKs

This repository owns both SDKs. The root is npm-installable; Python installs from `python/`. GitHub installs need neither npm nor PyPI credentials:

```bash
npm install github:Johnndelembi/universal-sms-sdk
python -m pip install 'universal-sms-sdk @ git+https://github.com/Johnndelembi/universal-sms-sdk.git#subdirectory=python'
```

These commands follow main. Pin a commit or release tag for reproducible production installs. They require Git, and an account with repository access if the repository is private.

## Tagged GitHub release

Update `version` in root `package.json` and `python/pyproject.toml` together. Commit the tested change, then:

```bash
git tag sdk-v0.1.0
git push origin main sdk-v0.1.0
```

`.github/workflows/sdks.yml` tests supported runtimes, checks matching package/tag versions, builds the npm tarball, Python wheel and source distribution, smoke-tests installed packages, and attaches them to a GitHub Release. GitHub Actions must be enabled and permitted to create releases. No provider, npm or PyPI credentials are required.

Consumers can install the release assets:

```bash
npm install https://github.com/Johnndelembi/universal-sms-sdk/releases/download/sdk-v0.1.0/universal-sms-sdk-0.1.0.tgz
python -m pip install https://github.com/Johnndelembi/universal-sms-sdk/releases/download/sdk-v0.1.0/universal_sms_sdk-0.1.0-py3-none-any.whl
```

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
