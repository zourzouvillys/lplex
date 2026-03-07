import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

const config: Config = {
  title: 'lplex',
  tagline: 'CAN bus HTTP bridge for NMEA 2000',
  favicon: 'img/favicon.svg',

  future: {
    v4: true,
  },

  url: 'https://sixfathoms.github.io',
  baseUrl: '/lplex/',

  organizationName: 'sixfathoms',
  projectName: 'lplex',

  onBrokenLinks: 'throw',

  markdown: {
    hooks: {
      onBrokenMarkdownLinks: 'throw',
    },
  },

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  themes: [
    [
      '@easyops-cn/docusaurus-search-local',
      {
        hashed: true,
        docsRouteBasePath: '/',
      },
    ],
  ],

  presets: [
    [
      'classic',
      {
        docs: {
          sidebarPath: './sidebars.ts',
          routeBasePath: '/',
          editUrl: 'https://github.com/sixfathoms/lplex/edit/main/website/',
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  themeConfig: {
    colorMode: {
      respectPrefersColorScheme: true,
    },
    navbar: {
      title: 'lplex',
      items: [
        {
          type: 'docSidebar',
          sidebarId: 'docs',
          position: 'left',
          label: 'Docs',
        },
        {
          href: 'https://github.com/sixfathoms/lplex',
          label: 'GitHub',
          position: 'right',
        },
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Documentation',
          items: [
            {label: 'Introduction', to: '/'},
            {label: 'Getting Started', to: '/getting-started/installation'},
            {label: 'User Guide', to: '/user-guide/lplexdump'},
          ],
        },
        {
          title: 'Developers',
          items: [
            {label: 'HTTP API', to: '/integration/http-api'},
            {label: 'Go Client', to: '/integration/go-client'},
            {label: 'PGN DSL', to: '/pgn-dsl/overview'},
          ],
        },
        {
          title: 'More',
          items: [
            {label: 'GitHub', href: 'https://github.com/sixfathoms/lplex'},
            {label: 'Dockwise', href: 'https://dockwise.app'},
          ],
        },
      ],
      copyright: `Copyright ${new Date().getFullYear()} Six Fathoms.`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['bash', 'json', 'protobuf', 'toml'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
