import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';
import type * as OpenApiPlugin from 'docusaurus-plugin-openapi-docs';

const config: Config = {
  title: 'Task Interleaving',
  tagline:
    'Advisory next-task hints: when a station completes a task, rank the nearest open tasks of the site. A WES-tier Supporting context.',
  favicon: 'img/favicon.svg',

  future: {
    v4: true,
    faster: true,
  },

  url: 'https://iqvo.github.io',
  baseUrl: '/task-interleaving/',

  organizationName: 'IQVO',
  projectName: 'task-interleaving',
  deploymentBranch: 'gh-pages',
  trailingSlash: false,

  onBrokenLinks: 'throw',
  onBrokenAnchors: 'throw',

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  markdown: {
    mermaid: true,
    hooks: {
      onBrokenMarkdownLinks: 'throw',
    },
  },

  presets: [
    [
      'classic',
      {
        docs: {
          sidebarPath: './sidebars.ts',
          editUrl:
            'https://github.com/IQVO/task-interleaving/tree/main/docs/docs/',
          docItemComponent: '@theme/ApiItem',
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  plugins: [
    // The ADRs stay in docs/adr/*.md (other repos link to those paths); this
    // second docs instance serves them in place under /docs/adr.
    [
      '@docusaurus/plugin-content-docs',
      {
        id: 'adr',
        path: 'adr',
        routeBasePath: 'docs/adr',
        sidebarPath: './sidebarsAdr.ts',
        // Keep the 0001- prefix in URLs/ids so they match the file names.
        numberPrefixParser: false,
        editUrl: 'https://github.com/IQVO/task-interleaving/tree/main/docs/adr/',
      },
    ],
    [
      'docusaurus-plugin-openapi-docs',
      {
        id: 'openapi',
        docsPluginId: 'classic',
        config: {
          'task-interleaving': {
            // The single source of truth: the same Spectral-linted spec CI
            // gates on. Never hand-transcribed here.
            specPath: '../apis/openapi.yaml',
            outputDir: 'docs/api-reference/rest',
            sidebarOptions: {
              groupPathsBy: 'tag',
              categoryLinkSource: 'tag',
            },
            hideSendButton: true,
          } satisfies OpenApiPlugin.Options,
        },
      },
    ],
  ],

  themes: ['docusaurus-theme-openapi-docs', '@docusaurus/theme-mermaid'],

  themeConfig: {
    colorMode: {
      respectPrefersColorScheme: true,
    },
    navbar: {
      title: 'Task Interleaving',
      logo: {
        alt: 'Task Interleaving',
        src: 'img/logo.svg',
      },
      items: [
        {
          type: 'docSidebar',
          sidebarId: 'docsSidebar',
          position: 'left',
          label: 'Documentation',
        },
        {
          to: '/docs/api-reference',
          label: 'API Reference',
          position: 'left',
        },
        {
          to: '/docs/adr',
          label: 'ADRs',
          position: 'left',
        },
        {
          href: 'https://github.com/IQVO/task-interleaving',
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
            {label: 'Introduction', to: '/docs/overview/introduction'},
            {label: 'Quickstart', to: '/docs/overview/quickstart'},
            {label: 'Testing', to: '/docs/development/testing'},
            {label: 'API Reference', to: '/docs/api-reference'},
            {label: 'Architecture decisions', to: '/docs/adr'},
          ],
        },
        {
          title: 'Neighbouring contexts',
          items: [
            {label: 'fulfillment-execution', href: 'https://github.com/IQVO/fulfillment-execution'},
            {label: 'facility-layout', href: 'https://github.com/IQVO/facility-layout'},
          ],
        },
        {
          title: 'Source',
          items: [
            {label: 'GitHub repository', href: 'https://github.com/IQVO/task-interleaving'},
            {label: 'OpenAPI spec', href: 'https://raw.githubusercontent.com/IQVO/task-interleaving/main/apis/openapi.yaml'},
            {label: 'AsyncAPI spec', href: 'https://raw.githubusercontent.com/IQVO/task-interleaving/main/apis/asyncapi.yaml'},
          ],
        },
      ],
      copyright: `Task Interleaving — a warehouse-systems bounded context. Built ${new Date().getFullYear()}.`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['bash', 'go', 'json', 'yaml', 'sql'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
