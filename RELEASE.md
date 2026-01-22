# Release Process

This document describes the release process for gpt-load.

## Overview

The release process is semi-automated using GitHub Actions. When a new tag is pushed, the workflow automatically builds binaries for all supported platforms and creates a draft release. The release notes must be manually added before publishing.

## Automated Steps

When you push a tag (e.g., `v1.0.0`), the GitHub Actions workflow (`.github/workflows/release.yml`) automatically:

1. Collects PGO (Profile-Guided Optimization) profile from tests and benchmarks
2. Builds the frontend once and shares it across all platform builds
3. Builds optimized binaries for all supported platforms:
   - Linux (amd64, arm64)
   - macOS (amd64, arm64)
   - Windows (amd64)
4. Creates a **draft release** with all binaries attached
5. **Does NOT generate release notes automatically** (to avoid duplication and allow manual curation)

## Manual Steps

After the automated workflow completes, you must:

1. Go to the [Releases page](../../releases) on GitHub
2. Find the draft release created by the workflow
3. Edit the draft release
4. Add release notes manually (see format below)
5. Review the attached binaries
6. Publish the release

## Release Notes Format

Release notes should follow this structure:

```markdown
## What's New

- List new features and enhancements
- Use bullet points for clarity

## Bug Fixes

- List bug fixes
- Reference issue numbers when applicable (#123)

## Breaking Changes

- List any breaking changes
- Provide migration instructions if needed

## Performance Improvements

- List performance improvements
- Include benchmark results if available

## Dependencies

- List major dependency updates

## Full Changelog

**Full Changelog**: https://github.com/xunxun1982/gpt-load/compare/v1.0.0...v1.1.0
```

## Version Numbering

Follow [Semantic Versioning](https://semver.org/):

- **MAJOR** version: Incompatible API changes
- **MINOR** version: New functionality in a backward-compatible manner
- **PATCH** version: Backward-compatible bug fixes

Examples: `v1.0.0`, `v1.2.3`, `v2.0.0-beta.1`

## Release Checklist

Before publishing a release:

- [ ] All tests pass
- [ ] Release notes are complete and accurate
- [ ] Version number follows semantic versioning
- [ ] Breaking changes are clearly documented
- [ ] All binaries are attached and named correctly
- [ ] Download links work correctly
- [ ] Documentation is updated (if needed)

## Creating a Release

To create a new release:

```bash
# Ensure you're on the main branch and up to date
git checkout main
git pull origin main

# Create and push a new tag
git tag v1.0.0
git push origin v1.0.0
```

The GitHub Actions workflow will automatically start building the release.

## Binary Naming Convention

Binaries follow this naming pattern:

- `gpt-load-linux-amd64`
- `gpt-load-linux-arm64`
- `gpt-load-macos-amd64`
- `gpt-load-macos-arm64`
- `gpt-load-windows-amd64.exe`

## Post-Release

After publishing a release:

1. Verify download links work
2. Test binaries on different platforms (if possible)
3. Update documentation if needed
4. Announce the release (if applicable)

## Troubleshooting

If the workflow fails:

1. Check the [Actions tab](../../actions) for error details
2. Fix the issue
3. Delete the failed tag: `git tag -d v1.0.0 && git push origin :refs/tags/v1.0.0`
4. Create a new tag after fixing the issue

## Notes

- Draft releases are not visible to the public until published
- You can edit draft releases multiple times before publishing
- Release notes are not auto-generated to allow for manual curation and quality control
- The workflow uses PGO optimization for better performance (3-7% improvement)
