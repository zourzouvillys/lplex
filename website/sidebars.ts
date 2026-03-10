import type {SidebarsConfig} from '@docusaurus/plugin-content-docs';

const sidebars: SidebarsConfig = {
  docs: [
    'intro',
    {
      type: 'category',
      label: 'Getting Started',
      items: [
        'getting-started/installation',
        'getting-started/configuration',
        'getting-started/quick-start',
      ],
    },
    {
      type: 'category',
      label: 'User Guide',
      items: [
        'user-guide/lplexdump',
        'user-guide/streaming',
        'user-guide/filtering',
        'user-guide/journaling',
        'user-guide/retention',
        'user-guide/devices',
        'user-guide/best-practices',
      ],
    },
    {
      type: 'category',
      label: 'Integration',
      items: [
        'integration/http-api',
        'integration/go-client',
        'integration/typescript-client',
        'integration/embedding',
      ],
    },
    {
      type: 'category',
      label: 'Cloud',
      items: [
        'cloud/overview',
        'cloud/self-hosted',
        'cloud/replication',
        'cloud/dockwise',
      ],
    },
    {
      type: 'category',
      label: 'PGN DSL',
      items: [
        'pgn-dsl/overview',
        'pgn-dsl/syntax',
        'pgn-dsl/enums-and-lookups',
        'pgn-dsl/dispatch',
        'pgn-dsl/repeated-fields',
        'pgn-dsl/tutorial',
      ],
    },
    {
      type: 'category',
      label: 'Contributing',
      items: [
        'contributing/overview',
        'contributing/architecture',
        'contributing/journal-format',
      ],
    },
    {
      type: 'category',
      label: 'Releases',
      collapsed: false,
      items: [
        'releases/v0.3.0',
      ],
    },
  ],
};

export default sidebars;
