# website (Docusaurus docs site)

Docusaurus 4 site for lplex documentation. Deployed to GitHub Pages via `.github/workflows/deploy-docs.yml` on pushes to `main` that touch `website/**`.

## Build

```bash
cd website
npm ci
npm run build     # production build into website/build/
npm start         # local dev server with hot reload
```

## Doc Structure

Docs live in `website/docs/` as Markdown. Sidebar order is defined in `sidebars.ts`.

| Section | Path | Covers |
|---|---|---|
| Intro | `docs/intro.md` | What lplex is, high-level overview |
| Getting Started | `docs/getting-started/` | Installation, configuration, quick start |
| User Guide | `docs/user-guide/` | lplexdump, streaming, filtering, journaling, retention, devices, best practices |
| Integration | `docs/integration/` | HTTP API, Go client, TypeScript client, embedding |
| Cloud | `docs/cloud/` | Cloud overview, self-hosted, replication protocol, Dockwise |
| PGN DSL | `docs/pgn-dsl/` | DSL overview, syntax, enums/lookups, dispatch, repeated fields, tutorial |
| Contributing | `docs/contributing/` | Dev overview, architecture, journal format |

## Keeping Docs in Sync

**This is critical.** When making changes to lplex code, update the corresponding docs. Stale docs are worse than no docs.

### What triggers a doc update

- **New or changed CLI flags/config**: update `docs/getting-started/configuration.md`
- **HTTP API changes** (new endpoints, changed request/response shapes, new query params): update `docs/integration/http-api.md`
- **Go client library (`lplexc/`) changes**: update `docs/integration/go-client.md`
- **PGN DSL syntax changes** (new attributes, changed parsing): update the relevant file in `docs/pgn-dsl/`
- **Journal format changes**: update `docs/contributing/journal-format.md`
- **New features in lplexdump**: update `docs/user-guide/lplexdump.md`
- **Streaming/filtering behavior changes**: update `docs/user-guide/streaming.md` or `docs/user-guide/filtering.md`
- **Journaling or retention changes**: update `docs/user-guide/journaling.md` or `docs/user-guide/retention.md`
- **Device discovery changes**: update `docs/user-guide/devices.md`
- **Cloud/replication changes**: update the relevant file in `docs/cloud/`
- **Architecture changes**: update `docs/contributing/architecture.md`
- **New major feature or concept**: add a new doc page and wire it into `sidebars.ts`

### README.md

The root `README.md` is the quick-reference for developers. When a change affects the public interface (new endpoints, new CLI flags, new config options, changed behavior), update the README too. The website docs go deeper; the README stays concise.

### Sidebar

When adding a new doc page, add it to `sidebars.ts` in the appropriate category. The sidebar controls navigation order.
