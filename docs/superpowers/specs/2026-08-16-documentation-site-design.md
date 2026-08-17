# Documentation Site Design

**Issue:** #32 — Ship upgrade, rollback, compatibility, and integration-bundle documentation
**Date:** 2026-08-16
**Status:** Approved and implemented

## Decision

Publish the project's public documentation as a Starlight static site from the
existing repository. The site will live under `website/`, build in GitHub
Actions, and deploy to GitHub Pages. A custom `docs.<domain>` hostname can be
added later without changing the content or build architecture.

The first release documents the current supported product only. It will not
duplicate every page into versioned trees. Compatibility matrices, release
notes, and explicit upgrade guides will describe version boundaries until the
project has at least two actively supported release lines.

## Why This Shape

The existing README mixes onboarding, reference material, operational
runbooks, architecture, and maintenance procedures. It is useful as a source,
but increasingly difficult to navigate and review as one page. A generated
documentation site adds structured navigation and search while preserving
Markdown as the authoring format.

Starlight is selected because it provides a documentation-focused information
architecture, local full-text search, responsive navigation, accessibility,
dark mode, code highlighting, and static output with limited custom code. It
is lighter than adopting Docusaurus solely for versioning that is not yet
needed. Zensical remains promising, but its current versioning path is still
transitional. VitePress is capable, but would require more custom assembly for
the intended documentation structure.

## Repository and Publishing Architecture

- `website/` contains the isolated Node/Astro/Starlight project and all public
  documentation content.
- `README.md` remains the repository landing page: short product overview,
  minimal quick start, development entry points, and links into the site.
- `docs/ai-plans/` and `docs/superpowers/` remain internal engineering
  artifacts and are not included in the published site.
- A dedicated GitHub Actions workflow installs locked dependencies, builds the
  static site, and publishes the output through GitHub Pages.
- The site must work under the repository Pages base path. Custom-domain
  configuration is deferred and must not be required for the first release.
- Documentation deployment is independent of the production MCP service and
  does not require a VPS, database, Qdrant, TEI, credentials, or runtime
  server.

## Information Architecture

The initial sidebar is organized around reader tasks rather than source-code
packages:

1. **Getting Started** — prerequisites, installation, configuration, startup,
   health verification, and client connection.
2. **Upgrade and Rollback** — supported upgrade path, snapshot, image pinning,
   validation, rollback, and failure handling.
3. **Lifecycle** — lifecycle concepts and the dry-run/snapshot/apply/verify/
   rollback migration runbook.
4. **Integration Bundle** — install, update, verify, rollback/removal, client
   activation requirements, and conformance evidence semantics.
5. **Operations** — backups, release checklist, observability, and
   troubleshooting.
6. **Maintenance** — analyze, quarantine, restore, purge, and the deprecated
   `forget_old` behavior with safety constraints.
7. **Reference** — MCP tools, environment variables, defaults, deprecations,
   commands, and compatibility matrix.
8. **Architecture, Security, and Limitations** — trust boundaries, optional
   features, public limitations, and unsupported scenarios.

Existing authoritative documents will be migrated or adapted rather than
copied into two canonical locations. Links from source comments, workflows,
and the README must be updated when a document moves.

## Content and Safety Rules

- Published prose, navigation, examples, and code comments are English.
- Examples use placeholders such as `mcp.example.com`, `<api-key>`, and
  `sha-<commit>`; no personal domains, real tokens, server addresses, or local
  absolute paths are published.
- Commands must reflect actual binaries, flags, environment variables, and
  defaults in the repository at implementation time.
- Destructive maintenance operations remain clearly separated from ordinary
  upgrades. Their prerequisites, confirmation flags, stopped-writer
  requirement, snapshots, archive requirements, and ambiguous-outcome rules
  must not be weakened for brevity.
- The site describes public concepts and safe operations. It does not expose
  private production topology or operator credentials.

## Navigation, Search, and Version Communication

Starlight's sidebar and local Pagefind search are used without a custom search
service. Content remains readable without client-side application state.

The header shows the documentation as applying to the current release. A
compatibility page records supported server, integration-bundle, client, and
dependency expectations. Upgrade pages state explicit source and target
assumptions. True version switching is deferred until multiple release lines
are supported; adding it later requires a separate design because duplicated
documentation has an ongoing maintenance cost.

## Validation and Release Gate

The documentation workflow must fail on an unsuccessful production build or
broken internal content references. The implementation will also include:

- a repository-wide scan for links to moved canonical documents;
- a clean-install rehearsal using only public instructions and placeholders;
- an upgrade rehearsal covering snapshot, pinned image, verification, and
  rollback steps without changing production;
- verification that every issue #32 acceptance topic has a discoverable page;
- a review of environment-variable/default tables against the configuration
  source;
- a GitHub Pages artifact preview or deployment check before completion.

## Rollout and Rollback

Roll out in one documentation-only pull request. The existing README remains a
functional fallback until the site deploy succeeds. If Pages deployment fails,
disable or revert the new documentation workflow and restore moved Markdown
links; no production service rollback is involved.

## Deferred Work

- Custom domain and DNS configuration.
- Multiple version trees and a version selector.
- External hosted search or analytics.
- Interactive API consoles or commands that contact production.
- Localization.
